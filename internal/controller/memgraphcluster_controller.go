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

package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
)

// MemgraphClusterReconciler reconciles a MemgraphCluster object
type MemgraphClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=memgraph.com,resources=memgraphclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=memgraph.com,resources=memgraphclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=memgraph.com,resources=memgraphclusters/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// Placeholder implementation: fetches the MemgraphCluster and logs a
// greeting. Provisioning arrives with the walking-skeleton slice
// (specs/operator-mvp/issues/02).
func (r *MemgraphClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var cluster memgraphcomv1alpha1.MemgraphCluster
	if err := r.Get(ctx, req.NamespacedName, &cluster); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("hello from the MemgraphCluster reconciler", "memgraphcluster", req.NamespacedName)

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MemgraphClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&memgraphcomv1alpha1.MemgraphCluster{}).
		Named("memgraphcluster").
		Complete(r)
}
