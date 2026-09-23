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

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
	"github.com/memgraph/kubernetes-operator/internal/resources"
)

// The external objects' names and the marker label every one of them carries.
const (
	coordinatorExternalName = coordinatorName + "-external"
	externalAccessLabel     = "memgraph.com/external-access"
	podNameLabel            = "statefulset.kubernetes.io/pod-name"

	externalDNSAnnotation = "external-dns.alpha.kubernetes.io/hostname"
	lbTypeAnnotation      = "service.beta.kubernetes.io/aws-load-balancer-type"
)

// exposedCluster is the minimal cluster exposed through LoadBalancers, with
// every decoration of the block set so the golden tests can pin where each one
// lands: the coordinators' hostname has no ordinal, the data hostname carries
// the placeholder, and each role has one label of its own.
func exposedCluster() *memgraphcomv1alpha1.MemgraphCluster {
	cluster := minimalCluster()
	cluster.Spec.ExternalAccess = &memgraphcomv1alpha1.ExternalAccessSpec{
		Type: memgraphcomv1alpha1.ExternalAccessLoadBalancer,
		Coordinators: memgraphcomv1alpha1.ExternalAccessRoleSpec{
			Labels:      map[string]string{exposeLabel: "coordinators"},
			Annotations: map[string]string{externalDNSAnnotation: "memgraph.example.com"},
		},
		Data: memgraphcomv1alpha1.ExternalAccessRoleSpec{
			Labels: map[string]string{exposeLabel: dataComponent},
			Annotations: map[string]string{
				externalDNSAnnotation: "data-{ordinal}.memgraph.example.com",
				lbTypeAnnotation:      "nlb",
			},
		},
	}
	return cluster
}

func externalLabels(component string, custom map[string]string) map[string]string {
	l := expectedLabelsWith(component, custom)
	l[externalAccessLabel] = "true"
	return l
}

func TestExternalServicesAbsentUnlessExposed(t *testing.T) {
	if got := resources.ExternalServices(minimalCluster(), 2); got != nil {
		t.Errorf("ExternalServices() on an unexposed cluster = %d Services, want none", len(got))
	}
}

// TestExternalServicesFollowRunningDataPods pins that the data Services follow
// the pods the operator runs rather than the declared count: a retiring instance
// keeps its Service until its pod is shed.
func TestExternalServicesFollowRunningDataPods(t *testing.T) {
	got := resources.ExternalServices(exposedCluster(), 3)

	names := make([]string, 0, len(got))
	for _, service := range got {
		names = append(names, service.Name)
	}
	want := []string{
		coordinatorExternalName,
		dataName + "-0-external",
		dataName + "-1-external",
		dataName + "-2-external",
	}
	if diff := cmp.Diff(want, names); diff != "" {
		t.Errorf("ExternalServices() names mismatch (-want +got):\n%s", diff)
	}
}

func TestCoordinatorExternalService(t *testing.T) {
	want := &corev1.Service{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: serviceKind},
		ObjectMeta: metav1.ObjectMeta{
			Name:        coordinatorExternalName,
			Namespace:   testNamespace,
			Labels:      externalLabels(coordinatorComponent, map[string]string{exposeLabel: "coordinators"}),
			Annotations: map[string]string{externalDNSAnnotation: "memgraph.example.com"},
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeLoadBalancer,
			Selector: expectedSelectorLabels(coordinatorComponent),
			Ports:    []corev1.ServicePort{{Name: boltPortName, Port: memgraphcomv1alpha1.BoltPort}},
		},
	}

	got := resources.CoordinatorExternalService(exposedCluster())
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("CoordinatorExternalService() mismatch (-want +got):\n%s", diff)
	}
}

// TestDataExternalService pins the per-instance shape: the Service selects one
// pod by its StatefulSet pod-name label, and every annotation value has the
// ordinal substituted — the hostname, and anything else carrying the placeholder.
func TestDataExternalService(t *testing.T) {
	selector := expectedSelectorLabels(dataComponent)
	selector[podNameLabel] = dataName + "-1"
	want := &corev1.Service{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: serviceKind},
		ObjectMeta: metav1.ObjectMeta{
			Name:      dataName + "-1-external",
			Namespace: testNamespace,
			Labels:    externalLabels(dataComponent, map[string]string{exposeLabel: dataComponent}),
			Annotations: map[string]string{
				externalDNSAnnotation: "data-1.memgraph.example.com",
				lbTypeAnnotation:      "nlb",
			},
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeLoadBalancer,
			Selector: selector,
			Ports:    []corev1.ServicePort{{Name: boltPortName, Port: memgraphcomv1alpha1.BoltPort}},
		},
	}

	got := resources.DataExternalService(exposedCluster(), 1)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("DataExternalService() mismatch (-want +got):\n%s", diff)
	}
}

