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

// DeclaredCoordinators is the number of coordinators the spec declares, with
// the CRD's schema default resolved so a spec that never passed admission reads
// the same as one that did.
func DeclaredCoordinators(cluster *memgraphcomv1alpha1.MemgraphCluster) int32 {
	return normalize(cluster.Spec).coordinators
}

// DeclaredDataInstances is the number of data instances the spec declares, with
// the CRD's schema default resolved.
func DeclaredDataInstances(cluster *memgraphcomv1alpha1.MemgraphCluster) int32 {
	return normalize(cluster.Spec).dataInstances
}

// CoordinatorID is the Raft coordinator ID of the coordinator running on the pod
// with the given ordinal. IDs are 1-based because Memgraph treats ID 0 as unset.
func CoordinatorID(ordinal int32) int32 {
	return ordinal + 1
}

// CoordinatorInstanceName and DataInstanceName are the names the members running
// on a pod ordinal are known by in SHOW INSTANCES.
//
// They are exported because this mapping has to exist in exactly one place.
// Anything that matches an observed cluster row against a pod — the rolling
// restart, which has nothing but pods to work from — needs the same derivation the
// builders and the declared topology use, and a second spelling of it would fail
// quietly: a name that is merely wrong matches no row at all, so the caller
// concludes the instance is absent rather than that it asked the wrong question.
func CoordinatorInstanceName(ordinal int32) string {
	return memgraph.CoordinatorSpec{ID: CoordinatorID(ordinal)}.Name()
}

// DataInstanceName is the SHOW INSTANCES name of the data instance on the pod
// with the given ordinal.
func DataInstanceName(ordinal int32) string {
	return fmt.Sprintf("instance_%d", ordinal)
}

// CoordinatorOrdinal and DataInstanceOrdinal are the inverses: the ordinal of the
// pod running the member an observed view names. They live next to the functions
// they invert so the two cannot drift apart, and they are what anything holding a
// name and needing the pod behind it uses — the e2e suite reading MAIN out of
// SHOW INSTANCES, for one.
func CoordinatorOrdinal(name string) (int32, error) {
	id, err := memgraph.CoordinatorIDFromName(name)
	if err != nil {
		return 0, err
	}
	return id - 1, nil
}

// DataInstanceOrdinal is the ordinal of the pod running the named data instance.
func DataInstanceOrdinal(name string) (int32, error) {
	var ordinal int32
	if _, err := fmt.Sscanf(name, "instance_%d", &ordinal); err != nil {
		return 0, fmt.Errorf("parsing data instance name %q: %w", name, err)
	}
	return ordinal, nil
}

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
		topology.Coordinators = append(topology.Coordinators, coordinator(cluster, spec, ordinal))
	}
	for ordinal := range spec.dataInstances {
		topology.DataInstances = append(topology.DataInstances, dataInstance(cluster, spec, ordinal))
	}
	return topology
}

// RetiringCoordinators is the coordinators a lowered coordinators count is
// shedding: pod ordinals [declared, applied), where applied is the replica count
// the operator's own previous apply left on the coordinator StatefulSet. It is
// empty while a cluster grows or holds its size.
//
// Because the count must stay odd, a shrink always retires an even number of
// coordinators, so the surviving Raft cluster keeps an odd membership throughout.
func RetiringCoordinators(
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	applied int32,
) []memgraph.CoordinatorSpec {
	spec := normalize(cluster.Spec)
	if applied <= spec.coordinators {
		return nil
	}

	retiring := make([]memgraph.CoordinatorSpec, 0, applied-spec.coordinators)
	for ordinal := spec.coordinators; ordinal < applied; ordinal++ {
		retiring = append(retiring, coordinator(cluster, spec, ordinal))
	}
	return retiring
}

// RetiringDataInstances is the data instances a lowered dataInstances count is
// shedding: pod ordinals [declared, applied), where applied is the replica count
// the operator's own previous apply left on the data StatefulSet. It is empty
// while a cluster grows or holds its size.
//
// The operator never picks which member retires. A StatefulSet sheds its highest
// ordinals and nothing else, so the range is fully determined by the two counts —
// which is also what keeps an instance the operator did not create out of it.
func RetiringDataInstances(
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	applied int32,
) []memgraph.DataInstanceSpec {
	spec := normalize(cluster.Spec)
	if applied <= spec.dataInstances {
		return nil
	}

	retiring := make([]memgraph.DataInstanceSpec, 0, applied-spec.dataInstances)
	for ordinal := spec.dataInstances; ordinal < applied; ordinal++ {
		retiring = append(retiring, dataInstance(cluster, spec, ordinal))
	}
	return retiring
}

// coordinator describes the coordinator running on the given pod ordinal, as the
// pod itself advertises it. Retiring coordinators are described the same way as
// declared ones: they are members of the Raft cluster under the ID and addresses
// the operator added them with, whether or not the spec still declares them.
func coordinator(
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	spec normalizedSpec,
	ordinal int32,
) memgraph.CoordinatorSpec {
	fqdn := podFQDN(cluster, CoordinatorName(cluster), spec, ordinal)
	return memgraph.CoordinatorSpec{
		ID:                CoordinatorID(ordinal),
		BoltServer:        hostPort(fqdn, spec.ports.bolt),
		CoordinatorServer: hostPort(fqdn, spec.ports.coordinator),
		ManagementServer:  hostPort(fqdn, spec.ports.management),
	}
}

// dataInstance describes the data instance running on the given pod ordinal, as
// the pod itself advertises it. Retiring instances are described the same way as
// declared ones: they are registered under the addresses the operator registered
// them with, whether or not the spec still declares them.
func dataInstance(
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	spec normalizedSpec,
	ordinal int32,
) memgraph.DataInstanceSpec {
	fqdn := podFQDN(cluster, DataName(cluster), spec, ordinal)
	return memgraph.DataInstanceSpec{
		Name:              DataInstanceName(ordinal),
		BoltServer:        hostPort(fqdn, spec.ports.bolt),
		ManagementServer:  hostPort(fqdn, spec.ports.management),
		ReplicationServer: hostPort(fqdn, spec.ports.replication),
	}
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
