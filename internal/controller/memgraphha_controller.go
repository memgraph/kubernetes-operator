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

	"k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	memgraphv1 "github.com/memgraph/kubernetes-operator/api/v1"
)

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

	logger.Info("MemgrahHA", "namespace", memgraphha.Namespace)

	for coordId := 1; coordId <= 3; coordId++ {
		// ClusterIP
		coordClusterIPStatus, coordClusterIPErr := r.reconcileCoordClusterIPService(ctx, memgraphha, &logger, coordId)
		if coordClusterIPErr != nil {
			logger.Info("Error returned when reconciling ClusterIP Returning empty Result with error.", "coordId", coordId)
			return ctrl.Result{}, coordClusterIPErr
		}

		if coordClusterIPStatus == true {
			logger.Info("ClusterIP has been created. Returning Result with the request for requeing with error set to nil.", "coordId", coordId)
			return ctrl.Result{Requeue: true}, nil
		}

		// NodePort
		coordNodePortStatus, coordNodePortErr := r.reconcileCoordNodePortService(ctx, memgraphha, &logger, coordId)
		if coordNodePortErr != nil {
			logger.Info("Error returned when reconciling NodePort. Returning empty Result with error.", "coordId", coordId)
			return ctrl.Result{}, coordNodePortErr
		}

		if coordNodePortStatus == true {
			logger.Info("NodePort has been created. Returning Result with the request for requeing with error set to nil.", "coordId", coordId)
			return ctrl.Result{Requeue: true}, nil
		}

		// Coordinator
		coordStatus, coordErr := r.reconcileCoordinator(ctx, memgraphha, &logger, coordId)
		if coordErr != nil {
			logger.Info("Error returned when reconciling coordinator. Returning empty Result with error.", "coordId", coordId)
			return ctrl.Result{}, coordErr
		}

		if coordStatus == true {
			logger.Info("Coordinator has been created. Returning Result with the request for requeing with error set to nil.", "coordId", coordId)
			return ctrl.Result{Requeue: true}, nil
		}
	}

	logger.Info("Reconciliation of coordinators finished without actions needed.")

	for dataInstanceId := 0; dataInstanceId <= 1; dataInstanceId++ {
		// ClusterIP
		dataInstanceClusterIPStatus, dataInstanceClusterIPErr := r.reconcileDataInstanceClusterIPService(ctx, memgraphha, &logger, dataInstanceId)
		if dataInstanceClusterIPErr != nil {
			logger.Info("Error returned when reconciling ClusterIP. Returning empty Result with error.", "dataInstanceId", dataInstanceId)
			return ctrl.Result{}, dataInstanceClusterIPErr
		}

		if dataInstanceClusterIPStatus == true {
			logger.Info("ClusterIP has been created. Returning Result with the request for requeing with error set to nil.", "dataInstanceId", dataInstanceId)
			return ctrl.Result{Requeue: true}, nil
		}

		// NodePort
		dataInstanceNodePortStatus, dataInstanceNodePortErr := r.reconcileDataInstanceNodePortService(ctx, memgraphha, &logger, dataInstanceId)
		if dataInstanceNodePortErr != nil {
			logger.Info("Error returned when reconciling NodePort. Returning empty Result with error.", "dataInstanceId", dataInstanceId)
			return ctrl.Result{}, dataInstanceNodePortErr
		}

		if dataInstanceNodePortStatus == true {
			logger.Info("NodePort has been created. Returning Result with the request for requeing with error set to nil.", "dataInstanceId", dataInstanceId)
			return ctrl.Result{Requeue: true}, nil
		}

		// Data instance
		dataInstancesStatus, dataInstancesErr := r.reconcileDataInstance(ctx, memgraphha, &logger, dataInstanceId)
		if dataInstancesErr != nil {
			logger.Info("Error returned when reconciling data instance. Returning empty Result with error.", "dataInstanceId", dataInstanceId)
			return ctrl.Result{}, dataInstancesErr
		}

		if dataInstancesStatus == true {
			logger.Info("Data instance has been created. Returning Result with the request for requeing with error=nil.", "dataInstanceId", dataInstanceId)
			return ctrl.Result{Requeue: true}, nil
		}
	}

	logger.Info("Reconciliation of data instances finished without actions needed.")

	setupJobStatus, setupJobErr := r.reconcileSetupJob(ctx, memgraphha, &logger)
	if setupJobErr != nil {
		logger.Info("Error returned when reconciling coordinator. Returning empty Result with error.")
		return ctrl.Result{}, setupJobErr
	}

	if setupJobStatus == true {
		logger.Info("SetupJob has been created. Returning Result with the request for requeing with error set to nil.")
		return ctrl.Result{Requeue: true}, nil
	}

	// The resource doesn't need to be reconciled anymore
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MemgraphHAReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&memgraphv1.MemgraphHA{}).
		Complete(r)
}
