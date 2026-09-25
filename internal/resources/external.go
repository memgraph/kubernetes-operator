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
	"maps"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
)

// ExternalAccessLabel marks every external object the operator builds for a
// cluster, with ExternalAccessValue as its value. The controller lists by it to
// find the external objects a spec no longer describes — because the block was
// removed, or because a lowered count retired the instance behind one — and
// deletes them: server-side apply creates and updates, it never removes.
const (
	ExternalAccessLabel = "memgraph.com/external-access"
	ExternalAccessValue = "true"
)

// externalSuffix distinguishes a role's external Service from its headless one.
const externalSuffix = "-external"

// ExternalServicesSelector matches every external Service of this cluster and
// nothing else: the Services the operator has to consider deleting when the
// spec stops describing them.
func ExternalServicesSelector(cluster *memgraphcomv1alpha1.MemgraphCluster) map[string]string {
	return map[string]string{
		instanceLabel:       cluster.Name,
		ExternalAccessLabel: ExternalAccessValue,
	}
}

// CoordinatorExternalServiceName is the name of the one LoadBalancer Service
// shared by all coordinators.
func CoordinatorExternalServiceName(cluster *memgraphcomv1alpha1.MemgraphCluster) string {
	return CoordinatorName(cluster) + externalSuffix
}

// DataExternalServiceName is the name of the LoadBalancer Service in front of
// the data instance on the given pod ordinal.
func DataExternalServiceName(cluster *memgraphcomv1alpha1.MemgraphCluster, ordinal int32) string {
	return fmt.Sprintf("%s-%d%s", DataName(cluster), ordinal, externalSuffix)
}

// ExternalServices builds every external Service the spec asks for: none when
// the cluster is not exposed, otherwise the coordinators' shared Service and one
// per data pod the operator currently runs. The count is the one the data
// StatefulSet is applied at rather than the declared one, so a retiring data
// instance keeps its Service for as long as it keeps its pod — it may still be
// serving clients as MAIN while its handover waits — and loses it in the pass
// that sheds the pod, when the applied count drops to the declared one.
//
// The Services are the same in both exposure modes and differ only in type:
// LoadBalancers when they are the way in themselves, ClusterIPs when a Gateway
// is, with a TCPRoute per Service pointing at it.
func ExternalServices(cluster *memgraphcomv1alpha1.MemgraphCluster, dataReplicas int32) []*corev1.Service {
	if cluster.Spec.ExternalAccess == nil {
		return nil
	}
	services := make([]*corev1.Service, 0, 1+dataReplicas)
	services = append(services, CoordinatorExternalService(cluster))
	for ordinal := range dataReplicas {
		services = append(services, DataExternalService(cluster, ordinal))
	}
	return services
}

// CoordinatorExternalService builds the one Service every coordinator sits
// behind. Any coordinator answers a routing request — a follower forwards it to
// the leader — so clients need one address for the role, not one per member,
// and every coordinator is announced at this one.
func CoordinatorExternalService(cluster *memgraphcomv1alpha1.MemgraphCluster) *corev1.Service {
	spec := normalize(cluster.Spec)
	return externalService(cluster, coordinatorComponent, CoordinatorExternalServiceName(cluster),
		selectorLabels(cluster, coordinatorComponent), spec, spec.coordinatorRole, spec.external.coordinators,
		serviceAnnotations(spec, spec.external.coordinators.annotations))
}

// DataExternalService builds the Service in front of one data instance. Every
// data instance gets its own: the routing table names each one individually,
// and a client writes to the MAIN and reads from a replica it is told about by
// name, so one address per instance is the only shape that works. The Service
// selects the pod by the name label the StatefulSet controller stamps on it,
// which is the one label that singles out an ordinal.
func DataExternalService(cluster *memgraphcomv1alpha1.MemgraphCluster, ordinal int32) *corev1.Service {
	spec := normalize(cluster.Spec)
	selector := selectorLabels(cluster, dataComponent)
	selector[appsv1.StatefulSetPodNameLabel] = fmt.Sprintf("%s-%d", DataName(cluster), ordinal)
	return externalService(cluster, dataComponent, DataExternalServiceName(cluster, ordinal),
		selector, spec, spec.dataRole, spec.external.data,
		serviceAnnotations(spec, perInstanceAnnotations(spec.external.data.annotations, ordinal)))
}

// serviceAnnotations decides whether the role's annotations belong on its
// Service: they do when the Service is the way in, and not when a Gateway is,
// because then the controllers that read them — external-dns above all — read
// the TCPRoute, and a hostname annotation on a ClusterIP Service nobody
// publishes would be a lie.
func serviceAnnotations(spec normalizedSpec, annotations map[string]string) map[string]string {
	if spec.external.typ == memgraphcomv1alpha1.ExternalAccessGateway {
		return nil
	}
	return annotations
}

