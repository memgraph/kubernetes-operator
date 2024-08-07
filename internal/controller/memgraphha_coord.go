/*
copyright 2024 memgraph ltd.

licensed under the apache license, version 2.0 (the "license");
you may not use this file except in compliance with the license.
you may obtain a copy of the license at

    http://www.apache.org/licenses/license-2.0

unless required by applicable law or agreed to in writing, software
distributed under the license is distributed on an "as is" basis,
without warranties or conditions of any kind, either express or implied.
see the license for the specific language governing permissions and
limitations under the license.
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

/*
Returns bool, error tuple. If error exists, the caller should return with error and status will always be set to true.
If there is no error, we must look at bool status which when true will say that the coordinator was createdand we need to requeue
or that nothing was done and we can continue with the next step of reconciliation.
*/
func (r *MemgraphHAReconciler) reconcileCoordinator(ctx context.Context, memgraphha *memgraphv1.MemgraphHA, logger *logr.Logger, coordId int) (bool, error) {
	name := fmt.Sprintf("memgraph-coordinator-%d", coordId)
	logger.Info("Started reconciling", name)
	coordStatefulSet := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: memgraphha.Namespace}, coordStatefulSet)

	if err == nil {
		logger.Info("StatefulSet", name, "already exists.")
		return false, nil
	}

	if errors.IsNotFound(err) {
		coord := r.createStatefulSetForCoord(memgraphha, coordId)
		logger.Info("Creating a new StatefulSet", "StatefulSet.Namespace", coord.Namespace, "StatefulSet.Name", coord.Name)
		err := r.Create(ctx, coord)
		if err != nil {
			logger.Error(err, "Failed to create new StatefulSet", "StatefulSet.Namespace", coord.Namespace, "StatefulSet.Name", coord.Name)
			return true, err
		}
		logger.Info("StatefulSet", name, "is created.")
		return true, nil
	}

	logger.Error(err, "Failed to fetch StatefulSet", name)
	return true, err
}

func (r *MemgraphHAReconciler) createStatefulSetForCoord(memgraphha *memgraphv1.MemgraphHA, coordId int) *appsv1.StatefulSet {
	coordName := fmt.Sprintf("memgraph-coordinator-%d", coordId)
	serviceName := coordName
	labels := createCoordLabels(coordName)
	replicas := int32(1)
	containerName := "memgraph-coordinator"
	image := "memgraph/memgraph:2.18.1"
	boltPort := 7687
	coordPort := 12000
	mgmtPort := 10000
	args := []string{
		fmt.Sprintf("--coordinator-id=%d", coordId),
		fmt.Sprintf("--coordinator-port=%d", coordPort),
		fmt.Sprintf("--management-port=%d", mgmtPort),
		fmt.Sprintf("--bolt-port=%d", boltPort),
		fmt.Sprintf("--coordinator-hostname=%s.default.svc.cluster.local", coordName),
		"--experimental-enabled=high-availability",
		"--also-log-to-stderr",
		"--log-level=TRACE",
		"--log-file=/var/log/memgraph/memgraph.log",
		"--nuraft-log-file=/var/log/memgraph/memgraph.log",
	}
	license := "<TODO> add"
	organization := "testing-k8"
	volumeLibName := fmt.Sprintf("%s-lib-storage", coordName)
	volumeLibSize := "1Gi"
	volumeLogName := fmt.Sprintf("%s-log-storage", coordName)
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

	// TODO (andi): How to handle license and organization name?
	coord := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      coordName,
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
						ImagePullPolicy: corev1.PullIfNotPresent,
						Ports: []corev1.ContainerPort{
							{
								ContainerPort: int32(boltPort),
								Name:          "boltPort",
							},
							{
								ContainerPort: int32(mgmtPort),
								Name:          "managementPort",
							},
							{
								ContainerPort: int32(coordPort),
								Name:          "coordinatorPort",
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
								corev1.ResourceEphemeralStorage: resource.MustParse(volumeLibSize),
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
								corev1.ResourceEphemeralStorage: resource.MustParse(volumeLogSize),
							},
						},
					},
				},
			},
		},
	}

	ctrl.SetControllerReference(memgraphha, coord, r.Scheme)
	return coord
}

func createCoordLabels(coordName string) map[string]string {
	return map[string]string{"app": coordName}
}
