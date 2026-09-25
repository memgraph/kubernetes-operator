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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
)

// gatewayComponent labels the Gateway itself, which belongs to neither role.
const gatewayComponent = "gateway"

// The Gateway's listener names, which its TCPRoutes attach to by section name.
// TCPRoute has no host matching, so every data instance needs a listener of
// its own on its own port; the coordinators share one, because any coordinator
// answers a routing request.
const (
	coordinatorsListener = "coordinators-bolt"
	dataListenerFormat   = "data-%d-bolt"
)

// GatewayName is the name of the one Gateway the operator creates for an
// exposed cluster.
func GatewayName(cluster *memgraphcomv1alpha1.MemgraphCluster) string {
	return cluster.Name + "-" + gatewayComponent
}

// CoordinatorTCPRouteName is the name of the TCPRoute feeding the coordinators'
// shared listener.
func CoordinatorTCPRouteName(cluster *memgraphcomv1alpha1.MemgraphCluster) string {
	return CoordinatorName(cluster) + "-bolt"
}

// DataTCPRouteName is the name of the TCPRoute feeding the listener of the data
// instance on the given pod ordinal.
func DataTCPRouteName(cluster *memgraphcomv1alpha1.MemgraphCluster, ordinal int32) string {
	return fmt.Sprintf("%s-%d-bolt", DataName(cluster), ordinal)
}

// UsesGateway reports whether the cluster is exposed through a Gateway, which
// is when the Gateway API objects below exist at all.
func UsesGateway(cluster *memgraphcomv1alpha1.MemgraphCluster) bool {
	return cluster.Spec.ExternalAccess != nil &&
		normalize(cluster.Spec).external.typ == memgraphcomv1alpha1.ExternalAccessGateway
}

// DataGatewayPort is the Gateway listener port of the data instance on the
// given pod ordinal: the configured base plus the ordinal.
func DataGatewayPort(cluster *memgraphcomv1alpha1.MemgraphCluster, ordinal int32) int32 {
	return normalize(cluster.Spec).external.gateway.dataPortBase + ordinal
}

// Gateway builds the cluster's Gateway with one TCP listener per way in: the
// coordinators' shared listener on the bolt port, and one per data pod the
// operator currently runs on its own port. Like the external Services, the
// listeners follow the applied data count rather than the declared one, so a
// retiring instance stays reachable until its pod is shed. The Gateway only
// admits TCPRoutes, from its own namespace, which is the Gateway API default.
func Gateway(cluster *memgraphcomv1alpha1.MemgraphCluster, dataReplicas int32) *gatewayv1.Gateway {
	spec := normalize(cluster.Spec)

	listener := func(name string, port int32) gatewayv1.Listener {
		return gatewayv1.Listener{
			Name:     gatewayv1.SectionName(name),
			Port:     port,
			Protocol: gatewayv1.TCPProtocolType,
			AllowedRoutes: &gatewayv1.AllowedRoutes{
				Kinds: []gatewayv1.RouteGroupKind{{
					Group: ptr.To(gatewayv1.Group(gatewayv1.GroupName)),
					Kind:  "TCPRoute",
				}},
			},
		}
	}
	listeners := make([]gatewayv1.Listener, 0, 1+dataReplicas)
	listeners = append(listeners, listener(coordinatorsListener, memgraphcomv1alpha1.BoltPort))
	for ordinal := range dataReplicas {
		listeners = append(listeners,
			listener(fmt.Sprintf(dataListenerFormat, ordinal), spec.external.gateway.dataPortBase+ordinal))
	}

	return &gatewayv1.Gateway{
		TypeMeta: metav1.TypeMeta{APIVersion: gatewayv1.GroupVersion.String(), Kind: "Gateway"},
		ObjectMeta: metav1.ObjectMeta{
			Name:        GatewayName(cluster),
			Namespace:   cluster.Namespace,
			Labels:      externalLabels(cluster, gatewayComponent, spec.external.gateway.labels),
			Annotations: emptyToNil(spec.external.gateway.annotations),
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: gatewayv1.ObjectName(spec.external.gateway.className),
			Listeners:        listeners,
		},
	}
}

// TCPRoutes builds every TCPRoute of an exposed cluster: the coordinators' and
// one per data pod the operator currently runs, each attached to its listener
// by section name and pointing at the role's or instance's ClusterIP Service.
func TCPRoutes(cluster *memgraphcomv1alpha1.MemgraphCluster, dataReplicas int32) []*gatewayv1.TCPRoute {
	routes := make([]*gatewayv1.TCPRoute, 0, 1+dataReplicas)
	routes = append(routes, CoordinatorTCPRoute(cluster))
	for ordinal := range dataReplicas {
		routes = append(routes, DataTCPRoute(cluster, ordinal))
	}
	return routes
}

