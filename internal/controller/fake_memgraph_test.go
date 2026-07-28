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
	"strconv"
	"strings"
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
	instances []memgraph.Instance
	// staleViews replaces the shared view for individual coordinator addresses:
	// a coordinator that lost the leader answers from its own state machine, so
	// what it reports need not match the cluster at all. Commands still land on
	// the shared view — a stale coordinator is never written to.
	staleViews      map[string][]memgraph.Instance
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

// setStaleView makes the coordinator at the given Bolt address answer
// SHOW INSTANCES with its own view instead of the cluster's.
func (f *fakeMemgraph) setStaleView(address string, instances []memgraph.Instance) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.staleViews == nil {
		f.staleViews = map[string][]memgraph.Instance{}
	}
	f.staleViews[address] = slices.Clone(instances)
}

type fakeClient struct {
	cluster *fakeMemgraph
	address string
	closed  bool
}

// ShowInstances serves the shared cluster view, plus the connected
// coordinator's own row when the cluster does not know it yet. That row is how
// a real coordinator answers before it is added: its initial Raft configuration
// holds itself alone and it is started as the leader of that one-member
// cluster, so it names itself leader and reports an empty bolt_server until
// ADD COORDINATOR fills the address in. The row is not written into the shared
// view — a coordinator the cluster has lost track of speaks only for itself.
func (c *fakeClient) ShowInstances(context.Context) ([]memgraph.Instance, error) {
	c.cluster.mu.Lock()
	defer c.cluster.mu.Unlock()
	if c.closed {
		return nil, fmt.Errorf("fake memgraph: connection to %s already closed", c.address)
	}
	if stale, ok := c.cluster.staleViews[c.address]; ok {
		return slices.Clone(stale), nil
	}
	self, err := c.selfName()
	if err != nil {
		return nil, err
	}
	view := slices.Clone(c.cluster.instances)
	if !c.cluster.hasInstance(self) {
		view = append(view, memgraph.Instance{Name: self, Health: "up", Role: memgraph.RoleLeader})
	}
	return view, nil
}

func (c *fakeClient) AddCoordinator(_ context.Context, coordinator memgraph.CoordinatorSpec) error {
	return c.execute(fmt.Sprintf("ADD COORDINATOR %d", coordinator.ID), func() error {
		for i, instance := range c.cluster.instances {
			if instance.Name != coordinator.Name() {
				continue
			}
			if instance.BoltServer != "" {
				return fmt.Errorf("fake memgraph: coordinator %s already exists", coordinator.Name())
			}
			c.cluster.instances[i].BoltServer = coordinator.BoltServer
			c.cluster.instances[i].CoordinatorServer = coordinator.CoordinatorServer
			c.cluster.instances[i].ManagementServer = coordinator.ManagementServer
			return nil
		}
		// Adding the coordinator that is serving this connection materializes
		// the row it has been reporting for itself, so it keeps its leadership;
		// any other coordinator joins the formed cluster as a follower.
		self, err := c.selfName()
		if err != nil {
			return err
		}
		role := memgraph.RoleFollower
		if coordinator.Name() == self {
			role = memgraph.RoleLeader
		}
		c.cluster.instances = append(c.cluster.instances, memgraph.Instance{
			Name:              coordinator.Name(),
			BoltServer:        coordinator.BoltServer,
			CoordinatorServer: coordinator.CoordinatorServer,
			ManagementServer:  coordinator.ManagementServer,
			Health:            "up",
			Role:              role,
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

// DemoteInstance turns the named MAIN back into a replica, and — like the real
// thing — refuses an instance that is not MAIN, so the operator's read-before-write
// is what has to keep this call meaningful.
func (c *fakeClient) DemoteInstance(_ context.Context, name string) error {
	return c.execute("DEMOTE INSTANCE "+name, func() error {
		for i, instance := range c.cluster.instances {
			if instance.Name != name {
				continue
			}
			if !instance.IsMain() {
				return fmt.Errorf("fake memgraph: instance %s is not MAIN", name)
			}
			c.cluster.instances[i].Role = memgraph.RoleReplica
			return nil
		}
		return fmt.Errorf("fake memgraph: instance %s is not registered", name)
	})
}

// UnregisterInstance removes the named data instance from the cluster view. It
// rejects an unregistered name and, as Memgraph does, the MAIN — so a plan that
// aims an unregistration at a MAIN fails the suite loudly.
func (c *fakeClient) UnregisterInstance(_ context.Context, name string) error {
	return c.execute("UNREGISTER INSTANCE "+name, func() error {
		for i, instance := range c.cluster.instances {
			if instance.Name != name {
				continue
			}
			if instance.IsMain() {
				return fmt.Errorf("fake memgraph: instance %s is MAIN", name)
			}
			c.cluster.instances = slices.Delete(c.cluster.instances, i, i+1)
			return nil
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

// selfName is the instance name of the coordinator this connection is served
// by. Addresses are the resource builders' pod FQDNs
// ("<statefulset>-<ordinal>.<service>.<namespace>.svc.<domain>:<port>") and the
// coordinator on pod ordinal N runs with Raft ID N+1.
func (c *fakeClient) selfName() (string, error) {
	pod, _, _ := strings.Cut(c.address, ".")
	dash := strings.LastIndex(pod, "-")
	if dash < 0 {
		return "", fmt.Errorf("fake memgraph: %s is not a pod address", c.address)
	}
	ordinal, err := strconv.Atoi(pod[dash+1:])
	if err != nil {
		return "", fmt.Errorf("fake memgraph: %s carries no pod ordinal: %w", c.address, err)
	}
	return fmt.Sprintf("coordinator_%d", ordinal+1), nil
}

// hasInstance must be called with the cluster lock held.
func (f *fakeMemgraph) hasInstance(name string) bool {
	return slices.ContainsFunc(f.instances, func(instance memgraph.Instance) bool {
		return instance.Name == name
	})
}
