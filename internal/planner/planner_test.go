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

package planner_test

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
	"github.com/memgraph/kubernetes-operator/internal/memgraph"
	"github.com/memgraph/kubernetes-operator/internal/planner"
	"github.com/memgraph/kubernetes-operator/internal/resources"
)

// The data instances the cases below name. firstInstance is the one a bootstrap
// promotes — the first the topology declares — and the one a shrink keeps; the
// higher ordinals are what a lowered count retires.
const (
	firstInstance  = "instance_0"
	secondInstance = "instance_1"
	thirdInstance  = "instance_2"
)

// fourthCoordinator is the lowest-numbered coordinator a shrink from five to
// three retires, and the one the cases below park Raft leadership on: a
// StatefulSet sheds its highest ordinals, so the leader may well sit on one.
const fourthCoordinator = "coordinator_4"

// declaredTopology is the canonical 3-coordinator, 2-data-instance fixture
// the cases below diff observed cluster states against.
func declaredTopology() planner.Topology {
	return topologyOf(3, 2)
}

// grownTopology is the same cluster after both counts were raised: 5
// coordinators and 3 data instances.
func grownTopology() planner.Topology {
	return topologyOf(5, 3)
}

// shrunkTopology is a cluster whose dataInstances count was lowered to the given
// number while its StatefulSet still runs `running` data pods: the ordinals in
// between are retiring.
func shrunkTopology(declared, running int) planner.Topology {
	topology := topologyOf(3, declared)
	for i := declared; i < running; i++ {
		topology.RetiringDataInstances = append(topology.RetiringDataInstances, dataInstanceSpec(i))
	}
	return topology
}

// mixedTopology is one edit moving both counts in opposite directions: the
// coordinators grow from 3 to 5 while the data instances shrink from 3 to 2.
func mixedTopology() planner.Topology {
	topology := topologyOf(5, 2)
	topology.RetiringDataInstances = append(topology.RetiringDataInstances, dataInstanceSpec(2))
	return topology
}

// shrunkCoordinators is a cluster whose coordinators count was lowered from five
// to three — the smallest coordinator shrink the schema floors allow, and an even
// number of members either way — while its StatefulSet still runs all five pods:
// coordinator_4 and coordinator_5 are Raft members on their way out.
func shrunkCoordinators() planner.Topology {
	topology := topologyOf(3, 2)
	for id := int32(4); id <= 5; id++ {
		topology.RetiringCoordinators = append(topology.RetiringCoordinators, coordinatorSpec(id))
	}
	return topology
}

// retiringBothRoles is one edit lowering both counts: the coordinators shrink from
// 5 to 3 while the data instances shrink from 3 to 2.
func retiringBothRoles() planner.Topology {
	topology := shrunkCoordinators()
	topology.RetiringDataInstances = append(topology.RetiringDataInstances, dataInstanceSpec(2))
	return topology
}

func topologyOf(coordinators int32, dataInstances int) planner.Topology {
	topology := planner.Topology{}
	for id := int32(1); id <= coordinators; id++ {
		topology.Coordinators = append(topology.Coordinators, coordinatorSpec(id))
	}
	for i := range dataInstances {
		topology.DataInstances = append(topology.DataInstances, dataInstanceSpec(i))
	}
	return topology
}

// coordinatorSpec builds the declared coordinator with the given 1-based Raft
// ID (Memgraph treats ID 0 as unset), hosted on the pod with ordinal ID-1.
func coordinatorSpec(id int32) memgraph.CoordinatorSpec {
	host := fmt.Sprintf("example-coordinator-%d.example-coordinator.default.svc.cluster.local", id-1)
	return memgraph.CoordinatorSpec{
		ID:                id,
		BoltServer:        host + ":7687",
		CoordinatorServer: host + ":12000",
		ManagementServer:  host + ":10000",
	}
}

