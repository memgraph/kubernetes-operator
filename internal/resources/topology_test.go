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

package resources_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/utils/ptr"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
	"github.com/memgraph/kubernetes-operator/internal/memgraph"
	"github.com/memgraph/kubernetes-operator/internal/planner"
	"github.com/memgraph/kubernetes-operator/internal/resources"
)

// secondDataInstance is the data instance on pod ordinal 1: the one the default
// topology's second replica registers as, and the first one a lowered count
// retires.
const secondDataInstance = "instance_1"

func TestDeclaredTopologyDefaults(t *testing.T) {
	got := resources.DeclaredTopology(minimalCluster())

	coordinatorFQDN := func(ordinal int) string {
		return fmt.Sprintf("%s-%d.%s.%s.svc.cluster.local", coordinatorName, ordinal, coordinatorName, testNamespace)
	}
	dataFQDN := func(ordinal int) string {
		return fmt.Sprintf("%s-%d.%s.%s.svc.cluster.local", dataName, ordinal, dataName, testNamespace)
	}

	want := planner.Topology{
		Coordinators: []memgraph.CoordinatorSpec{
			{
				ID:                1,
				BoltServer:        endpoint(coordinatorFQDN(0), memgraphcomv1alpha1.BoltPort),
				CoordinatorServer: endpoint(coordinatorFQDN(0), memgraphcomv1alpha1.CoordinatorPort),
				ManagementServer:  endpoint(coordinatorFQDN(0), memgraphcomv1alpha1.ManagementPort),
			},
			{
				ID:                2,
				BoltServer:        endpoint(coordinatorFQDN(1), memgraphcomv1alpha1.BoltPort),
				CoordinatorServer: endpoint(coordinatorFQDN(1), memgraphcomv1alpha1.CoordinatorPort),
				ManagementServer:  endpoint(coordinatorFQDN(1), memgraphcomv1alpha1.ManagementPort),
			},
			{
				ID:                3,
				BoltServer:        endpoint(coordinatorFQDN(2), memgraphcomv1alpha1.BoltPort),
				CoordinatorServer: endpoint(coordinatorFQDN(2), memgraphcomv1alpha1.CoordinatorPort),
				ManagementServer:  endpoint(coordinatorFQDN(2), memgraphcomv1alpha1.ManagementPort),
			},
		},
		DataInstances: []memgraph.DataInstanceSpec{
			{
				Name:              "instance_0",
				BoltServer:        endpoint(dataFQDN(0), memgraphcomv1alpha1.BoltPort),
				ManagementServer:  endpoint(dataFQDN(0), memgraphcomv1alpha1.ManagementPort),
				ReplicationServer: endpoint(dataFQDN(0), memgraphcomv1alpha1.ReplicationPort),
			},
			{
				Name:              secondDataInstance,
				BoltServer:        endpoint(dataFQDN(1), memgraphcomv1alpha1.BoltPort),
				ManagementServer:  endpoint(dataFQDN(1), memgraphcomv1alpha1.ManagementPort),
				ReplicationServer: endpoint(dataFQDN(1), memgraphcomv1alpha1.ReplicationPort),
			},
		},
	}

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("DeclaredTopology() mismatch (-want +got):\n%s", diff)
	}
}

func TestDeclaredTopologyFollowsReplicaCounts(t *testing.T) {
	got := resources.DeclaredTopology(specifiedCluster())

	if len(got.Coordinators) != 5 {
		t.Errorf("DeclaredTopology() declared %d coordinators, want 5", len(got.Coordinators))
	}
	if len(got.DataInstances) != 3 {
		t.Errorf("DeclaredTopology() declared %d data instances, want 3", len(got.DataInstances))
	}
}

