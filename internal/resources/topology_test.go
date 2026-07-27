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

	"github.com/memgraph/kubernetes-operator/internal/memgraph"
	"github.com/memgraph/kubernetes-operator/internal/planner"
	"github.com/memgraph/kubernetes-operator/internal/resources"
)

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
				Name:              "instance_1",
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

// The registration topology must advertise exactly the identity the
// coordinator pods derive for themselves at startup, otherwise the Raft
// cluster and the registrations disagree about who is who.
func TestDeclaredTopologyMatchesCoordinatorStartScript(t *testing.T) {
	cluster := minimalCluster()
	topology := resources.DeclaredTopology(cluster)
	sts := resources.CoordinatorStatefulSet(cluster)
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
				Name:              "instance_1",
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
	sts := resources.CoordinatorStatefulSet(cluster)
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