func dataInstanceSpec(i int) memgraph.DataInstanceSpec {
	host := fmt.Sprintf("example-data-%d.example-data.default.svc.cluster.local", i)
	return memgraph.DataInstanceSpec{
		Name:              fmt.Sprintf("instance_%d", i),
		BoltServer:        host + ":7687",
		ManagementServer:  host + ":10000",
		ReplicationServer: host + ":20000",
	}
}

func observedCoordinator(id int32, role string) memgraph.Instance {
	spec := coordinatorSpec(id)
	return memgraph.Instance{
		Name:              spec.Name(),
		BoltServer:        spec.BoltServer,
		CoordinatorServer: spec.CoordinatorServer,
		ManagementServer:  spec.ManagementServer,
		Health:            "up",
		Role:              role,
	}
}

// observedCoordinators is the Raft membership a leader reports: one row per given
// coordinator ID, the one named by leaderID reporting itself leader. Which
// coordinator holds leadership is what decides whether a shrink can remove
// members at all, so every retirement case below states it outright.
func observedCoordinators(leaderID int32, ids ...int32) []memgraph.Instance {
	view := make([]memgraph.Instance, 0, len(ids))
	for _, id := range ids {
		role := memgraph.RoleFollower
		if id == leaderID {
			role = memgraph.RoleLeader
		}
		view = append(view, observedCoordinator(id, role))
	}
	return view
}

func observedDataInstance(i int, role string) memgraph.Instance {
	spec := dataInstanceSpec(i)
	return memgraph.Instance{
		Name:             spec.Name,
		BoltServer:       spec.BoltServer,
		ManagementServer: spec.ManagementServer,
		Health:           memgraph.HealthUp,
		Role:             role,
	}
}

// downDataInstance is a registered data instance the coordinator leader cannot
// reach — the state a promotion must route around.
func downDataInstance(i int) memgraph.Instance {
	instance := observedDataInstance(i, memgraph.RoleReplica)
	instance.Health = "down"
	return instance
}

