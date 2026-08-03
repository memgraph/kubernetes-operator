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
// drive a Memgraph high-availability cluster over Bolt: show instances, show
// replication lag, add coordinator, register instance, set main, and — for the
// members a lowered replica count is retiring — demote and unregister a data
// instance, yield coordinator leadership and remove a coordinator. All higher
// layers depend on the Client and Connector interfaces, never on the Bolt driver
// — this package is the mock seam for testing and the only place the driver is
// referenced.
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

// HealthUp is the SHOW INSTANCES health of an instance the coordinator leader
// currently reaches. Anything else — "down", or "unknown" from a coordinator
// that does not health-check the data plane — means it does not.
const HealthUp = "up"

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

// IsUp reports whether the coordinator leader currently reaches the instance.
func (i Instance) IsUp() bool {
	return strings.EqualFold(i.Health, HealthUp)
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
	return fmt.Sprintf(coordinatorNameFormat, c.ID)
}

// CoordinatorIDFromName is the inverse of Name: the Raft ID of the coordinator a
// view names. It sits here rather than with its callers so that the format and its
// parser cannot drift — a changed name would otherwise leave the parser silently
// matching nothing.
func CoordinatorIDFromName(name string) (int32, error) {
	var id int32
	if _, err := fmt.Sscanf(name, coordinatorNameFormat, &id); err != nil {
		return 0, fmt.Errorf("parsing coordinator name %q: %w", name, err)
	}
	return id, nil
}

// coordinatorNameFormat is how Memgraph derives a coordinator's instance name
// from its Raft ID, stated once for both directions.
const coordinatorNameFormat = "coordinator_%d"

// DataInstanceSpec declares one data instance to register with the cluster.
type DataInstanceSpec struct {
	Name              string
	BoltServer        string
	ManagementServer  string
	ReplicationServer string
}

// DatabaseLag is one database's replication progress on one data instance: how
// many transactions it has committed, and how many that leaves it behind the
// MAIN. The count behind can be negative for a moment after a failover — a SYNC
// replica can hold transactions the new MAIN never saw — so "not behind" is the
// condition worth testing, never "exactly equal".
type DatabaseLag struct {
	Database       string
	CommittedTxns  int64
	TxnsBehindMain int64
}

// ReplicationLag is one row of SHOW REPLICATION LAG: one data instance's
// replication progress across every database it holds. The MAIN reports itself
// too, at zero behind, because the lag of every other instance is measured
// against it.
type ReplicationLag struct {
	Instance  string
	Databases []DatabaseLag
}

// IsCaughtUp reports whether the instance holds every transaction the MAIN has
// committed, in every one of its databases — which is what makes it promotable
// without losing writes.
//
// An instance with no databases reported is not caught up. That is the answer
// for anything the view does not cover: an instance the MAIN does not list, or a
// whole view that came back empty because there is no MAIN to measure against.
// Unknown has to read as "not safe to promote", because the alternative is
// promoting on an assumption and discarding whatever the survivor never received.
func (l ReplicationLag) IsCaughtUp() bool {
	if len(l.Databases) == 0 {
		return false
	}
	for _, database := range l.Databases {
		if database.TxnsBehindMain > 0 {
			return false
		}
	}
	return true
}

// Client is the narrow surface of a single coordinator's Bolt endpoint. Every
// method issues exactly one HA management query.
type Client interface {
	ShowInstances(ctx context.Context) ([]Instance, error)

	// ShowReplicationLag reports how far behind the MAIN every data instance the
	// cluster knows is, counted in committed transactions. Only a coordinator
	// answers it, and the answer is relayed from the MAIN itself — so a cluster
	// with no MAIN, or one whose MAIN the coordinator leader cannot reach, reports
	// no rows rather than failing. An empty view therefore means "cannot tell",
	// which is why nothing is promoted on the strength of it.
	ShowReplicationLag(ctx context.Context) ([]ReplicationLag, error)

	AddCoordinator(ctx context.Context, coordinator CoordinatorSpec) error
	RegisterInstance(ctx context.Context, instance DataInstanceSpec) error
	SetInstanceToMain(ctx context.Context, name string) error

	// DemoteInstance turns the named MAIN back into a replica, which is what
	// makes a MAIN on its way out of the cluster unregisterable: Memgraph
	// refuses to unregister the MAIN. It deliberately leaves the cluster
	// MAIN-less — the coordinators fail over only on a leadership change or a
	// failed ping, so the caller promotes a survivor itself.
	DemoteInstance(ctx context.Context, name string) error

	// UnregisterInstance removes the named data instance from the cluster, so
	// the coordinators stop expecting it before its pod goes away.
	UnregisterInstance(ctx context.Context, name string) error

	// RemoveCoordinator drops the coordinator with the given Raft ID from the
	// Raft cluster, so its vote is gone before its pod is. Raft refuses to
	// remove its own leader, so the caller must never aim this at the leader —
	// YieldLeadership moves leadership away first.
	//
	// The removed coordinator keeps running and keeps its state: NuRaft only
	// stops it from campaigning, which is what makes a later ADD COORDINATOR on
	// the retained volume safe.
	RemoveCoordinator(ctx context.Context, id int32) error

	// YieldLeadership makes the coordinator this client is connected to give up
	// Raft leadership. It has to be issued on the leader itself and cannot name
	// a successor — NuRaft's election picks one — so its outcome is not
	// predictable and the caller must re-observe the cluster afterwards.
	YieldLeadership(ctx context.Context) error

	Close(ctx context.Context) error
}

// Connector opens a Client to a coordinator's "host:port" Bolt address. The
// controller depends on this interface so tests can substitute a fake cluster.
type Connector interface {
	Connect(ctx context.Context, address string) (Client, error)
}