// externalService is the shape both roles' external Services share: the bolt
// port and nothing else — the management, replication and coordinator ports
// are the cluster's own business and never leave its network. The role's
// serviceLabels land on it as on the headless Service, with the external
// block's labels beside them and the operator's identity labels winning any
// collision.
func externalService(
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	component, name string,
	selector map[string]string,
	spec normalizedSpec,
	role normalizedRole,
	external normalizedExternalRole,
	annotations map[string]string,
) *corev1.Service {
	custom := make(map[string]string, len(role.serviceLabels)+len(external.labels))
	maps.Copy(custom, role.serviceLabels)
	maps.Copy(custom, external.labels)

	serviceType := corev1.ServiceTypeLoadBalancer
	if spec.external.typ == memgraphcomv1alpha1.ExternalAccessGateway {
		serviceType = corev1.ServiceTypeClusterIP
	}
	return &corev1.Service{
		// TypeMeta is set explicitly because the controller server-side
		// applies builder output, and apply patches must carry the GVK.
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   cluster.Namespace,
			Labels:      externalLabels(cluster, component, custom),
			Annotations: annotations,
		},
		Spec: corev1.ServiceSpec{
			Type:     serviceType,
			Selector: selector,
			Ports:    []corev1.ServicePort{{Name: boltPortName, Port: memgraphcomv1alpha1.BoltPort}},
		},
	}
}

// externalLabels is the label set of an external object: the role's labels with
// the custom ones merged underneath, plus the marker the controller prunes by.
func externalLabels(
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	component string,
	custom map[string]string,
) map[string]string {
	l := labels(cluster, component, custom)
	l[ExternalAccessLabel] = ExternalAccessValue
	return l
}

// perInstanceAnnotations copies the role's annotations for one instance's
// object, replacing the ordinal placeholder in every value. It is what makes
// one annotation map serve every data instance with a hostname of its own. The
// coordinators' shared objects get the role's annotations as written: admission
// keeps the placeholder off their hostname annotation, and any other annotation
// carrying it there is passed through untouched. A map with no entries comes
// back nil so an object that was asked for no annotations claims none in its
// apply.
func perInstanceAnnotations(annotations map[string]string, ordinal int32) map[string]string {
	if len(annotations) == 0 {
		return nil
	}
	out := make(map[string]string, len(annotations))
	for key, value := range annotations {
		out[key] = strings.ReplaceAll(value, memgraphcomv1alpha1.OrdinalPlaceholder, strconv.Itoa(int(ordinal)))
	}
	return out
}

// ExternalAddresses is the external "host:port" every exposed member is
// announced at: the address a client outside the cluster reaches it through,
// and therefore the bolt address it is registered with. An address that is
// not known — the LoadBalancer in front of the member has none yet, or the
// cluster is not exposed — is simply absent, and the member is announced at its
// in-cluster pod address instead. The zero value is an unexposed cluster.
type ExternalAddresses struct {
	// Coordinators is the one address every coordinator is announced at.
	Coordinators string
	// Data is each data instance's address, keyed by pod ordinal.
	Data map[int32]string
}

// ExternalBoltAddress derives the "host:port" clients outside the cluster reach
// the LoadBalancer Service's bolt port at, or the empty string while nothing
// does yet.
//
// The host is taken in this order. First, the hostname external-dns publishes,
// read off the Service's own annotation: external-dns writes the record at the
// DNS provider and never back into the Service, so the annotation is the only
// place the name the user wants clients to use can be found. Then the hostname
// the LoadBalancer reports, which is what cloud providers that front a Service
// with a DNS name fill in. Then the IP. Every candidate is read afresh on every
// pass, so an address that appears or changes later is followed rather than
// missed: nothing here is a one-time discovery.
func ExternalBoltAddress(service *corev1.Service) string {
	host := externalDNSHost(service.Annotations)
	if host == "" {
		host = loadBalancerHost(service.Status.LoadBalancer.Ingress)
	}
	if host == "" {
		return ""
	}
	return hostPort(host, memgraphcomv1alpha1.BoltPort)
}

// externalDNSHost is the hostname external-dns publishes for an object, read
// off its annotation, or the empty string when none is set. external-dns
// accepts a comma-separated list and publishes every name in it; the first is
// the one clients are announced to.
func externalDNSHost(annotations map[string]string) string {
	hostnames := annotations[memgraphcomv1alpha1.ExternalDNSHostnameAnnotation]
	hostname, _, _ := strings.Cut(hostnames, ",")
	return strings.TrimSpace(hostname)
}

// loadBalancerHost is the host a LoadBalancer Service reports, a hostname
// preferred over an IP whichever entry carries it, or the empty string while it
// reports none.
func loadBalancerHost(ingress []corev1.LoadBalancerIngress) string {
	for _, entry := range ingress {
		if entry.Hostname != "" {
			return entry.Hostname
		}
	}
	for _, entry := range ingress {
		if entry.IP != "" {
			return entry.IP
		}
	}
	return ""
}