func TestPlan(t *testing.T) {
	cases := []struct {
		name     string
		observed []memgraph.Instance
		// declared overrides the canonical fixture for the cases about a
		// topology whose counts changed.
		declared *planner.Topology
		want     []planner.Command
	}{
		{
			name:     "fresh cluster bootstraps everything and promotes one MAIN",
			observed: nil,
			want: []planner.Command{
				planner.AddCoordinator{Coordinator: coordinatorSpec(1)},
				planner.AddCoordinator{Coordinator: coordinatorSpec(2)},
				planner.AddCoordinator{Coordinator: coordinatorSpec(3)},
				planner.RegisterInstance{Instance: dataInstanceSpec(0)},
				planner.RegisterInstance{Instance: dataInstanceSpec(1)},
				planner.SetInstanceToMain{Name: firstInstance},
			},
		},
		{
			name: "partially registered cluster gets only the missing registrations",
			observed: []memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(3, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleMain),
			},
			want: []planner.Command{
				planner.AddCoordinator{Coordinator: coordinatorSpec(2)},
				planner.RegisterInstance{Instance: dataInstanceSpec(1)},
			},
		},
		{
			name: "self-reporting coordinator with empty bolt server is still added",
			observed: []memgraph.Instance{
				// The coordinator the client is connected to lists itself in
				// SHOW INSTANCES with an empty bolt_server until explicitly
				// added.
				func() memgraph.Instance {
					instance := observedCoordinator(2, memgraph.RoleLeader)
					instance.BoltServer = ""
					return instance
				}(),
				observedCoordinator(1, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleMain),
				observedDataInstance(1, memgraph.RoleReplica),
			},
			want: []planner.Command{
				planner.AddCoordinator{Coordinator: coordinatorSpec(2)},
			},
		},
		{
			name: "fully converged cluster is a no-op",
			observed: []memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleMain),
				observedDataInstance(1, memgraph.RoleReplica),
			},
			want: nil,
		},
		{
			name: "an existing MAIN is never overridden, even on another instance",
			observed: []memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleReplica),
				observedDataInstance(1, memgraph.RoleMain),
			},
			want: nil,
		},
		{
			name: "registered but leaderless data plane still gets the one MAIN promotion",
			observed: []memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleReplica),
				observedDataInstance(1, memgraph.RoleReplica),
			},
			want: []planner.Command{
				planner.SetInstanceToMain{Name: firstInstance},
			},
		},
		{
			name: "missing instance registers without MAIN promotion when a MAIN exists",
			observed: []memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				observedDataInstance(1, memgraph.RoleMain),
			},
			want: []planner.Command{
				planner.RegisterInstance{Instance: dataInstanceSpec(0)},
			},
		},
		{
			name: "multiple lost registrations are all re-issued without a second MAIN",
			// A coordinator and a data instance both lost their registration
			// while instance_1 remained MAIN: every missing registration is
			// re-issued, and no promotion is planned because a MAIN exists.
			observed: []memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(3, memgraph.RoleFollower),
				observedDataInstance(1, memgraph.RoleMain),
			},
			want: []planner.Command{
				planner.AddCoordinator{Coordinator: coordinatorSpec(2)},
				planner.RegisterInstance{Instance: dataInstanceSpec(0)},
			},
		},
		// Promoting a down instance only writes the intent to Raft: the cluster
		// stays MAIN-less until the coordinators retry it. A registered instance
		// the leader can reach is the better target even at a higher ordinal.
		{
			name: "a down instance_0 is skipped in favor of the lowest reachable instance",
			observed: []memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				downDataInstance(0),
				observedDataInstance(1, memgraph.RoleReplica),
			},
			want: []planner.Command{
				planner.SetInstanceToMain{Name: secondInstance},
			},
		},
		{
			name: "a reachable instance_0 is promoted ahead of its higher-ordinal peers",
			observed: []memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleReplica),
				observedDataInstance(1, memgraph.RoleReplica),
			},
			want: []planner.Command{
				planner.SetInstanceToMain{Name: firstInstance},
			},
		},
		// With every declared instance down there is no reachable target, so the
		// first one is promoted anyway: the coordinators act on the intent once
		// the instance comes back, which beats never promoting at all.
		{
			name: "the first instance is promoted when none is reachable",
			observed: []memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				downDataInstance(0),
				downDataInstance(1),
			},
			want: []planner.Command{
				planner.SetInstanceToMain{Name: firstInstance},
			},
		},
		// A grown topology declares members the cluster has never heard of: the
		// diff that restores a lost registration is the same one that registers a
		// new pod, so growth needs no separate plan.
		{
			name: "a grown topology registers only the added members",
			observed: []memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleMain),
				observedDataInstance(1, memgraph.RoleReplica),
			},
			declared: ptr.To(grownTopology()),
			want: []planner.Command{
				planner.AddCoordinator{Coordinator: coordinatorSpec(4)},
				planner.AddCoordinator{Coordinator: coordinatorSpec(5)},
				planner.RegisterInstance{Instance: dataInstanceSpec(2)},
			},
		},
		{
			name: "instances the topology does not declare are left untouched",
			observed: []memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				observedCoordinator(4, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleMain),
				observedDataInstance(1, memgraph.RoleReplica),
				observedDataInstance(2, memgraph.RoleReplica),
			},
			want: nil,
		},
		// A lowered count with MAIN on a survivor needs nothing but the removal:
		// the cluster keeps serving from the instance it is already serving from.
		{
			name: "a retiring instance is unregistered with the surviving MAIN untouched",
			observed: []memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleMain),
				observedDataInstance(1, memgraph.RoleReplica),
				observedDataInstance(2, memgraph.RoleReplica),
			},
			declared: ptr.To(shrunkTopology(2, 3)),
			want: []planner.Command{
				planner.UnregisterInstance{Name: thirdInstance},
			},
		},
		// MAIN on the ordinal that is going away: Memgraph refuses to unregister
		// the MAIN, so it is demoted, a survivor is promoted in its place, and only
		// then is it removed — all in this one plan.
		{
			name: "a retiring MAIN is demoted, a survivor promoted, and only then unregistered",
			observed: []memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleReplica),
				observedDataInstance(1, memgraph.RoleReplica),
				observedDataInstance(2, memgraph.RoleMain),
			},
			declared: ptr.To(shrunkTopology(2, 3)),
			want: []planner.Command{
				planner.DemoteInstance{Name: thirdInstance},
				planner.SetInstanceToMain{Name: firstInstance},
				planner.UnregisterInstance{Name: thirdInstance},
			},
		},
		// The promotion follows the same rule as at bootstrap, so a survivor the
		// leader cannot reach is not the one that gets MAIN.
		{
			name: "a retiring MAIN hands MAIN to the lowest reachable survivor",
			observed: []memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				downDataInstance(0),
				observedDataInstance(1, memgraph.RoleReplica),
				observedDataInstance(2, memgraph.RoleMain),
			},
			declared: ptr.To(shrunkTopology(2, 3)),
			want: []planner.Command{
				planner.DemoteInstance{Name: thirdInstance},
				planner.SetInstanceToMain{Name: secondInstance},
				planner.UnregisterInstance{Name: thirdInstance},
			},
		},
		{
			name: "several retiring instances are removed down to a single survivor",
			observed: []memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleReplica),
				observedDataInstance(1, memgraph.RoleMain),
				observedDataInstance(2, memgraph.RoleReplica),
			},
			declared: ptr.To(shrunkTopology(1, 3)),
			want: []planner.Command{
				planner.DemoteInstance{Name: secondInstance},
				planner.SetInstanceToMain{Name: firstInstance},
				planner.UnregisterInstance{Name: secondInstance},
				planner.UnregisterInstance{Name: thirdInstance},
			},
		},
		// Read-before-write: a retiring member the cluster no longer knows about
		// gets no command, so a reconcile that crashed between the unregistration
		// and the shrink re-plans to just the rest of the work.
		{
			name: "an already-unregistered retiring instance is not unregistered again",
			observed: []memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleMain),
				observedDataInstance(1, memgraph.RoleReplica),
				observedDataInstance(2, memgraph.RoleReplica),
			},
			declared: ptr.To(shrunkTopology(2, 4)),
			want: []planner.Command{
				planner.UnregisterInstance{Name: thirdInstance},
			},
		},
		// Both counts change in one edit, in opposite directions: the coordinators
		// grow while the data instances shrink, and each role's work is planned
		// independently of the other's.
		{
			name: "a mixed grow-and-shrink adds coordinators and retires a data instance",
			observed: []memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleReplica),
				observedDataInstance(1, memgraph.RoleReplica),
				observedDataInstance(2, memgraph.RoleMain),
			},
			declared: ptr.To(mixedTopology()),
			want: []planner.Command{
				planner.AddCoordinator{Coordinator: coordinatorSpec(4)},
				planner.AddCoordinator{Coordinator: coordinatorSpec(5)},
				planner.DemoteInstance{Name: thirdInstance},
				planner.SetInstanceToMain{Name: firstInstance},
				planner.UnregisterInstance{Name: thirdInstance},
			},
		},
		// Coordinators the count drops are removed from Raft outright when the
		// leader is a survivor: their votes are gone before their pods are, and
		// removing a follower needs no leadership dance.
		{
			name: "retiring coordinators are removed from Raft under a surviving leader",
			observed: append(observedCoordinators(1, 1, 2, 3, 4, 5),
				observedDataInstance(0, memgraph.RoleMain),
				observedDataInstance(1, memgraph.RoleReplica),
			),
			declared: ptr.To(shrunkCoordinators()),
			want: []planner.Command{
				planner.RemoveCoordinator{Coordinator: coordinatorSpec(4)},
				planner.RemoveCoordinator{Coordinator: coordinatorSpec(5)},
			},
		},
		// Raft refuses to remove its own leader, so a leader on a retiring ordinal
		// is asked to yield — last in the plan, with nothing after it, because the
		// election picks the successor and only a fresh observation can say who won.
		// The other retiring member still goes in this same pass: removing a
		// follower is safe and predictable.
		{
			name: "a retiring leader yields last, after every removal it can still order",
			observed: append(observedCoordinators(4, 1, 2, 3, 4, 5),
				observedDataInstance(0, memgraph.RoleMain),
				observedDataInstance(1, memgraph.RoleReplica),
			),
			declared: ptr.To(shrunkCoordinators()),
			want: []planner.Command{
				planner.RemoveCoordinator{Coordinator: coordinatorSpec(5)},
				planner.YieldLeadership{Leader: fourthCoordinator},
			},
		},
		// Read-before-write: a coordinator already gone from the Raft membership gets
		// no removal, so a pass that crashed between two removals re-plans to just
		// the rest of the work.
		{
			name: "an already-removed retiring coordinator is not removed again",
			observed: append(observedCoordinators(1, 1, 2, 3, 4),
				observedDataInstance(0, memgraph.RoleMain),
				observedDataInstance(1, memgraph.RoleReplica),
			),
			declared: ptr.To(shrunkCoordinators()),
			want: []planner.Command{
				planner.RemoveCoordinator{Coordinator: coordinatorSpec(4)},
			},
		},
		// Nothing is left to order ahead of the yield: the plan is the yield alone,
		// and the removal of the leader itself waits for the next pass.
		{
			name: "a retiring leader with nothing else to remove plans only the yield",
			observed: append(observedCoordinators(4, 1, 2, 3, 4),
				observedDataInstance(0, memgraph.RoleMain),
				observedDataInstance(1, memgraph.RoleReplica),
			),
			declared: ptr.To(shrunkCoordinators()),
			want: []planner.Command{
				planner.YieldLeadership{Leader: fourthCoordinator},
			},
		},
		// Leadership on a coordinator the topology neither declares nor retires — one
		// a human added — is left where it is: it is not in the way of any removal.
		{
			name: "a leader outside the retiring set is not asked to yield",
			observed: append(observedCoordinators(6, 1, 2, 3, 4, 5, 6),
				observedDataInstance(0, memgraph.RoleMain),
				observedDataInstance(1, memgraph.RoleReplica),
			),
			declared: ptr.To(shrunkCoordinators()),
			want: []planner.Command{
				planner.RemoveCoordinator{Coordinator: coordinatorSpec(4)},
				planner.RemoveCoordinator{Coordinator: coordinatorSpec(5)},
			},
		},
		// Both roles shrinking in one edit: the data instances are retired first
		// (MAIN moved off the one going away), then the Raft members are removed.
		{
			name: "both roles retire in one pass under a surviving leader",
			observed: append(observedCoordinators(1, 1, 2, 3, 4, 5),
				observedDataInstance(0, memgraph.RoleReplica),
				observedDataInstance(1, memgraph.RoleReplica),
				observedDataInstance(2, memgraph.RoleMain),
			),
			declared: ptr.To(retiringBothRoles()),
			want: []planner.Command{
				planner.DemoteInstance{Name: thirdInstance},
				planner.SetInstanceToMain{Name: firstInstance},
				planner.UnregisterInstance{Name: thirdInstance},
				planner.RemoveCoordinator{Coordinator: coordinatorSpec(4)},
				planner.RemoveCoordinator{Coordinator: coordinatorSpec(5)},
			},
		},
		// The same edit with leadership in the way: the data-instance retirement is
		// fully ordered and issues in this pass regardless — only the removal of the
		// leader itself has to wait behind the yield.
		{
			name: "a retiring leader does not hold up the data-instance retirement",
			observed: append(observedCoordinators(4, 1, 2, 3, 4, 5),
				observedDataInstance(0, memgraph.RoleReplica),
				observedDataInstance(1, memgraph.RoleReplica),
				observedDataInstance(2, memgraph.RoleMain),
			),
			declared: ptr.To(retiringBothRoles()),
			want: []planner.Command{
				planner.DemoteInstance{Name: thirdInstance},
				planner.SetInstanceToMain{Name: firstInstance},
				planner.UnregisterInstance{Name: thirdInstance},
				planner.RemoveCoordinator{Coordinator: coordinatorSpec(5)},
				planner.YieldLeadership{Leader: fourthCoordinator},
			},
		},
		// The retiring range is bounded by the operator's own prior apply, which is
		// what keeps an instance a human registered out of it — even one at a
		// higher ordinal than everything the operator ever ran.
		{
			name: "an undeclared instance outside the retiring range is left registered",
			observed: []memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleMain),
				observedDataInstance(1, memgraph.RoleReplica),
				observedDataInstance(2, memgraph.RoleReplica),
				observedDataInstance(3, memgraph.RoleReplica),
			},
			declared: ptr.To(shrunkTopology(2, 3)),
			want: []planner.Command{
				planner.UnregisterInstance{Name: thirdInstance},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			declared := declaredTopology()
			if tc.declared != nil {
				declared = *tc.declared
			}

			got := planner.Plan(declared, tc.observed)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Plan() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestRegistered covers what the CR's status publishes: how many of each role's
// declared members the cluster has registered. It shares Plan's definition of
// registered, so the counts reach the declared ones exactly when Plan falls
// silent.
func TestRegistered(t *testing.T) {
	selfReporting := observedCoordinator(2, memgraph.RoleLeader)
	// The coordinator the client is connected to lists itself with an empty
	// bolt_server until ADD COORDINATOR is issued for its ID.
	selfReporting.BoltServer = ""

	cases := []struct {
		name              string
		declared          planner.Topology
		observed          []memgraph.Instance
		wantCoordinators  int32
		wantDataInstances int32
	}{
		{
			name:     "a fresh cluster has nothing registered",
			declared: declaredTopology(),
		},
		{
			name:     "a converged cluster reports the declared counts",
			declared: declaredTopology(),
			observed: []memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleMain),
				observedDataInstance(1, memgraph.RoleReplica),
			},
			wantCoordinators:  3,
			wantDataInstances: 2,
		},
		{
			name:     "a coordinator that is present but not added does not count",
			declared: declaredTopology(),
			observed: []memgraph.Instance{
				observedCoordinator(1, memgraph.RoleFollower),
				selfReporting,
				observedDataInstance(0, memgraph.RoleMain),
			},
			wantCoordinators:  1,
			wantDataInstances: 1,
		},
		{
			name:     "a grown topology reports the members registered so far",
			declared: grownTopology(),
			observed: []memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				observedCoordinator(4, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleMain),
				observedDataInstance(1, memgraph.RoleReplica),
			},
			wantCoordinators:  4,
			wantDataInstances: 2,
		},
		// Counting only declared members keeps the status a report on the topology
		// the user asked for, not on whatever else the cluster happens to know.
		{
			name:     "members the topology does not declare are not counted",
			declared: declaredTopology(),
			observed: []memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				observedCoordinator(4, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleMain),
				observedDataInstance(1, memgraph.RoleReplica),
				observedDataInstance(2, memgraph.RoleReplica),
			},
			wantCoordinators:  3,
			wantDataInstances: 2,
		},
		// Health is not registration: an instance the leader cannot reach is still
		// a member of the cluster.
		{
			name:     "a down instance still counts as registered",
			declared: declaredTopology(),
			observed: []memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				downDataInstance(0),
				observedDataInstance(1, memgraph.RoleMain),
			},
			wantCoordinators:  3,
			wantDataInstances: 2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			coordinators, dataInstances := planner.Registered(tc.declared, tc.observed)
			if coordinators != tc.wantCoordinators || dataInstances != tc.wantDataInstances {
				t.Errorf("Registered() = (%d, %d), want (%d, %d)",
					coordinators, dataInstances, tc.wantCoordinators, tc.wantDataInstances)
			}
		})
	}
}

