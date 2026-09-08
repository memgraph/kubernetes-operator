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

func TestCoordinatorHeadlessService(t *testing.T) {
	want := &corev1.Service{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: serviceKind},
		ObjectMeta: metav1.ObjectMeta{
			Name:      coordinatorName,
			Namespace: testNamespace,
			Labels:    expectedLabels(coordinatorComponent),
		},
		Spec: corev1.ServiceSpec{
			ClusterIP:                corev1.ClusterIPNone,
			Selector:                 expectedSelectorLabels(coordinatorComponent),
			PublishNotReadyAddresses: true,
			Ports: []corev1.ServicePort{
				{Name: boltPortName, Port: memgraphcomv1alpha1.BoltPort},
				{Name: managementPortName, Port: memgraphcomv1alpha1.ManagementPort},
				{Name: coordinatorComponent, Port: memgraphcomv1alpha1.CoordinatorPort},
			},
		},
	}

	got := resources.CoordinatorHeadlessService(minimalCluster())
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("CoordinatorHeadlessService() mismatch (-want +got):\n%s", diff)
	}
}

func TestDataHeadlessService(t *testing.T) {
	want := &corev1.Service{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: serviceKind},
		ObjectMeta: metav1.ObjectMeta{
			Name:      dataName,
			Namespace: testNamespace,
			Labels:    expectedLabels(dataComponent),
		},
		Spec: corev1.ServiceSpec{
			ClusterIP:                corev1.ClusterIPNone,
			Selector:                 expectedSelectorLabels(dataComponent),
			PublishNotReadyAddresses: true,
			Ports: []corev1.ServicePort{
				{Name: boltPortName, Port: memgraphcomv1alpha1.BoltPort},
				{Name: managementPortName, Port: memgraphcomv1alpha1.ManagementPort},
				{Name: replicationPortName, Port: memgraphcomv1alpha1.ReplicationPort},
			},
		},
	}

	got := resources.DataHeadlessService(minimalCluster())
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("DataHeadlessService() mismatch (-want +got):\n%s", diff)
	}
}

// TestHeadlessServicePortsAndLabels asserts the Services publish the fixed
// ports — the pods listen on nothing else — and carry the role's custom Service
// labels while keeping the operator-owned selector.
func TestHeadlessServicePortsAndLabels(t *testing.T) {
	cluster := tunedCluster()

	tests := []struct {
		name      string
		service   *corev1.Service
		component string
		labels    map[string]string
		ports     []corev1.ServicePort
	}{
		{
			name:      coordinatorComponent,
			service:   resources.CoordinatorHeadlessService(cluster),
			component: coordinatorComponent,
			labels:    map[string]string{exposeLabel: "internal"},
			ports: []corev1.ServicePort{
				{Name: boltPortName, Port: memgraphcomv1alpha1.BoltPort},
				{Name: managementPortName, Port: memgraphcomv1alpha1.ManagementPort},
				{Name: coordinatorComponent, Port: memgraphcomv1alpha1.CoordinatorPort},
			},
		},
		{
			name:      dataComponent,
			service:   resources.DataHeadlessService(cluster),
			component: dataComponent,
			labels:    map[string]string{exposeLabel: "bolt"},
			ports: []corev1.ServicePort{
				{Name: boltPortName, Port: memgraphcomv1alpha1.BoltPort},
				{Name: managementPortName, Port: memgraphcomv1alpha1.ManagementPort},
				{Name: replicationPortName, Port: memgraphcomv1alpha1.ReplicationPort},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.ports, tc.service.Spec.Ports); diff != "" {
				t.Errorf("Service ports mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(expectedLabelsWith(tc.component, tc.labels), tc.service.Labels); diff != "" {
				t.Errorf("Service labels mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(expectedSelectorLabels(tc.component), tc.service.Spec.Selector); diff != "" {
				t.Errorf("Service selector mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
