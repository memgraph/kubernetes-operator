/*
Copyright 2024 Memgraph Ltd.

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

package controller

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	memgraphv1 "github.com/memgraph/kubernetes-operator/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
)

func (r *MemgraphHAReconciler) reconcileDataInstance(ctx context.Context, memgraphha *memgraphv1.MemgraphHA, logger *logr.Logger, dataInstanceId int) (bool, error) {
	name := fmt.Sprintf("memgraph-data-%d", dataInstanceId)
	logger.Info("Started reconciling", "StatefulSet", name)
	dataInstanceStatefulSet := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: memgraphha.Namespace}, dataInstanceStatefulSet)

	if err == nil {
		logger.Info("StatefulSet already exists.", "StatefulSet", name)
		return false, nil
	}

	if errors.IsNotFound(err) {
		dataInstance := r.createStatefulSetForDataInstance(memgraphha, dataInstanceId)
		logger.Info("Creating a new StatefulSet", "StatefulSet.Namespace", dataInstance.Namespace, "StatefulSet.Name", dataInstance.Name)
		err := r.Create(ctx, dataInstance)
		if err != nil {
			logger.Error(err, "Failed to create new StatefulSet", "StatefulSet.Namespace", dataInstance.Namespace, "StatefulSet.Name", dataInstance.Name)
			return true, err
		}
		logger.Info("StatefulSet is created.", "StatefulSet", name)
		return true, nil
	}

	logger.Error(err, "Failed to fetch StatefulSet", "StatefulSet", name)
	return true, err

}

func (r *MemgraphHAReconciler) createStatefulSetForDataInstance(memgraphha *memgraphv1.MemgraphHA, dataInstanceId int) *appsv1.StatefulSet {
	dataInstanceName := fmt.Sprintf("memgraph-data-%d", dataInstanceId)
	serviceName := dataInstanceName
	labels := createDataInstanceLabels(dataInstanceName)
	replicas := int32(1)
	containerName := "memgraph-data"
	image := "memgraph/memgraph:2.18.1"
	boltPort := 7687
	replicationPort := 12000
	mgmtPort := 10000
	args := []string{
		fmt.Sprintf("--management-port=%d", mgmtPort),
		fmt.Sprintf("--bolt-port=%d", boltPort),
		"--experimental-enabled=high-availability",
		"--also-log-to-stderr",
		"--log-level=TRACE",
		"--log-file=/var/log/memgraph/memgraph.log",
	}
	license := "<TODO> add"
	organization := "testing-k8"
	volumeLibName := fmt.Sprintf("%s-lib-storage", dataInstanceName)
	volumeLibSize := "1Gi"
	volumeLogName := fmt.Sprintf("%s-log-storage", dataInstanceName)
	volumeLogSize := "256Mi"
	initContainerName := "init"
	initContainerCommand := []string{
		"/bin/sh",
		"-c",
	}
	initContainerArgs := []string{"chown -R memgraph:memgraph /var/log; chown -R memgraph:memgraph /var/lib"}
	initContainerPrivileged := true
	initContainerReadOnlyRootFilesystem := false
	initContainerRunAsNonRoot := false
	initContainerRunAsUser := int64(0)

	// TODO: (andi) How handle licensing info?
	data := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dataInstanceName,
			Namespace: memgraphha.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: serviceName,
			Replicas:    &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name:  initContainerName,
							Image: image,
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      volumeLibName,
									MountPath: "/var/lib/memgraph",
								},
								{
									Name:      volumeLogName,
									MountPath: "/var/log/memgraph",
								},
							},
							Command: initContainerCommand,
							Args:    initContainerArgs,
							SecurityContext: &corev1.SecurityContext{
								Privileged:             &initContainerPrivileged,
								ReadOnlyRootFilesystem: &initContainerReadOnlyRootFilesystem,
								RunAsNonRoot:           &initContainerRunAsNonRoot,
								RunAsUser:              &initContainerRunAsUser,
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{"all"},
									Add:  []corev1.Capability{"CHOWN"},
								},
							},
						},
					},

					Containers: []corev1.Container{{
						Name:            containerName,
						Image:           image,
						ImagePullPolicy: corev1.PullAlways,
						Ports: []corev1.ContainerPort{
							{
								ContainerPort: int32(boltPort),
								Name:          "bolt",
							},
							{
								ContainerPort: int32(mgmtPort),
								Name:          "management",
							},
							{
								ContainerPort: int32(replicationPort),
								Name:          "replication",
							},
						},
						Args: args,
						Env: []corev1.EnvVar{
							{
								Name:  "MEMGRAPH_ENTERPRISE_LICENSE",
								Value: license,
							},
							{
								Name:  "MEMGRAPH_ORGANIZATION_NAME",
								Value: organization,
							},
						},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      volumeLibName,
								MountPath: "/var/lib/memgraph",
							},
							{
								Name:      volumeLogName,
								MountPath: "/var/log/memgraph",
							},
						},
					}},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: volumeLibName,
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse(volumeLibSize),
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: volumeLogName,
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse(volumeLogSize),
							},
						},
					},
				},
			},
		},
	}

	ctrl.SetControllerReference(memgraphha, data, r.Scheme)
	return data
}

func createDataInstanceLabels(dataInstanceName string) map[string]string {
	return map[string]string{"app": dataInstanceName}
}
