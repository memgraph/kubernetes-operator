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
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
	"github.com/memgraph/kubernetes-operator/internal/resources"
)

// fieldOwner identifies this controller as the server-side-apply field
// manager of the workload objects it provisions.
const fieldOwner = "memgraph-operator"

// MemgraphClusterReconciler reconciles a MemgraphCluster object
type MemgraphClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=memgraph.com,resources=memgraphclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=memgraph.com,resources=memgraphclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=memgraph.com,resources=memgraphclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete

// Reconcile drives the cluster toward the declared MemgraphCluster spec by
// server-side-applying the builders' desired objects: one StatefulSet per
// role (coordinators, data instances), each backed by a headless Service.
// Deletion needs no handling here — every object carries a controller owner
// reference, so garbage collection removes the workloads with the CR.
func (r *MemgraphClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var cluster memgraphcomv1alpha1.MemgraphCluster
	if err := r.Get(ctx, req.NamespacedName, &cluster); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	desired := []client.Object{
		resources.CoordinatorHeadlessService(&cluster),
		resources.DataHeadlessService(&cluster),
		resources.CoordinatorStatefulSet(&cluster),
		resources.DataStatefulSet(&cluster),
	}
	for _, obj := range desired {
		if err := controllerutil.SetControllerReference(&cluster, obj, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("setting owner reference on %T %s: %w", obj, obj.GetName(), err)
		}
		if err := r.apply(ctx, obj); err != nil {
			return ctrl.Result{}, fmt.Errorf("applying %T %s: %w", obj, obj.GetName(), err)
		}
	}

	log.Info("Applied desired workload objects for MemgraphCluster", "memgraphcluster", req.NamespacedName)

	return ctrl.Result{}, nil
}

// apply server-side-applies a desired object built by the resource builders.
// Builders set only the fields the operator owns, so the converted apply
// configuration claims exactly those fields for this controller.
func (r *MemgraphClusterReconciler) apply(ctx context.Context, obj client.Object) error {
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return fmt.Errorf("converting to unstructured: %w", err)
	}
	u := &unstructured.Unstructured{Object: content}
	// Zero-valued struct fields survive the conversion; drop them so the
	// applied configuration only claims fields the builders actually set.
	unstructured.RemoveNestedField(u.Object, "status")
	unstructured.RemoveNestedField(u.Object, "metadata", "creationTimestamp")
	unstructured.RemoveNestedField(u.Object, "spec", "template", "metadata", "creationTimestamp")

	return r.Apply(ctx, client.ApplyConfigurationFromUnstructured(u), client.FieldOwner(fieldOwner), client.ForceOwnership)
}

// SetupWithManager sets up the controller with the Manager.
func (r *MemgraphClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&memgraphcomv1alpha1.MemgraphCluster{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Named("memgraphcluster").
		Complete(r)
}
