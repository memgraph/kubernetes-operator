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
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
	"github.com/memgraph/kubernetes-operator/internal/resources"
)

const (
	gatewayName      = clusterName + "-gateway"
	gatewayClassName = "eg"
	gatewayAPIGroup  = "gateway.networking.k8s.io"
	gatewayVersion   = gatewayAPIGroup + "/v1"
	tcpRouteKind     = "TCPRoute"
	tcpProtocol      = "TCP"
)

// gatewayCluster is exposedCluster switched to a Gateway, with the Gateway
// block decorated and a non-default port base so the golden tests pin every
// knob.
func gatewayCluster() *memgraphcomv1alpha1.MemgraphCluster {
	cluster := exposedCluster()
	cluster.Spec.ExternalAccess.Type = memgraphcomv1alpha1.ExternalAccessGateway
	cluster.Spec.ExternalAccess.Gateway = memgraphcomv1alpha1.ExternalAccessGatewaySpec{
		GatewayClassName: gatewayClassName,
		DataPortBase:     ptr.To(int32(9100)),
		Labels:           map[string]string{tierLabel: "edge"},
		Annotations:      map[string]string{externalDNSAnnotation: coordinatorsHostname},
	}
	return cluster
}

func tcpRouteKinds() *gatewayv1.AllowedRoutes {
	return &gatewayv1.AllowedRoutes{
		Kinds: []gatewayv1.RouteGroupKind{{Group: ptr.To(gatewayv1.Group(gatewayAPIGroup)), Kind: tcpRouteKind}},
	}
}

// TestGateway pins the Gateway's shape: one shared coordinators listener on the
// bolt port and one listener per running data pod on base + ordinal, every one
// of them TCP and admitting TCPRoutes only.
func TestGateway(t *testing.T) {
	want := &gatewayv1.Gateway{
		TypeMeta: metav1.TypeMeta{APIVersion: gatewayVersion, Kind: "Gateway"},
		ObjectMeta: metav1.ObjectMeta{
			Name:        gatewayName,
			Namespace:   testNamespace,
			Labels:      externalLabels("gateway", map[string]string{tierLabel: "edge"}),
			Annotations: map[string]string{externalDNSAnnotation: coordinatorsHostname},
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: gatewayClassName,
			Listeners: []gatewayv1.Listener{
				{Name: "coordinators-bolt", Port: memgraphcomv1alpha1.BoltPort, Protocol: tcpProtocol, AllowedRoutes: tcpRouteKinds()},
				{Name: "data-0-bolt", Port: 9100, Protocol: tcpProtocol, AllowedRoutes: tcpRouteKinds()},
				{Name: "data-1-bolt", Port: 9101, Protocol: tcpProtocol, AllowedRoutes: tcpRouteKinds()},
				{Name: "data-2-bolt", Port: 9102, Protocol: tcpProtocol, AllowedRoutes: tcpRouteKinds()},
			},
		},
	}

	// Three running data pods against two declared: the retiring one keeps its
	// listener until its pod is shed.
	got := resources.Gateway(gatewayCluster(), 3)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Gateway() mismatch (-want +got):\n%s", diff)
	}
}

// TestGatewayDefaultsPortBase pins the default the CRD declares, because the
// builder has to resolve it on a spec that never passed admission.
func TestGatewayDefaultsPortBase(t *testing.T) {
	cluster := gatewayCluster()
	cluster.Spec.ExternalAccess.Gateway.DataPortBase = nil

	if got := resources.DataGatewayPort(cluster, 1); got != memgraphcomv1alpha1.DefaultGatewayDataPortBase+1 {
		t.Errorf("DataGatewayPort(1) = %d, want %d", got, memgraphcomv1alpha1.DefaultGatewayDataPortBase+1)
	}
	if got := resources.Gateway(cluster, 1).Spec.Listeners[1].Port; got != memgraphcomv1alpha1.DefaultGatewayDataPortBase {
		t.Errorf("first data listener port = %d, want the default %d", got, memgraphcomv1alpha1.DefaultGatewayDataPortBase)
	}
}