// CoordinatorTCPRoute feeds the coordinators' shared listener from their shared
// ClusterIP Service, so the Gateway spreads new connections across every
// coordinator and a client keeps one entrypoint whichever of them is up.
func CoordinatorTCPRoute(cluster *memgraphcomv1alpha1.MemgraphCluster) *gatewayv1.TCPRoute {
	spec := normalize(cluster.Spec)
	return tcpRoute(cluster, coordinatorComponent, CoordinatorTCPRouteName(cluster), coordinatorsListener,
		CoordinatorExternalServiceName(cluster), spec.external.coordinators, spec.external.coordinators.annotations)
}

// DataTCPRoute feeds one data instance's listener from that instance's
// ClusterIP Service. The role's annotations land here with the ordinal
// substituted, because in Gateway mode the route is what external-dns reads a
// hostname from.
func DataTCPRoute(cluster *memgraphcomv1alpha1.MemgraphCluster, ordinal int32) *gatewayv1.TCPRoute {
	spec := normalize(cluster.Spec)
	return tcpRoute(cluster, dataComponent, DataTCPRouteName(cluster, ordinal),
		fmt.Sprintf(dataListenerFormat, ordinal), DataExternalServiceName(cluster, ordinal),
		spec.external.data, perInstanceAnnotations(spec.external.data.annotations, ordinal))
}

func tcpRoute(
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	component, name, listener, backend string,
	external normalizedExternalRole,
	annotations map[string]string,
) *gatewayv1.TCPRoute {
	return &gatewayv1.TCPRoute{
		TypeMeta: metav1.TypeMeta{APIVersion: gatewayv1.GroupVersion.String(), Kind: "TCPRoute"},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   cluster.Namespace,
			Labels:      externalLabels(cluster, component, external.labels),
			Annotations: annotations,
		},
		Spec: gatewayv1.TCPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name:        gatewayv1.ObjectName(GatewayName(cluster)),
					SectionName: ptr.To(gatewayv1.SectionName(listener)),
				}},
			},
			Rules: []gatewayv1.TCPRouteRule{{
				BackendRefs: []gatewayv1.BackendRef{{
					BackendObjectReference: gatewayv1.BackendObjectReference{
						Name: gatewayv1.ObjectName(backend),
						Port: ptr.To(memgraphcomv1alpha1.BoltPort),
					},
				}},
			}},
		},
	}
}

// GatewayBoltAddress derives the "host:port" clients outside the cluster reach
// one listener of the Gateway at, or the empty string while nothing does yet.
// The host is taken in the order the LoadBalancer mode uses, adapted to where
// the information lives here: the external-dns hostname on the member's own
// TCPRoute, which is what external-dns's TCPRoute source reads; then the one
// on the Gateway, for a user publishing a single name for the whole cluster;
// then the hostname the Gateway reports in its status; then its IP.
func GatewayBoltAddress(gateway *gatewayv1.Gateway, route *gatewayv1.TCPRoute, port int32) string {
	host := ""
	if route != nil {
		host = externalDNSHost(route.Annotations)
	}
	if host == "" && gateway != nil {
		host = externalDNSHost(gateway.Annotations)
	}
	if host == "" && gateway != nil {
		host = gatewayHost(gateway.Status.Addresses)
	}
	if host == "" {
		return ""
	}
	return hostPort(host, port)
}

// gatewayHost is the host a Gateway reports, a hostname preferred over an IP
// whichever entry carries it, or the empty string while it reports none. An
// address with no type is an IP, as the Gateway API defines it.
func gatewayHost(addresses []gatewayv1.GatewayStatusAddress) string {
	for _, address := range addresses {
		if address.Type != nil && *address.Type == gatewayv1.HostnameAddressType && address.Value != "" {
			return address.Value
		}
	}
	for _, address := range addresses {
		if (address.Type == nil || *address.Type == gatewayv1.IPAddressType) && address.Value != "" {
			return address.Value
		}
	}
	return ""
}

// emptyToNil turns an annotation map with no entries into nil, so an object
// that was asked for no annotations claims none in its apply.
func emptyToNil(annotations map[string]string) map[string]string {
	if len(annotations) == 0 {
		return nil
	}
	return annotations
}
