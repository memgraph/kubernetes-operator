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

// Package memgraph provides the narrow client surface the operator uses to
// drive a Memgraph high-availability cluster over Bolt: show instances, add
// coordinator, register instance, set main. All higher layers depend on the
// Client and Connector interfaces, never on the Bolt driver — this package is
// the mock seam for testing and the only place the driver is referenced.
package memgraph

import (
	"context"
	"fmt"
	"strings"
)

// Roles reported in the SHOW INSTANCES role column: coordinators are
// leader/follower, data instances are main/replica.
const (
	RoleLeader   = "leader"
	RoleFollower = "follower"
	RoleMain     = "main"
	RoleReplica  = "replica"
)

// Instance is one row of SHOW INSTANCES: a coordinator or data instance the
// cluster currently knows about.
type Instance struct {
	Name              string
	BoltServer        string
	CoordinatorServer string
	ManagementServer  string
	Health            string
	Role              string
}

// IsLeader reports whether the instance is the current coordinator leader.
func (i Instance) IsLeader() bool {
	return strings.EqualFold(i.Role, RoleLeader)
}

// IsMain reports whether the instance is the current MAIN data instance.
func (i Instance) IsMain() bool {
	return strings.EqualFold(i.Role, RoleMain)
}

// CoordinatorSpec declares one coordinator to add to the cluster. Servers are
// "host:port" addresses the rest of the cluster reaches the coordinator at.
type CoordinatorSpec struct {
	// ID is the Raft coordinator ID (1-based; Memgraph treats ID 0 as unset).
	ID                int32
	BoltServer        string
	CoordinatorServer string
	ManagementServer  string
}

// Name returns the instance name Memgraph derives from the coordinator ID and
// reports in SHOW INSTANCES.
func (c CoordinatorSpec) Name() string {
	return fmt.Sprintf("coordinator_%d", c.ID)
}

// DataInstanceSpec declares one data instance to register with the cluster.
type DataInstanceSpec struct {
	Name              string
	BoltServer        string
	ManagementServer  string
	ReplicationServer string
}

// Client is the narrow surface of a single coordinator's Bolt endpoint. Every
// method issues exactly one HA management query.
type Client interface {
	ShowInstances(ctx context.Context) ([]Instance, error)
	AddCoordinator(ctx context.Context, coordinator CoordinatorSpec) error
	RegisterInstance(ctx context.Context, instance DataInstanceSpec) error
	SetInstanceToMain(ctx context.Context, name string) error
	Close(ctx context.Context) error
}

// Connector opens a Client to a coordinator's "host:port" Bolt address. The
// controller depends on this interface so tests can substitute a fake cluster.
type Connector interface {
	Connect(ctx context.Context, address string) (Client, error)
}
