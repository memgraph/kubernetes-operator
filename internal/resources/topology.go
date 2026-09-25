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

// CoordinatorID is the zero-based Raft coordinator ID of the coordinator
// running on the pod with the given ordinal.
func CoordinatorID(ordinal int32) int32 {
	return ordinal
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
	return id, nil
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
// pods advertise it: coordinator ordinal N is Raft coordinator N, data ordinal
// N registers as instance_N, and every address the cluster reaches a member
// over is the pod's stable DNS name within its headless Service.
//
// The bolt address is the exception, because it is not for the cluster: it is
// what the coordinators hand clients in the routing table, so it has to be
// where clients are. A member with a known external address is announced at
// it; every other member is announced at its pod address, which is also what
// an unexposed cluster announces throughout. The topology is recomputed from
// the current addresses on every pass, so the announced address follows the
// external one as it appears, changes or goes away.
func DeclaredTopology(
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	external ExternalAddresses,
) planner.Topology {
	spec := normalize(cluster.Spec)

	topology := planner.Topology{
		Coordinators:  make([]memgraph.CoordinatorSpec, 0, spec.coordinators),
		DataInstances: make([]memgraph.DataInstanceSpec, 0, spec.dataInstances),
		// A single data instance is the whole cluster: it has to serve reads
		// too, or the routing table names nowhere to read from.
		ReadsOnMain: spec.dataInstances == 1,
	}
	for ordinal := range spec.coordinators {
		topology.Coordinators = append(topology.Coordinators,
			coordinator(cluster, spec, ordinal, external.Coordinators))
	}
	for ordinal := range spec.dataInstances {
		topology.DataInstances = append(topology.DataInstances,
			dataInstance(cluster, spec, ordinal, external.Data[ordinal]))
	}
	return topology
}

// CoordinatorEndpoint is where the operator itself reaches one coordinator over
// Bolt: always the pod's own address, never the announced one. The announced
// address may be an external LoadBalancer, which the operator has no business
// going through — it may not be reachable from inside the cluster, and the
// coordinators' shared one lands on whichever member the balancer picks, when
// the operator needs to speak to a particular one.
type CoordinatorEndpoint struct {
	// Name is the coordinator's SHOW INSTANCES name.
	Name string
	// Address is its pod's bolt "host:port".
	Address string
}

// CoordinatorEndpoints is every coordinator pod the operator currently runs,
// in ordinal order: the declared ones and, while a lowered count is being
// carried out, the retiring ones too, since a retiring coordinator can hold
// Raft leadership until it yields it. `running` is the replica count the
// operator's own apply left on the coordinator StatefulSet.
func CoordinatorEndpoints(cluster *memgraphcomv1alpha1.MemgraphCluster, running int32) []CoordinatorEndpoint {
	spec := normalize(cluster.Spec)
	endpoints := make([]CoordinatorEndpoint, 0, running)
	for ordinal := range running {
		endpoints = append(endpoints, CoordinatorEndpoint{
			Name:    CoordinatorInstanceName(ordinal),
			Address: hostPort(podFQDN(cluster, CoordinatorName(cluster), spec, ordinal), memgraphcomv1alpha1.BoltPort),
		})
	}
	return endpoints
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
		retiring = append(retiring, coordinator(cluster, spec, ordinal, ""))
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
		retiring = append(retiring, dataInstance(cluster, spec, ordinal, ""))
	}
	return retiring
}

// coordinator describes the coordinator running on the given pod ordinal, as the
// pod itself advertises it, announced at the given external bolt address when
// there is one. Retiring coordinators are described the same way as declared
// ones: they are members of the Raft cluster under the ID and addresses the
// operator added them with, whether or not the spec still declares them — and
// with no external address, because the planner never follows the announced
// address of a member on its way out.
func coordinator(
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	spec normalizedSpec,
	ordinal int32,
	externalBolt string,
) memgraph.CoordinatorSpec {
	fqdn := podFQDN(cluster, CoordinatorName(cluster), spec, ordinal)
	return memgraph.CoordinatorSpec{
		ID:                CoordinatorID(ordinal),
		BoltServer:        announcedBolt(fqdn, externalBolt),
		CoordinatorServer: hostPort(fqdn, memgraphcomv1alpha1.CoordinatorPort),
		ManagementServer:  hostPort(fqdn, memgraphcomv1alpha1.ManagementPort),
	}
}

// dataInstance describes the data instance running on the given pod ordinal, as
// the pod itself advertises it, announced at the given external bolt address
// when there is one. Retiring instances are described the same way as declared
// ones, for the reason coordinators are.
func dataInstance(
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	spec normalizedSpec,
	ordinal int32,
	externalBolt string,
) memgraph.DataInstanceSpec {
	fqdn := podFQDN(cluster, DataName(cluster), spec, ordinal)
	return memgraph.DataInstanceSpec{
		Name:              DataInstanceName(ordinal),
		BoltServer:        announcedBolt(fqdn, externalBolt),
		ManagementServer:  hostPort(fqdn, memgraphcomv1alpha1.ManagementPort),
		ReplicationServer: hostPort(fqdn, memgraphcomv1alpha1.ReplicationPort),
	}
}

// announcedBolt is the bolt address a member is announced at: the external one
// when it is known, the pod's own otherwise.
func announcedBolt(fqdn, externalBolt string) string {
	if externalBolt != "" {
		return externalBolt
	}
	return hostPort(fqdn, memgraphcomv1alpha1.BoltPort)
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