// TestPlanUsesConfiguredPortsAndClusterDomain plans a fresh bootstrap over a
// topology derived from a CR with non-default ports and cluster domain: the
// registration commands must carry exactly those addresses, because they are
// what the coordinators will use to reach every instance.
func TestPlanUsesConfiguredPortsAndClusterDomain(t *testing.T) {
	cluster := &memgraphcomv1alpha1.MemgraphCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "memgraph-test"},
		Spec: memgraphcomv1alpha1.MemgraphClusterSpec{
			Coordinators:  ptr.To(int32(1)),
			DataInstances: ptr.To(int32(1)),
			ClusterDomain: "k8s.example.com",
			Ports: memgraphcomv1alpha1.PortsSpec{
				BoltPort:        ptr.To(int32(7777)),
				ManagementPort:  ptr.To(int32(10001)),
				ReplicationPort: ptr.To(int32(20001)),
				CoordinatorPort: ptr.To(int32(12001)),
			},
		},
	}

	coordinatorHost := "example-coordinator-0.example-coordinator.memgraph-test.svc.k8s.example.com"
	dataHost := "example-data-0.example-data.memgraph-test.svc.k8s.example.com"
	want := []planner.Command{
		planner.AddCoordinator{Coordinator: memgraph.CoordinatorSpec{
			ID:                1,
			BoltServer:        coordinatorHost + ":7777",
			CoordinatorServer: coordinatorHost + ":12001",
			ManagementServer:  coordinatorHost + ":10001",
		}},
		planner.RegisterInstance{Instance: memgraph.DataInstanceSpec{
			Name:              firstInstance,
			BoltServer:        dataHost + ":7777",
			ManagementServer:  dataHost + ":10001",
			ReplicationServer: dataHost + ":20001",
		}},
		planner.SetInstanceToMain{Name: firstInstance},
	}

	got := planner.Plan(resources.DeclaredTopology(cluster), nil)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Plan() mismatch (-want +got):\n%s", diff)
	}
}

