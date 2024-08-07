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

/*
apimachinery package contains code to help developers serialize data in various formats
between Go structures and objects written in the JSON(or YAML or Protobuf)
The library is generic in the sense that it doesn't include any Kubernetes API resource
definitions.
*/

/*
API library is a collection of Go structures that are needed to work in Go with the resources
defined by the Kubernetes API. k8s.io/api is the prefix.
*/

/*
Kubernetes API
apis/memgraph/v1/...
`kubectl get pods --namespace project1 --watch -o json`
`kubectl proxy`
`HOST=http://127.0.0.1:8001`
e.g create a pod:
curl $HOST/api/v1/namespaces/project1/pods -H "Content-Type: application/yaml" --data-binary @pod.yaml
curl -X GET $HOST/api/v1/namespaces/project1/pods/nginx
*/

/*
The ResourceList type will have to be used to define the limits and requests of resources.
*/

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	// this corresponds to cachev1alpha1 from Memcached example
	memgraphv1 "github.com/memgraph/kubernetes-operator/api/v1"
)

// MemgraphHAReconciler reconciles a MemgraphHA object
type MemgraphHAReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=memgraph.com,resources=memgraphhas,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=memgraph.com,resources=memgraphhas/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=memgraph.com,resources=memgraphhas/finalizers,verbs=update

// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.16.3/pkg/reconcile
func (r *MemgraphHAReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	memgraphha := &memgraphv1.MemgraphHA{}
	err := r.Get(ctx, req.NamespacedName, memgraphha)
	if err != nil {
		// Handle specifically not found error
		if errors.IsNotFound(err) {
			logger.Info("MemgraphHA resource not found. Ignoring since object must be deleted.")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get MemgraphHA")
		// Requeue
		return ctrl.Result{}, err
	}

	logger.Info("MemgrahHA namespace", memgraphha.Namespace)

	coord1StatefulSet := &appsv1.StatefulSet{}
	err = r.Get(ctx, types.NamespacedName{Name: "memgraph-coordinator-1", Namespace: memgraphha.Namespace}, coord1StatefulSet)
	if err != nil {
		if errors.IsNotFound(err) {
			coordId := int32(1)
			coord := r.createStatefulSetForCoord(memgraphha, coordId)
			logger.Info("Creating a new StatefulSet", "StatefulSet.Namespace", coord.Namespace, "StatefulSet.Name", coord.Name)
			err := r.Create(ctx, coord)
			if err != nil {
				logger.Error(err, "Failed to create new StatefulSet", "StatefulSet.Namespace", coord.Namespace, "StatefulSet.Name", coord.Name)
				return ctrl.Result{}, err
			}
			// Coordinator is created, requeue and continue reconciliation loop
			return ctrl.Result{Requeue: true}, nil

		} else {
			logger.Error(err, "Failed to fetch StatefulSet for coordinator1")
			return ctrl.Result{}, err
		}
	}

	// The resource doesn't need to be reconciled anymore
	return ctrl.Result{}, nil
}

func (r *MemgraphHAReconciler) createStatefulSetForCoord(memgraphha *memgraphv1.MemgraphHA, coordId int32) *appsv1.StatefulSet {
	labels := createCoordLabels(coordId)
	coordName := fmt.Sprintf("memgraph-coordinator-%d", coordId)
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
		fmt.Sprintf("--coordinator-hostname=memgraph-coordinator-%d.default.svc.cluster.local", coordId),
		"--experimental-enabled=high-availability",
		"--also-log-to-stderr",
		"--log-level=TRACE",
		"--log-file=/var/log/memgraph/memgraph.log",
		"--nuraft-log-file=/var/log/memgraph/memgraph.log",
	}
	license := "<TODO> add"
	organization := "testing-k8"
	volumeLibName := fmt.Sprintf("memgraph-coordinator-%d-lib-storage", coordId)
	volumeLibSize := "1Gi"
	volumeLogName := fmt.Sprintf("memgraph-coordinator-%d-log-storage", coordId)
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

	// TODO:
	/*
		add serviceName
		env
		volumeMounts
		initContainers
		volumeClaimTemplates
	*/

	coord := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      coordName,
			Namespace: memgraphha.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
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

func createCoordLabels(coordId int32) map[string]string {
	return map[string]string{"app": fmt.Sprintf("memgraph-coordinator-%d", coordId)}
}

// SetupWithManager sets up the controller with the Manager.
func (r *MemgraphHAReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&memgraphv1.MemgraphHA{}).
		Complete(r)
}
