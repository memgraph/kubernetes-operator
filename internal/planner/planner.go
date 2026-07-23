/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package planner computes the ordered registration commands that drive an
// observed Memgraph HA cluster toward its declared topology. The logic is a
// pure diff — declared topology plus observed SHOW INSTANCES output in,
// commands out (empty when converged) — so reconciliation stays idempotent
// and read-before-write: only missing registrations are re-issued, and the
// initial MAIN promotion happens exactly once, when no MAIN exists. After
// bootstrap, failover belongs to the Raft coordinators; the planner never
// overrides an existing MAIN.
package planner

import (
	"context"
	"fmt"

	"github.com/memgraph/kubernetes-operator/internal/memgraph"
)

// Topology is the declared cluster registration state: every coordinator and
// data instance the CR says must exist, with the addresses each advertises.
type Topology struct {
	Coordinators  []memgraph.CoordinatorSpec
	DataInstances []memgraph.DataInstanceSpec
}

// Command is one registration step to execute against the coordinator leader.
type Command interface {
	Run(ctx context.Context, client memgraph.Client) error
	fmt.Stringer
}

// AddCoordinator adds one declared coordinator to the Raft cluster.
type AddCoordinator struct {
	Coordinator memgraph.CoordinatorSpec
}

// Run implements Command.
func (c AddCoordinator) Run(ctx context.Context, client memgraph.Client) error {
	return client.AddCoordinator(ctx, c.Coordinator)
}

func (c AddCoordinator) String() string {
	return fmt.Sprintf("ADD COORDINATOR %d", c.Coordinator.ID)
}

// RegisterInstance registers one declared data instance with the cluster.
type RegisterInstance struct {
	Instance memgraph.DataInstanceSpec
}

// Run implements Command.
func (c RegisterInstance) Run(ctx context.Context, client memgraph.Client) error {
	return client.RegisterInstance(ctx, c.Instance)
}

func (c RegisterInstance) String() string {
	return "REGISTER INSTANCE " + c.Instance.Name
}

// SetInstanceToMain promotes the named data instance to MAIN at bootstrap.
type SetInstanceToMain struct {
	Name string
}

// Run implements Command.
func (c SetInstanceToMain) Run(ctx context.Context, client memgraph.Client) error {
	return client.SetInstanceToMain(ctx, c.Name)
}

func (c SetInstanceToMain) String() string {
	return fmt.Sprintf("SET INSTANCE %s TO MAIN", c.Name)
}

// Plan diffs the declared topology against the observed instances and returns
// the commands still needed, in execution order: coordinators before data
// instances (registration requires a formed Raft cluster), the initial MAIN
// promotion last. Instances the cluster knows but the topology does not
// declare are left untouched — unregistration is out of scope for v1.
func Plan(declared Topology, observed []memgraph.Instance) []Command {
	registered := make(map[string]memgraph.Instance, len(observed))
	hasMain := false
	for _, instance := range observed {
		registered[instance.Name] = instance
		hasMain = hasMain || instance.IsMain()
	}

	var commands []Command
	for _, coordinator := range declared.Coordinators {
		// A coordinator reports itself in SHOW INSTANCES with an empty
		// bolt_server until ADD COORDINATOR is issued for its ID, so presence
		// alone does not prove registration.
		if observed, ok := registered[coordinator.Name()]; !ok || observed.BoltServer == "" {
			commands = append(commands, AddCoordinator{Coordinator: coordinator})
		}
	}
	for _, instance := range declared.DataInstances {
		if _, ok := registered[instance.Name]; !ok {
			commands = append(commands, RegisterInstance{Instance: instance})
		}
	}
	if !hasMain && len(declared.DataInstances) > 0 {
		commands = append(commands, SetInstanceToMain{Name: declared.DataInstances[0].Name})
	}
	return commands
}