// A cluster already registered on the configured addresses is converged: the
// planner must not re-issue registrations just because the ports are not the
// defaults.
func TestPlanConvergedOnConfiguredPorts(t *testing.T) {
	cluster := &memgraphcomv1alpha1.MemgraphCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "memgraph-test"},
		Spec: memgraphcomv1alpha1.MemgraphClusterSpec{
			Coordinators:  ptr.To(int32(1)),
			DataInstances: ptr.To(int32(1)),
			ClusterDomain: "k8s.example.com",
			Ports:         memgraphcomv1alpha1.PortsSpec{BoltPort: ptr.To(int32(7777))},
		},
	}
	declared := resources.DeclaredTopology(cluster)

	coordinator := declared.Coordinators[0]
	instance := declared.DataInstances[0]
	observed := []memgraph.Instance{
		{
			Name:              coordinator.Name(),
			BoltServer:        coordinator.BoltServer,
			CoordinatorServer: coordinator.CoordinatorServer,
			ManagementServer:  coordinator.ManagementServer,
			Health:            "up",
			Role:              memgraph.RoleLeader,
		},
		{
			Name:             instance.Name,
			BoltServer:       instance.BoltServer,
			ManagementServer: instance.ManagementServer,
			Health:           "up",
			Role:             memgraph.RoleMain,
		},
	}

	if got := planner.Plan(declared, observed); got != nil {
		t.Errorf("Plan() = %v, want no commands", got)
	}
}
