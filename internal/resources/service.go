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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
)

// CoordinatorHeadlessService builds the headless Service backing the
// coordinator StatefulSet's per-pod DNS identities.
func CoordinatorHeadlessService(cluster *memgraphcomv1alpha1.MemgraphCluster) *corev1.Service {
	return headlessService(cluster, coordinatorComponent, CoordinatorName(cluster), []corev1.ServicePort{
		{Name: boltPortName, Port: BoltPort},
		{Name: managementPortName, Port: ManagementPort},
		{Name: coordinatorPortName, Port: CoordinatorPort},
	})
}

// DataHeadlessService builds the headless Service backing the data-instance
// StatefulSet's per-pod DNS identities.
func DataHeadlessService(cluster *memgraphcomv1alpha1.MemgraphCluster) *corev1.Service {
	return headlessService(cluster, dataComponent, DataName(cluster), []corev1.ServicePort{
		{Name: boltPortName, Port: BoltPort},
		{Name: managementPortName, Port: ManagementPort},
		{Name: replicationPortName, Port: ReplicationPort},
	})
}

func headlessService(
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	component, name string,
	ports []corev1.ServicePort,
) *corev1.Service {
	return &corev1.Service{
		// TypeMeta is set explicitly because the controller server-side
		// applies builder output, and apply patches must carry the GVK.
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cluster.Namespace,
			Labels:    labels(cluster, component),
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: corev1.ClusterIPNone,
			Selector:  selectorLabels(cluster, component),
			// Pods must resolve each other's DNS names before they are ready,
			// otherwise coordinators could never form a cluster.
			PublishNotReadyAddresses: true,
			Ports:                    ports,
		},
	}
}
