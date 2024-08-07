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

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
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

	for coordId := 1; coordId <= 3; coordId++ {
		// ClusterIP
		coordClusterIPStatus, coordClusterIPErr := r.reconcileCoordClusterIPService(ctx, memgraphha, &logger, coordId)
		if coordClusterIPErr != nil {
			logger.Info("Error returned when reconciling ClusterIP with id", coordId, "Returning empty Result with error.")
			return ctrl.Result{}, coordClusterIPErr
		}

		if coordClusterIPStatus == true {
			logger.Info("ClusterIP with id", coordId, "has been created. Returning Result with the request for requeing with error set to nil.")
			return ctrl.Result{Requeue: true}, nil
		}

		// NodePort
		coordNodePortStatus, coordNodePortErr := r.reconcileCoordNodePortService(ctx, memgraphha, &logger, coordId)
		if coordNodePortErr != nil {
			logger.Info("Error returned when reconciling NodePort with id", coordId, "Returning empty Result with error.")
			return ctrl.Result{}, coordNodePortErr
		}

		if coordNodePortStatus == true {
			logger.Info("NodePort with id", coordId, "has been created. Returning Result with the request for requeing with error set to nil.")
			return ctrl.Result{Requeue: true}, nil
		}

		// Coordinator
		coordStatus, coordErr := r.reconcileCoordinator(ctx, memgraphha, &logger, coordId)
		if coordErr != nil {
			logger.Info("Error returned when reconciling coordinator", coordId, "Returning empty Result with error.")
			return ctrl.Result{}, coordErr
		}

		if coordStatus == true {
			logger.Info("Coordinator", coordId, "has been created. Returning Result with the request for requeing with error set to nil.")
			return ctrl.Result{Requeue: true}, nil
		}
	}

	logger.Info("Reconciliation of coordinators finished without actions needed.")

	for dataInstanceId := 0; dataInstanceId <= 1; dataInstanceId++ {

		// Data instance
		dataInstancesStatus, dataInstancesErr := r.reconcileDataInstance(ctx, memgraphha, &logger, dataInstanceId)
		if dataInstancesErr != nil {
			logger.Info("Error returned when reconciling data instance", dataInstanceId, "Returning empty Result with error.")
			return ctrl.Result{}, dataInstancesErr
		}

		if dataInstancesStatus == true {
			logger.Info("Data instance", dataInstanceId, "has been created. Returning Result with the request for requeing with error=nil.")
			return ctrl.Result{Requeue: true}, nil
		}
	}

	logger.Info("Reconciliation of data instances finished without actions needed.")

	// The resource doesn't need to be reconciled anymore
	return ctrl.Result{}, nil
}

func (r *MemgraphHAReconciler) reconcileDataInstance(ctx context.Context, memgraphha *memgraphv1.MemgraphHA, logger *logr.Logger, dataInstanceId int) (bool, error) {
	name := fmt.Sprintf("memgraph-data-%d", dataInstanceId)
	logger.Info("Started reconciling", name)
	dataInstanceStatefulSet := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: memgraphha.Namespace}, dataInstanceStatefulSet)

	if err == nil {
		logger.Info("StatefulSet", name, "already exists.")
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
		logger.Info("StatefulSet", name, "is created.")
		return true, nil
	}

	logger.Error(err, "Failed to fetch StatefulSet", name)
	return true, err

}

func (r *MemgraphHAReconciler) createStatefulSetForDataInstance(memgraphha *memgraphv1.MemgraphHA, dataInstanceId int) *appsv1.StatefulSet {
	dataInstanceName := fmt.Sprintf("memgraph-data-%d", dataInstanceId)
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

	// TODO:
	/*
		add serviceName
		env
	*/

	data := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dataInstanceName,
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
								ContainerPort: int32(replicationPort),
								Name:          "replicationPort",
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

	ctrl.SetControllerReference(memgraphha, data, r.Scheme)
	return data
}

