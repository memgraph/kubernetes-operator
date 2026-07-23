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

	"github.com/memgraph/kubernetes-operator/internal/resources"
)

func TestCoordinatorHeadlessService(t *testing.T) {
	want := &corev1.Service{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
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
				{Name: boltPortName, Port: 7687},
				{Name: managementPortName, Port: 10000},
				{Name: coordinatorComponent, Port: 12000},
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
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
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
				{Name: boltPortName, Port: 7687},
				{Name: managementPortName, Port: 10000},
				{Name: replicationPortName, Port: 20000},
			},
		},
	}

	got := resources.DataHeadlessService(minimalCluster())
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("DataHeadlessService() mismatch (-want +got):\n%s", diff)
	}
}
