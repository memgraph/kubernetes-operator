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

package controller

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/memgraph/kubernetes-operator/internal/memgraph"
)

// fakeMemgraph is an in-memory Memgraph HA cluster behind the
// memgraph.Connector seam. It keeps one shared SHOW INSTANCES view, applies
// registration commands to it, and — like the real thing — rejects duplicate
// registrations and second MAIN promotions, so any controller behavior that
// is not read-before-write fails the suite loudly.
type fakeMemgraph struct {
	mu sync.Mutex

	// instances is the cluster view every coordinator serves.
	instances       []memgraph.Instance
	connectAttempts int
	// connectErr, when set, makes every Connect fail — the operator's view of a
	// cluster whose coordinators do not yet answer Bolt.
	connectErr error
	// executed records every mutating command as "<bolt address>: <command>".
	executed []string
}

func newFakeMemgraph() *fakeMemgraph {
	return &fakeMemgraph{}
}

func (f *fakeMemgraph) Connect(_ context.Context, address string) (memgraph.Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connectAttempts++
	if f.connectErr != nil {
		return nil, f.connectErr
	}
	return &fakeClient{cluster: f, address: address}, nil
}

func (f *fakeMemgraph) setConnectErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connectErr = err
}

func (f *fakeMemgraph) connects() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connectAttempts
}

func (f *fakeMemgraph) executedCommands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.executed)
}

func (f *fakeMemgraph) setInstances(instances []memgraph.Instance) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.instances = slices.Clone(instances)
}

type fakeClient struct {
	cluster *fakeMemgraph
	address string
	closed  bool
}

func (c *fakeClient) ShowInstances(context.Context) ([]memgraph.Instance, error) {
	c.cluster.mu.Lock()
	defer c.cluster.mu.Unlock()
	if c.closed {
		return nil, fmt.Errorf("fake memgraph: connection to %s already closed", c.address)
	}
	return slices.Clone(c.cluster.instances), nil
}

func (c *fakeClient) AddCoordinator(_ context.Context, coordinator memgraph.CoordinatorSpec) error {
	return c.execute(fmt.Sprintf("ADD COORDINATOR %d", coordinator.ID), func() error {
		if c.cluster.hasInstance(coordinator.Name()) {
			return fmt.Errorf("fake memgraph: coordinator %s already exists", coordinator.Name())
		}
		c.cluster.instances = append(c.cluster.instances, memgraph.Instance{
			Name:              coordinator.Name(),
			BoltServer:        coordinator.BoltServer,
			CoordinatorServer: coordinator.CoordinatorServer,
			ManagementServer:  coordinator.ManagementServer,
			Health:            "up",
			Role:              memgraph.RoleFollower,
		})
		return nil
	})
}

func (c *fakeClient) RegisterInstance(_ context.Context, instance memgraph.DataInstanceSpec) error {
	return c.execute("REGISTER INSTANCE "+instance.Name, func() error {
		if c.cluster.hasInstance(instance.Name) {
			return fmt.Errorf("fake memgraph: instance %s already registered", instance.Name)
		}
		c.cluster.instances = append(c.cluster.instances, memgraph.Instance{
			Name:             instance.Name,
			BoltServer:       instance.BoltServer,
			ManagementServer: instance.ManagementServer,
			Health:           "up",
			Role:             memgraph.RoleReplica,
		})
		return nil
	})
}

func (c *fakeClient) SetInstanceToMain(_ context.Context, name string) error {
	return c.execute(fmt.Sprintf("SET INSTANCE %s TO MAIN", name), func() error {
		for _, instance := range c.cluster.instances {
			if instance.IsMain() {
				return fmt.Errorf("fake memgraph: %s is already MAIN", instance.Name)
			}
		}
		for i, instance := range c.cluster.instances {
			if instance.Name == name {
				c.cluster.instances[i].Role = memgraph.RoleMain
				return nil
			}
		}
		return fmt.Errorf("fake memgraph: instance %s is not registered", name)
	})
}

func (c *fakeClient) Close(context.Context) error {
	c.cluster.mu.Lock()
	defer c.cluster.mu.Unlock()
	c.closed = true
	return nil
}

// execute records the command and applies it to the shared cluster view.
func (c *fakeClient) execute(command string, apply func() error) error {
	c.cluster.mu.Lock()
	defer c.cluster.mu.Unlock()
	if c.closed {
		return fmt.Errorf("fake memgraph: connection to %s already closed", c.address)
	}
	if err := apply(); err != nil {
		return err
	}
	c.cluster.executed = append(c.cluster.executed, c.address+": "+command)
	return nil
}

// hasInstance must be called with the cluster lock held.
func (f *fakeMemgraph) hasInstance(name string) bool {
	return slices.ContainsFunc(f.instances, func(instance memgraph.Instance) bool {
		return instance.Name == name
	})
}