// TestExternalServiceLabelsMergeBothBlocks pins the label precedence on an
// external Service: the role's serviceLabels land on it as on the headless
// Service, the external block's labels beside them, and the operator's identity
// labels win any collision — so a custom label can never detach the Service
// from its cluster or pretend it is not external.
func TestExternalServiceLabelsMergeBothBlocks(t *testing.T) {
	cluster := exposedCluster()
	cluster.Spec.Labels.Data.ServiceLabels = map[string]string{
		teamLabel:           platformTeam,
		componentLabel:      "impostor",
		externalAccessLabel: "false",
	}
	cluster.Spec.ExternalAccess.Data.Annotations = nil

	got := resources.DataExternalService(cluster, 0)
	want := externalLabels(dataComponent, map[string]string{teamLabel: platformTeam, exposeLabel: "data"})
	if diff := cmp.Diff(want, got.Labels); diff != "" {
		t.Errorf("DataExternalService() labels mismatch (-want +got):\n%s", diff)
	}
	if got.Annotations != nil {
		t.Errorf("DataExternalService() with no annotations asked for claims %v", got.Annotations)
	}
}

// TestExternalBoltAddress pins the order the announced host is taken in: the
// external-dns hostname the user wrote, then the hostname the LoadBalancer
// reports, then its IP — and nothing while none of them exists.
func TestExternalBoltAddress(t *testing.T) {
	const (
		hostname    = "a1b2c3.elb.example.com"
		ip          = "203.0.113.10"
		dnsHostname = "memgraph.example.com"
	)
	withIngress := func(ingress ...corev1.LoadBalancerIngress) corev1.ServiceStatus {
		return corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: ingress}}
	}

	cases := []struct {
		name    string
		service corev1.Service
		want    string
	}{
		{
			name: "no address until the LoadBalancer reports one",
		},
		{
			name:    "the reported IP",
			service: corev1.Service{Status: withIngress(corev1.LoadBalancerIngress{IP: ip})},
			want:    endpoint(ip, memgraphcomv1alpha1.BoltPort),
		},
		{
			name:    "the reported hostname",
			service: corev1.Service{Status: withIngress(corev1.LoadBalancerIngress{Hostname: hostname})},
			want:    endpoint(hostname, memgraphcomv1alpha1.BoltPort),
		},
		{
			name: "a hostname beats an IP, whichever entry carries it",
			service: corev1.Service{Status: withIngress(
				corev1.LoadBalancerIngress{IP: ip},
				corev1.LoadBalancerIngress{Hostname: hostname},
			)},
			want: endpoint(hostname, memgraphcomv1alpha1.BoltPort),
		},
		{
			name: "the external-dns hostname beats whatever the LoadBalancer reports",
			service: corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{externalDNSAnnotation: dnsHostname}},
				Status:     withIngress(corev1.LoadBalancerIngress{IP: ip, Hostname: hostname}),
			},
			want: endpoint(dnsHostname, memgraphcomv1alpha1.BoltPort),
		},
		{
			name: "the external-dns hostname counts even before the LoadBalancer has an address",
			service: corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{externalDNSAnnotation: dnsHostname}},
			},
			want: endpoint(dnsHostname, memgraphcomv1alpha1.BoltPort),
		},
		{
			name: "the first of several external-dns hostnames is announced",
			service: corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					externalDNSAnnotation: " " + dnsHostname + ", alias.example.com",
				}},
			},
			want: endpoint(dnsHostname, memgraphcomv1alpha1.BoltPort),
		},
		{
			name: "an empty external-dns annotation is no hostname",
			service: corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{externalDNSAnnotation: ""}},
				Status:     withIngress(corev1.LoadBalancerIngress{IP: ip}),
			},
			want: endpoint(ip, memgraphcomv1alpha1.BoltPort),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resources.ExternalBoltAddress(&tc.service); got != tc.want {
				t.Errorf("ExternalBoltAddress() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestExternalServicesSelector pins that the selector finds every external
// Service of the cluster and none of its headless ones, which is what makes the
// controller's pruning safe.
func TestExternalServicesSelector(t *testing.T) {
	cluster := exposedCluster()
	selector := resources.ExternalServicesSelector(cluster)

	matches := func(l map[string]string) bool {
		for key, value := range selector {
			if l[key] != value {
				return false
			}
		}
		return true
	}
	for _, service := range resources.ExternalServices(cluster, 2) {
		if !matches(service.Labels) {
			t.Errorf("external Service %s does not match ExternalServicesSelector()", service.Name)
		}
	}
	for _, service := range []*corev1.Service{
		resources.CoordinatorHeadlessService(cluster), resources.DataHeadlessService(cluster),
	} {
		if matches(service.Labels) {
			t.Errorf("headless Service %s matches ExternalServicesSelector()", service.Name)
		}
	}
}