func (r *MemgraphHAReconciler) reconcileCoordNodePortService(ctx context.Context, memgraphha *memgraphv1.MemgraphHA, logger *logr.Logger, coordId int) (bool, error) {
	serviceName := fmt.Sprintf("memgraph-coordinator-%d-external", coordId)
	logger.Info("Started reconciling NodePort service", serviceName)

	coordNodePortService := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: serviceName, Namespace: memgraphha.Namespace}, coordNodePortService)

	if err == nil {
		logger.Info("NodePort", serviceName, "already exists.")
		return false, nil
	}

	if errors.IsNotFound(err) {
		nodePort := r.createNodePort(memgraphha, coordId)
		logger.Info("Creating a new NodePort", "NodePort.Namespace", nodePort.Namespace, "NodePort.Name", nodePort.Name)
		err := r.Create(ctx, nodePort)
		if err != nil {
			logger.Error(err, "Failed to create new NodePort", "NodePort.Namespace", nodePort.Namespace, "NodePort.Name", nodePort.Name)
			return true, err
		}
		logger.Info("NodePort", serviceName, "is created.")
		return true, nil
	}

	logger.Error(err, "Failed to fetch NodePort", serviceName)
	return true, err

}

func (r *MemgraphHAReconciler) createNodePort(memgraphha *memgraphv1.MemgraphHA, coordId int) *corev1.Service {
	serviceName := fmt.Sprintf("memgraph-coordinator-%d-external", coordId)
	coordName := fmt.Sprintf("memgraph-coordinator-%d", coordId)
	// TODO: (andi) Extract somehow configuration and move into separate files.
	boltPort := 7687

	coordNodePort := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: memgraphha.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeNodePort,
			Selector: createCoordLabels(coordName),
			Ports: []corev1.ServicePort{
				{
					Name:       "bolt",
					Protocol:   corev1.ProtocolTCP,
					Port:       int32(boltPort),
					TargetPort: intstr.FromInt(boltPort),
				},
			},
		},
	}

	ctrl.SetControllerReference(memgraphha, coordNodePort, r.Scheme)
	return coordNodePort
}

func (r *MemgraphHAReconciler) reconcileCoordClusterIPService(ctx context.Context, memgraphha *memgraphv1.MemgraphHA, logger *logr.Logger, coordId int) (bool, error) {
	serviceName := fmt.Sprintf("memgraph-coordinator-%d", coordId)
	logger.Info("Started reconciling ClusterIP service", serviceName)

	coordClusterIPService := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: serviceName, Namespace: memgraphha.Namespace}, coordClusterIPService)

	if err == nil {
		logger.Info("ClusterIP", serviceName, "already exists.")
		return false, nil
	}

	if errors.IsNotFound(err) {
		clusterIP := r.createCoordClusterIP(memgraphha, coordId)
		logger.Info("Creating a new ClusterIP", "ClusterIP.Namespace", clusterIP.Namespace, "ClusterIP.Name", clusterIP.Name)
		err := r.Create(ctx, clusterIP)
		if err != nil {
			logger.Error(err, "Failed to create new ClusterIP", "ClusterIP.Namespace", clusterIP.Namespace, "ClusterIP.Name", clusterIP.Name)
			return true, err
		}
		logger.Info("ClusterIP", serviceName, "is created.")
		return true, nil
	}

	logger.Error(err, "Failed to fetch ClusterIP", serviceName)
	return true, err

}

func (r *MemgraphHAReconciler) createCoordClusterIP(memgraphha *memgraphv1.MemgraphHA, coordId int) *corev1.Service {
	serviceName := fmt.Sprintf("memgraph-coordinator-%d", coordId)
	coordName := serviceName
	// TODO: (andi) Extract somehow configuration and move into separate files.
	boltPort := 7687
	coordinatorPort := 12000
	mgmtPort := 10000

	coordClusterIP := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: memgraphha.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: createCoordLabels(coordName),
			Ports: []corev1.ServicePort{
				{
					Name:       "bolt",
					Protocol:   corev1.ProtocolTCP,
					Port:       int32(boltPort),
					TargetPort: intstr.FromInt(boltPort),
				},
				{
					Name:       "coordinator",
					Protocol:   corev1.ProtocolTCP,
					Port:       int32(coordinatorPort),
					TargetPort: intstr.FromInt(coordinatorPort),
				},
				{
					Name:       "management",
					Protocol:   corev1.ProtocolTCP,
					Port:       int32(mgmtPort),
					TargetPort: intstr.FromInt(mgmtPort),
				},
			},
		},
	}

	ctrl.SetControllerReference(memgraphha, coordClusterIP, r.Scheme)
	return coordClusterIP
}

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

	// TODO:
	/*
		add serviceName
		env
	*/

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

func createDataInstanceLabels(dataInstanceName string) map[string]string {
	return map[string]string{"app": dataInstanceName}
}

// SetupWithManager sets up the controller with the Manager.
func (r *MemgraphHAReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&memgraphv1.MemgraphHA{}).
		Complete(r)
}
