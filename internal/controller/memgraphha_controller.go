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

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

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

	// The resource doesn't need to be reconciled anymore
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MemgraphHAReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&memgraphv1.MemgraphHA{}).
		Complete(r)
}