func parentRef(listener string) []gatewayv1.ParentReference {
	return []gatewayv1.ParentReference{{
		Name: gatewayName, SectionName: ptr.To(gatewayv1.SectionName(listener)),
	}}
}

func backend(service string) []gatewayv1.TCPRouteRule {
	return []gatewayv1.TCPRouteRule{{
		BackendRefs: []gatewayv1.BackendRef{{
			BackendObjectReference: gatewayv1.BackendObjectReference{
				Name: gatewayv1.ObjectName(service), Port: ptr.To(memgraphcomv1alpha1.BoltPort),
			},
		}},
	}}
}

// TestTCPRoutes pins each route's attachment and backend: the coordinators'
// route feeds their shared listener from their shared Service, each data
// route feeds its own listener from its own Service, and the role annotations
// land on the routes with the ordinal substituted.
func TestTCPRoutes(t *testing.T) {
	want := []*gatewayv1.TCPRoute{
		{
			TypeMeta: metav1.TypeMeta{APIVersion: gatewayVersion, Kind: tcpRouteKind},
			ObjectMeta: metav1.ObjectMeta{
				Name:        coordinatorName + "-bolt",
				Namespace:   testNamespace,
				Labels:      externalLabels(coordinatorComponent, map[string]string{exposeLabel: coordinatorsExpose}),
				Annotations: map[string]string{externalDNSAnnotation: coordinatorsHostname},
			},
			Spec: gatewayv1.TCPRouteSpec{
				CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: parentRef("coordinators-bolt")},
				Rules:           backend(coordinatorExternalName),
			},
		},
		{
			TypeMeta: metav1.TypeMeta{APIVersion: gatewayVersion, Kind: tcpRouteKind},
			ObjectMeta: metav1.ObjectMeta{
				Name:      dataName + "-0-bolt",
				Namespace: testNamespace,
				Labels:    externalLabels(dataComponent, map[string]string{exposeLabel: dataComponent}),
				Annotations: map[string]string{
					externalDNSAnnotation: "data-0.memgraph.example.com",
					lbTypeAnnotation:      lbType,
				},
			},
			Spec: gatewayv1.TCPRouteSpec{
				CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: parentRef("data-0-bolt")},
				Rules:           backend(dataName + "-0-external"),
			},
		},
	}

	got := resources.TCPRoutes(gatewayCluster(), 1)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("TCPRoutes() mismatch (-want +got):\n%s", diff)
	}
}

// TestGatewayModeServicesAreClusterIPs pins the one thing that differs about
// the Services behind a Gateway: they are ClusterIPs, and they carry none of the
// role's annotations, which belong on the routes external-dns reads.
func TestGatewayModeServicesAreClusterIPs(t *testing.T) {
	for _, service := range resources.ExternalServices(gatewayCluster(), 2) {
		if service.Spec.Type != corev1.ServiceTypeClusterIP {
			t.Errorf("Service %s has type %s, want ClusterIP behind a Gateway", service.Name, service.Spec.Type)
		}
		if service.Annotations != nil {
			t.Errorf("Service %s carries annotations %v, want none behind a Gateway", service.Name, service.Annotations)
		}
		if service.Labels[exposeLabel] == "" {
			t.Errorf("Service %s lost the role's external labels", service.Name)
		}
	}
}

func TestUsesGateway(t *testing.T) {
	if resources.UsesGateway(minimalCluster()) {
		t.Error("UsesGateway() on an unexposed cluster = true")
	}
	if resources.UsesGateway(exposedCluster()) {
		t.Error("UsesGateway() on a LoadBalancer cluster = true")
	}
	if !resources.UsesGateway(gatewayCluster()) {
		t.Error("UsesGateway() on a Gateway cluster = false")
	}
}

