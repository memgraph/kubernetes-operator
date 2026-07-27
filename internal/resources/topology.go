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

package resources

import (
	"fmt"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
	"github.com/memgraph/kubernetes-operator/internal/memgraph"
	"github.com/memgraph/kubernetes-operator/internal/planner"
)

// DeclaredTopology derives the registration topology the planner drives the
// cluster toward. Identity follows the pod ordinal exactly as the workload
// pods advertise it: coordinator ordinal N is Raft coordinator N+1 (Memgraph
// treats coordinator ID 0 as unset, so IDs stay 1-based), data ordinal N
// registers as instance_N, and every address is the pod's stable DNS name
// within its headless Service.
func DeclaredTopology(cluster *memgraphcomv1alpha1.MemgraphCluster) planner.Topology {
	spec := normalize(cluster.Spec)

	topology := planner.Topology{
		Coordinators:  make([]memgraph.CoordinatorSpec, 0, spec.coordinators),
		DataInstances: make([]memgraph.DataInstanceSpec, 0, spec.dataInstances),
	}
	for ordinal := range spec.coordinators {
		fqdn := podFQDN(cluster, CoordinatorName(cluster), spec, ordinal)
		topology.Coordinators = append(topology.Coordinators, memgraph.CoordinatorSpec{
			ID:                ordinal + 1,
			BoltServer:        hostPort(fqdn, spec.ports.bolt),
			CoordinatorServer: hostPort(fqdn, spec.ports.coordinator),
			ManagementServer:  hostPort(fqdn, spec.ports.management),
		})
	}
	for ordinal := range spec.dataInstances {
		fqdn := podFQDN(cluster, DataName(cluster), spec, ordinal)
		topology.DataInstances = append(topology.DataInstances, memgraph.DataInstanceSpec{
			Name:              fmt.Sprintf("instance_%d", ordinal),
			BoltServer:        hostPort(fqdn, spec.ports.bolt),
			ManagementServer:  hostPort(fqdn, spec.ports.management),
			ReplicationServer: hostPort(fqdn, spec.ports.replication),
		})
	}
	return topology
}

// podFQDNSuffix returns the DNS suffix a pod name is appended to for pods of
// the given headless Service: "<service>.<namespace>.svc.<domain>", where the
// domain is the configured cluster domain.
func podFQDNSuffix(
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	serviceName string,
	spec normalizedSpec,
) string {
	return fmt.Sprintf("%s.%s.svc.%s", serviceName, cluster.Namespace, spec.clusterDomain)
}

// podFQDN returns the stable DNS name of the pod with the given ordinal in
// the StatefulSet backed by the given headless Service (both share one name).
func podFQDN(
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	serviceName string,
	spec normalizedSpec,
	ordinal int32,
) string {
	return fmt.Sprintf("%s-%d.%s", serviceName, ordinal, podFQDNSuffix(cluster, serviceName, spec))
}

func hostPort(host string, port int32) string {
	return fmt.Sprintf("%s:%d", host, port)
}
