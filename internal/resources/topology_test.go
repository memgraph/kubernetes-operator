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
				BoltServer:        coordinatorFQDN(0) + ":7687",
				CoordinatorServer: coordinatorFQDN(0) + ":12000",
				ManagementServer:  coordinatorFQDN(0) + ":10000",
			},
			{
				ID:                2,
				BoltServer:        coordinatorFQDN(1) + ":7687",
				CoordinatorServer: coordinatorFQDN(1) + ":12000",
				ManagementServer:  coordinatorFQDN(1) + ":10000",
			},
			{
				ID:                3,
				BoltServer:        coordinatorFQDN(2) + ":7687",
				CoordinatorServer: coordinatorFQDN(2) + ":12000",
				ManagementServer:  coordinatorFQDN(2) + ":10000",
			},
		},
		DataInstances: []memgraph.DataInstanceSpec{
			{
				Name:              "instance_0",
				BoltServer:        dataFQDN(0) + ":7687",
				ManagementServer:  dataFQDN(0) + ":10000",
				ReplicationServer: dataFQDN(0) + ":20000",
			},
			{
				Name:              secondDataInstance,
				BoltServer:        dataFQDN(1) + ":7687",
				ManagementServer:  dataFQDN(1) + ":10000",
				ReplicationServer: dataFQDN(1) + ":20000",
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
	// The tuned cluster (non-default ports and cluster domain) declares two
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
		wantHost := fmt.Sprintf("%s-%d.%s:12000", coordinatorName, i, suffix)
		if coordinator.CoordinatorServer != wantHost {
			t.Errorf("coordinator %d advertises %q, want %q", coordinator.ID, coordinator.CoordinatorServer, wantHost)
		}
	}
}

// TestDeclaredTopologyPortsAndClusterDomain asserts the configured ports and
// cluster domain reach every advertised address, since these are exactly the
// addresses the operator registers with the cluster.
func TestDeclaredTopologyPortsAndClusterDomain(t *testing.T) {
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
				BoltServer:        coordinatorFQDN(0) + ":7777",
				CoordinatorServer: coordinatorFQDN(0) + ":12001",
				ManagementServer:  coordinatorFQDN(0) + ":10001",
			},
			{
				ID:                2,
				BoltServer:        coordinatorFQDN(1) + ":7777",
				CoordinatorServer: coordinatorFQDN(1) + ":12001",
				ManagementServer:  coordinatorFQDN(1) + ":10001",
			},
			{
				ID:                3,
				BoltServer:        coordinatorFQDN(2) + ":7777",
				CoordinatorServer: coordinatorFQDN(2) + ":12001",
				ManagementServer:  coordinatorFQDN(2) + ":10001",
			},
		},
		DataInstances: []memgraph.DataInstanceSpec{
			{
				Name:              "instance_0",
				BoltServer:        dataFQDN(0) + ":7777",
				ManagementServer:  dataFQDN(0) + ":10001",
				ReplicationServer: dataFQDN(0) + ":20001",
			},
			{
				Name:              secondDataInstance,
				BoltServer:        dataFQDN(1) + ":7777",
				ManagementServer:  dataFQDN(1) + ":10001",
				ReplicationServer: dataFQDN(1) + ":20001",
			},
		},
	}

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("DeclaredTopology() mismatch (-want +got):\n%s", diff)
	}
}

// The coordinator pods must advertise the same non-default identity the
// registration topology declares for them, otherwise the Raft cluster and the
// registrations disagree about who is who.
func TestDeclaredTopologyMatchesTunedCoordinatorStartScript(t *testing.T) {
	cluster := tunedCluster()
	topology := resources.DeclaredTopology(cluster)
	sts := coordinatorStatefulSet(cluster)
	script := strings.Join(sts.Spec.Template.Spec.Containers[0].Command, "\n")

	suffix := fmt.Sprintf("%s.%s.svc.k8s.example.com", coordinatorName, testNamespace)
	if !strings.Contains(script, `--coordinator-hostname="${POD_NAME}.`+suffix+`"`) {
		t.Errorf("coordinator start script does not advertise the configured cluster domain:\n%s", script)
	}
	if !strings.Contains(script, "--coordinator-port=12001") {
		t.Errorf("coordinator start script does not listen on the configured coordinator port:\n%s", script)
	}
	for i, coordinator := range topology.Coordinators {
		wantHost := fmt.Sprintf("%s-%d.%s:12001", coordinatorName, i, suffix)
		if coordinator.CoordinatorServer != wantHost {
			t.Errorf("coordinator %d advertises %q, want %q", coordinator.ID, coordinator.CoordinatorServer, wantHost)
		}
	}
}
