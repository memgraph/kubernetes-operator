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

	"github.com/memgraph/kubernetes-operator/internal/memgraph"
	"github.com/memgraph/kubernetes-operator/internal/planner"
)

// declaredTopology is the canonical 3-coordinator, 2-data-instance fixture
// the cases below diff observed cluster states against.
func declaredTopology() planner.Topology {
	topology := planner.Topology{}
	for id := int32(1); id <= 3; id++ {
		topology.Coordinators = append(topology.Coordinators, coordinatorSpec(id))
	}
	for i := range 2 {
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

func observedDataInstance(i int, role string) memgraph.Instance {
	spec := dataInstanceSpec(i)
	return memgraph.Instance{
		Name:             spec.Name,
		BoltServer:       spec.BoltServer,
		ManagementServer: spec.ManagementServer,
		Health:           "up",
		Role:             role,
	}
}

func TestPlan(t *testing.T) {
	declared := declaredTopology()

	cases := []struct {
		name     string
		observed []memgraph.Instance
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
				planner.SetInstanceToMain{Name: "instance_0"},
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
				planner.SetInstanceToMain{Name: "instance_0"},
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
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := planner.Plan(declared, tc.observed)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Plan() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
