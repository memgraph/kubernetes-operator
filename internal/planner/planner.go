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
// and read-before-write: only missing registrations are re-issued, and a MAIN is
// promoted only when the cluster has none. After bootstrap, failover belongs to
// the Raft coordinators; the planner never overrides a MAIN that is staying.
//
// Members a lowered replica count is retiring are the one thing the planner
// removes, and it can order the whole removal in a single pass because it
// predicts every intermediate state: a retiring MAIN is demoted, a survivor is
// promoted in its place, and the retiring members are then unregistered — so no
// UNREGISTER INSTANCE is ever aimed at a MAIN, which Memgraph would refuse.
package planner

import (
	"context"
	"fmt"

	"github.com/memgraph/kubernetes-operator/internal/memgraph"
)

// Topology is the declared cluster registration state: every coordinator and
// data instance the CR says must exist, with the addresses each advertises,
// plus the members on their way out when a count was lowered.
type Topology struct {
	Coordinators  []memgraph.CoordinatorSpec
	DataInstances []memgraph.DataInstanceSpec

	// RetiringCoordinators and RetiringDataInstances are the members a lowered
	// replica count is shedding: still registered and still running, but no
	// longer declared. They are empty while a cluster grows or holds its size.
	//
	// A retiring data instance is demoted if it holds MAIN and then
	// unregistered, so the cluster stops expecting it before its pod goes.
	// RetiringCoordinators is still always empty: removing a Raft member is not
	// implemented yet, so a plan issues no command for one.
	RetiringCoordinators  []memgraph.CoordinatorSpec
	RetiringDataInstances []memgraph.DataInstanceSpec
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

// SetInstanceToMain promotes the named data instance to MAIN: at bootstrap,
// when the cluster has no MAIN yet, and after a retiring MAIN was demoted.
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

// DemoteInstance turns a retiring MAIN back into a replica, which is what makes
// it unregisterable. It is only ever aimed at an instance on its way out of the
// cluster: demoting one that is staying would be the operator overriding a
// failover decision that belongs to the coordinators.
type DemoteInstance struct {
	Name string
}

// Run implements Command.
func (c DemoteInstance) Run(ctx context.Context, client memgraph.Client) error {
	return client.DemoteInstance(ctx, c.Name)
}

func (c DemoteInstance) String() string {
	return "DEMOTE INSTANCE " + c.Name
}

// UnregisterInstance removes a retiring data instance from the cluster, so the
// coordinators stop expecting it before its pod is shed.
type UnregisterInstance struct {
	Name string
}

// Run implements Command.
func (c UnregisterInstance) Run(ctx context.Context, client memgraph.Client) error {
	return client.UnregisterInstance(ctx, c.Name)
}

func (c UnregisterInstance) String() string {
	return "UNREGISTER INSTANCE " + c.Name
}

// Plan diffs the declared topology against the observed instances and returns
// the commands still needed, in execution order: coordinators before data
// instances (registration requires a formed Raft cluster), then the retirement
// of the members a lowered count sheds — demote a retiring MAIN, promote a
// survivor in its place, unregister every retiring member. The promotion sits
// between the two so that no UNREGISTER INSTANCE is ever aimed at an observed
// MAIN, and so the cluster is MAIN-less only for the few milliseconds between
// two queries of the same pass.
//
// Instances the cluster knows but the topology neither declares nor retires are
// left untouched: the retiring set is bounded by the operator's own prior apply,
// so an instance a human registered is never removed.
func Plan(declared Topology, observed []memgraph.Instance) []Command {
	registered := index(observed)
	retiring := retiringNames(declared)

	// A MAIN on its way out does not count as one: it is demoted below, and the
	// cluster needs a survivor promoted in its place.
	hasMain := false
	for _, instance := range observed {
		hasMain = hasMain || (instance.IsMain() && !retiring[instance.Name])
	}

	var commands []Command
	for _, coordinator := range declared.Coordinators {
		if !coordinatorRegistered(registered, coordinator) {
			commands = append(commands, AddCoordinator{Coordinator: coordinator})
		}
	}
	for _, instance := range declared.DataInstances {
		if _, ok := registered[instance.Name]; !ok {
			commands = append(commands, RegisterInstance{Instance: instance})
		}
	}
	for _, instance := range declared.RetiringDataInstances {
		if observed, ok := registered[instance.Name]; ok && observed.IsMain() {
			commands = append(commands, DemoteInstance{Name: instance.Name})
		}
	}
	if !hasMain && len(declared.DataInstances) > 0 {
		commands = append(commands, SetInstanceToMain{Name: promotionTarget(declared, registered)})
	}
	for _, instance := range declared.RetiringDataInstances {
		if _, ok := registered[instance.Name]; ok {
			commands = append(commands, UnregisterInstance{Name: instance.Name})
		}
	}
	return commands
}

// Registered reports how many of the declared coordinators and data instances
// the observed cluster has registered. It is pure observation for the CR's
// status, and it shares Plan's definition of "registered" — so a role's count
// reaches its declared count exactly when Plan stops issuing registrations for
// it.
func Registered(declared Topology, observed []memgraph.Instance) (coordinators, dataInstances int32) {
	registered := index(observed)
	for _, coordinator := range declared.Coordinators {
		if coordinatorRegistered(registered, coordinator) {
			coordinators++
		}
	}
	for _, instance := range declared.DataInstances {
		if _, ok := registered[instance.Name]; ok {
			dataInstances++
		}
	}
	return coordinators, dataInstances
}

// retiringNames is the set of data instances the topology is shedding, keyed by
// the name they are registered under.
func retiringNames(declared Topology) map[string]bool {
	retiring := make(map[string]bool, len(declared.RetiringDataInstances))
	for _, instance := range declared.RetiringDataInstances {
		retiring[instance.Name] = true
	}
	return retiring
}

// index keys the observed cluster view by instance name.
func index(observed []memgraph.Instance) map[string]memgraph.Instance {
	registered := make(map[string]memgraph.Instance, len(observed))
	for _, instance := range observed {
		registered[instance.Name] = instance
	}
	return registered
}

// coordinatorRegistered reports whether the declared coordinator is a member of
// the Raft cluster. A coordinator reports itself in SHOW INSTANCES with an
// empty bolt_server until ADD COORDINATOR is issued for its ID, so presence
// alone does not prove registration.
func coordinatorRegistered(registered map[string]memgraph.Instance, coordinator memgraph.CoordinatorSpec) bool {
	observed, ok := registered[coordinator.Name()]
	return ok && observed.BoltServer != ""
}

// promotionTarget picks the data instance to promote when the cluster has no
// MAIN: the lowest-ordinal declared instance the cluster observes as up.
// Promoting a down instance would only write the intent to Raft and leave the
// cluster MAIN-less until the coordinators retried it, so an instance that is
// known to be reachable is preferred over a lower-ordinal one that is not.
//
// Only declared instances are candidates, which is what makes the rule serve a
// retirement too: promoting a survivor is the same choice as promoting at
// bootstrap, and a member on its way out can never be the target.
//
// The first declared instance is the fallback, which is what a fresh bootstrap
// uses: nothing is observed yet at the point its registrations are planned.
func promotionTarget(declared Topology, registered map[string]memgraph.Instance) string {
	for _, instance := range declared.DataInstances {
		if observed, ok := registered[instance.Name]; ok && observed.IsUp() {
			return instance.Name
		}
	}
	return declared.DataInstances[0].Name
}