// TestGatewayBoltAddress pins the order the announced host is taken in behind a
// Gateway: the member's own route's external-dns hostname, then the Gateway's,
// then the hostname the Gateway reports, then its IP — and nothing while none
// of them exists. The port is the listener's, whatever the host.
func TestGatewayBoltAddress(t *testing.T) {
	const (
		ip          = "203.0.113.20"
		hostname    = "gw.elb.example.com"
		dnsHostname = coordinatorsHostname
		routeName   = "data-1.memgraph.example.com"
	)
	annotated := func(hostname string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Annotations: map[string]string{externalDNSAnnotation: hostname}}
	}
	addresses := func(addresses ...gatewayv1.GatewayStatusAddress) gatewayv1.GatewayStatus {
		return gatewayv1.GatewayStatus{Addresses: addresses}
	}
	typed := func(t gatewayv1.AddressType, value string) gatewayv1.GatewayStatusAddress {
		return gatewayv1.GatewayStatusAddress{Type: ptr.To(t), Value: value}
	}

	cases := []struct {
		name    string
		gateway *gatewayv1.Gateway
		route   *gatewayv1.TCPRoute
		want    string
	}{
		{
			name:    "no address until the Gateway reports one",
			gateway: &gatewayv1.Gateway{},
			route:   &gatewayv1.TCPRoute{},
		},
		{
			name:    "no Gateway at all is no address",
			gateway: nil,
			route:   &gatewayv1.TCPRoute{ObjectMeta: metav1.ObjectMeta{}},
		},
		{
			name:    "the reported IP",
			gateway: &gatewayv1.Gateway{Status: addresses(typed(gatewayv1.IPAddressType, ip))},
			want:    endpoint(ip, 9001),
		},
		{
			name:    "an untyped address is an IP",
			gateway: &gatewayv1.Gateway{Status: addresses(gatewayv1.GatewayStatusAddress{Value: ip})},
			want:    endpoint(ip, 9001),
		},
		{
			name: "a reported hostname beats a reported IP",
			gateway: &gatewayv1.Gateway{Status: addresses(
				typed(gatewayv1.IPAddressType, ip), typed(gatewayv1.HostnameAddressType, hostname),
			)},
			want: endpoint(hostname, 9001),
		},
		{
			name: "the Gateway's external-dns hostname beats what it reports",
			gateway: &gatewayv1.Gateway{
				ObjectMeta: annotated(dnsHostname),
				Status:     addresses(typed(gatewayv1.HostnameAddressType, hostname)),
			},
			want: endpoint(dnsHostname, 9001),
		},
		{
			name: "the route's external-dns hostname beats the Gateway's",
			gateway: &gatewayv1.Gateway{
				ObjectMeta: annotated(dnsHostname),
				Status:     addresses(typed(gatewayv1.IPAddressType, ip)),
			},
			route: &gatewayv1.TCPRoute{ObjectMeta: annotated(routeName)},
			want:  endpoint(routeName, 9001),
		},
		{
			name:  "a route hostname counts even before the Gateway exists",
			route: &gatewayv1.TCPRoute{ObjectMeta: annotated(routeName)},
			want:  endpoint(routeName, 9001),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resources.GatewayBoltAddress(tc.gateway, tc.route, 9001); got != tc.want {
				t.Errorf("GatewayBoltAddress() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestGatewayObjectsMatchExternalSelector pins that the controller's pruning
// finds the Gateway and every route.
func TestGatewayObjectsMatchExternalSelector(t *testing.T) {
	cluster := gatewayCluster()
	selector := resources.ExternalServicesSelector(cluster)
	matches := func(l map[string]string) bool {
		for key, value := range selector {
			if l[key] != value {
				return false
			}
		}
		return true
	}
	if !matches(resources.Gateway(cluster, 2).Labels) {
		t.Error("the Gateway does not match ExternalServicesSelector()")
	}
	for _, route := range resources.TCPRoutes(cluster, 2) {
		if !matches(route.Labels) {
			t.Errorf("TCPRoute %s does not match ExternalServicesSelector()", route.Name)
		}
	}
}