// TestRetiringDataInstances covers the range a lowered dataInstances count
// sheds: the pod ordinals the applied StatefulSet still runs beyond the declared
// count, and nothing else. The bounds are what keep an instance the operator did
// not create out of the range, so they are pinned in both directions.
func TestRetiringDataInstances(t *testing.T) {
	cases := []struct {
		name    string
		cluster *memgraphcomv1alpha1.MemgraphCluster
		applied int32
		want    []string
	}{
		{
			name:    "a cluster holding its size retires nothing",
			cluster: minimalCluster(),
			applied: 2,
		},
		{
			name:    "a growing cluster retires nothing",
			cluster: minimalCluster(),
			applied: 1,
		},
		{
			name:    "the highest ordinal retires when the count drops by one",
			cluster: dataInstancesCluster(2),
			applied: 3,
			want:    []string{"instance_2"},
		},
		{
			name:    "every ordinal above the declared count retires at once",
			cluster: dataInstancesCluster(1),
			applied: 4,
			want:    []string{secondDataInstance, "instance_2", "instance_3"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var names []string
			for _, instance := range resources.RetiringDataInstances(tc.cluster, tc.applied) {
				names = append(names, instance.Name)
			}
			if diff := cmp.Diff(tc.want, names); diff != "" {
				t.Errorf("RetiringDataInstances() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestRetiringCoordinators covers the range a lowered coordinators count sheds.
// The bounds matter more here than for data instances: every member in the range
// loses a Raft vote, so a range that reached past what the operator applied would
// try to remove a coordinator a human added.
func TestRetiringCoordinators(t *testing.T) {
	cases := []struct {
		name    string
		cluster *memgraphcomv1alpha1.MemgraphCluster
		applied int32
		want    []string
	}{
		{
			name:    "a cluster holding its size retires nothing",
			cluster: minimalCluster(),
			applied: 3,
		},
		{
			name:    "a growing cluster retires nothing",
			cluster: coordinatorsCluster(5),
			applied: 3,
		},
		// The count must stay odd, so a shrink always retires an even number of
		// coordinators and the surviving Raft membership stays odd throughout.
		{
			name:    "both ordinals above the declared count retire at once",
			cluster: coordinatorsCluster(3),
			applied: 5,
			want:    []string{"coordinator_4", "coordinator_5"},
		},
		{
			name:    "a larger shrink retires every ordinal above the declared count",
			cluster: coordinatorsCluster(3),
			applied: 7,
			want:    []string{"coordinator_4", "coordinator_5", "coordinator_6", "coordinator_7"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var names []string
			for _, coordinator := range resources.RetiringCoordinators(tc.cluster, tc.applied) {
				names = append(names, coordinator.Name())
			}
			if diff := cmp.Diff(tc.want, names); diff != "" {
				t.Errorf("RetiringCoordinators() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// A retiring coordinator is a Raft member under the ID and addresses the operator
// added it with, so it must be described exactly as the declared coordinator on
// the same ordinal was — otherwise the plan would aim REMOVE COORDINATOR at the
// wrong ID.
func TestRetiringCoordinatorMatchesItsDeclaredForm(t *testing.T) {
	// The tuned cluster (non-default cluster domain) declares three
	// coordinators. Lowering the count is not possible below three, so the declared
	// form is taken from a five-coordinator variant of the same spec.
	grown := tunedCluster()
	grown.Spec.Coordinators = ptr.To(int32(5))
	declared := resources.DeclaredTopology(grown).Coordinators

	shrunk := tunedCluster()
	shrunk.Spec.Coordinators = ptr.To(int32(3))
	got := resources.RetiringCoordinators(shrunk, int32(len(declared)))

	if diff := cmp.Diff(declared[3:], got); diff != "" {
		t.Errorf("RetiringCoordinators() mismatch (-want +got):\n%s", diff)
	}
}

// coordinatorsCluster is the minimal cluster with a different coordinators count,
// the spec side of a coordinator scale.
func coordinatorsCluster(coordinators int32) *memgraphcomv1alpha1.MemgraphCluster {
	cluster := minimalCluster()
	cluster.Spec.Coordinators = ptr.To(coordinators)
	return cluster
}

// dataInstancesCluster is the minimal cluster with a lowered dataInstances count,
// the spec side of a scale-down.
func dataInstancesCluster(dataInstances int32) *memgraphcomv1alpha1.MemgraphCluster {
	cluster := minimalCluster()
	cluster.Spec.DataInstances = ptr.To(dataInstances)
	return cluster
}

// A retiring instance is registered under the addresses the operator registered
// it with, so it must be described exactly as the declared instance on the same
// ordinal was — otherwise the plan would aim its removal at a name the cluster
// does not know.
func TestRetiringDataInstanceMatchesItsDeclaredForm(t *testing.T) {
	// The tuned cluster (non-default cluster domain) declares two
	// instances. Lowering the count to one leaves instance_1 retiring, which must
	// equal the instance_1 the same spec declared before the edit, verbatim.
	declared := resources.DeclaredTopology(tunedCluster()).DataInstances

	shrunk := tunedCluster()
	shrunk.Spec.DataInstances = ptr.To(int32(1))
	got := resources.RetiringDataInstances(shrunk, int32(len(declared)))

	if diff := cmp.Diff(declared[1:], got); diff != "" {
		t.Errorf("RetiringDataInstances() mismatch (-want +got):\n%s", diff)
	}
}

// The registration topology must advertise exactly the identity the
// coordinator pods derive for themselves at startup, otherwise the Raft
// cluster and the registrations disagree about who is who.
func TestDeclaredTopologyMatchesCoordinatorStartScript(t *testing.T) {
	cluster := minimalCluster()
	topology := resources.DeclaredTopology(cluster)
	sts := coordinatorStatefulSet(cluster)
	script := strings.Join(sts.Spec.Template.Spec.Containers[0].Command, "\n")

	// The script derives '<pod-name>.<suffix>' from POD_NAME; every declared
	// coordinator_server must be a pod FQDN under that same suffix.
	suffix := fmt.Sprintf("%s.%s.svc.cluster.local", coordinatorName, testNamespace)
	if !strings.Contains(script, `--coordinator-hostname="${POD_NAME}.`+suffix+`"`) {
		t.Errorf("coordinator start script does not advertise the headless-service pod FQDN:\n%s", script)
	}
	for i, coordinator := range topology.Coordinators {
		wantHost := fmt.Sprintf("%s-%d.%s:%d", coordinatorName, i, suffix, memgraphcomv1alpha1.CoordinatorPort)
		if coordinator.CoordinatorServer != wantHost {
			t.Errorf("coordinator %d advertises %q, want %q", coordinator.ID, coordinator.CoordinatorServer, wantHost)
		}
	}
}

// TestDeclaredTopologyFixedPortsAndClusterDomain asserts the fixed ports and
// configured cluster domain reach every advertised address, since these are
// exactly the addresses the operator registers with the cluster.
func TestDeclaredTopologyFixedPortsAndClusterDomain(t *testing.T) {
	got := resources.DeclaredTopology(tunedCluster())

	coordinatorFQDN := func(ordinal int) string {
		return fmt.Sprintf("%s-%d.%s.%s.svc.k8s.example.com", coordinatorName, ordinal, coordinatorName, testNamespace)
	}
	dataFQDN := func(ordinal int) string {
		return fmt.Sprintf("%s-%d.%s.%s.svc.k8s.example.com", dataName, ordinal, dataName, testNamespace)
	}

	want := planner.Topology{
		Coordinators: []memgraph.CoordinatorSpec{
			{
				ID:                1,
				BoltServer:        endpoint(coordinatorFQDN(0), memgraphcomv1alpha1.BoltPort),
				CoordinatorServer: endpoint(coordinatorFQDN(0), memgraphcomv1alpha1.CoordinatorPort),
				ManagementServer:  endpoint(coordinatorFQDN(0), memgraphcomv1alpha1.ManagementPort),
			},
			{
				ID:                2,
				BoltServer:        endpoint(coordinatorFQDN(1), memgraphcomv1alpha1.BoltPort),
				CoordinatorServer: endpoint(coordinatorFQDN(1), memgraphcomv1alpha1.CoordinatorPort),
				ManagementServer:  endpoint(coordinatorFQDN(1), memgraphcomv1alpha1.ManagementPort),
			},
			{
				ID:                3,
				BoltServer:        endpoint(coordinatorFQDN(2), memgraphcomv1alpha1.BoltPort),
				CoordinatorServer: endpoint(coordinatorFQDN(2), memgraphcomv1alpha1.CoordinatorPort),
				ManagementServer:  endpoint(coordinatorFQDN(2), memgraphcomv1alpha1.ManagementPort),
			},
		},
		DataInstances: []memgraph.DataInstanceSpec{
			{
				Name:              "instance_0",
				BoltServer:        endpoint(dataFQDN(0), memgraphcomv1alpha1.BoltPort),
				ManagementServer:  endpoint(dataFQDN(0), memgraphcomv1alpha1.ManagementPort),
				ReplicationServer: endpoint(dataFQDN(0), memgraphcomv1alpha1.ReplicationPort),
			},
			{
				Name:              secondDataInstance,
				BoltServer:        endpoint(dataFQDN(1), memgraphcomv1alpha1.BoltPort),
				ManagementServer:  endpoint(dataFQDN(1), memgraphcomv1alpha1.ManagementPort),
				ReplicationServer: endpoint(dataFQDN(1), memgraphcomv1alpha1.ReplicationPort),
			},
		},
	}

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("DeclaredTopology() mismatch (-want +got):\n%s", diff)
	}
}

// The coordinator pods must advertise the same configured domain and fixed
// port the registration topology declares for them, otherwise the Raft cluster
// and registrations disagree about who is who.
func TestDeclaredTopologyMatchesTunedCoordinatorStartScript(t *testing.T) {
	cluster := tunedCluster()
	topology := resources.DeclaredTopology(cluster)
	sts := coordinatorStatefulSet(cluster)
	script := strings.Join(sts.Spec.Template.Spec.Containers[0].Command, "\n")

	suffix := fmt.Sprintf("%s.%s.svc.k8s.example.com", coordinatorName, testNamespace)
	if !strings.Contains(script, `--coordinator-hostname="${POD_NAME}.`+suffix+`"`) {
		t.Errorf("coordinator start script does not advertise the configured cluster domain:\n%s", script)
	}
	if !strings.Contains(script, fmt.Sprintf("--coordinator-port=%d", memgraphcomv1alpha1.CoordinatorPort)) {
		t.Errorf("coordinator start script does not listen on the fixed coordinator port:\n%s", script)
	}
	for i, coordinator := range topology.Coordinators {
		wantHost := fmt.Sprintf("%s-%d.%s:%d", coordinatorName, i, suffix, memgraphcomv1alpha1.CoordinatorPort)
		if coordinator.CoordinatorServer != wantHost {
			t.Errorf("coordinator %d advertises %q, want %q", coordinator.ID, coordinator.CoordinatorServer, wantHost)
		}
	}
}
