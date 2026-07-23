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
	"errors"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
	"github.com/memgraph/kubernetes-operator/internal/memgraph"
	"github.com/memgraph/kubernetes-operator/internal/planner"
	"github.com/memgraph/kubernetes-operator/internal/resources"
)

// fieldOwner identifies this controller as the server-side-apply field
// manager of the workload objects it provisions.
const fieldOwner = "memgraph-operator"

const (
	// requeueWhilePending is how long to wait before retrying when the
	// cluster cannot be registered yet — pods not ready, or coordinators not
	// answering Bolt queries. Both are expected while the cluster starts up.
	requeueWhilePending = 10 * time.Second

	// requeueAfterRegistration schedules the follow-up reconcile that
	// verifies issued registration commands actually converged the cluster.
	requeueAfterRegistration = 10 * time.Second
)

// MemgraphClusterReconciler reconciles a MemgraphCluster object
type MemgraphClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Memgraph opens Bolt connections to coordinators. Tests substitute a
	// fake; everything above the memgraph.Client interface never touches the
	// Bolt driver.
	Memgraph memgraph.Connector
}

// +kubebuilder:rbac:groups=memgraph.com,resources=memgraphclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=memgraph.com,resources=memgraphclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=memgraph.com,resources=memgraphclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete

// Reconcile drives the cluster toward the declared MemgraphCluster spec in
// two stages. First it server-side-applies the builders' desired objects: one
// StatefulSet per role (coordinators, data instances), each backed by a
// headless Service. Then, once every pod is ready, it reconciles cluster
// registration: observe SHOW INSTANCES on the coordinator leader, diff
// against the declared topology, and issue only the missing commands. All
// interaction is read-before-write and idempotent, so an operator restart
// mid-bootstrap is harmless. Deletion needs no handling here — every object
// carries a controller owner reference, so garbage collection removes the
// workloads with the CR.
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

	return r.reconcileRegistration(ctx, &cluster)
}

// reconcileRegistration converges cluster registration once the workloads are
// ready: find the coordinator leader, plan against its SHOW INSTANCES view,
// and execute the missing commands. Unreachable coordinators are retried on a
// delay rather than surfaced as errors — Bolt endpoints lagging pod readiness
// is a normal startup phase, not a failure.
func (r *MemgraphClusterReconciler) reconcileRegistration(
	ctx context.Context,
	cluster *memgraphcomv1alpha1.MemgraphCluster,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	ready, err := r.workloadsReady(ctx, cluster)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ready {
		log.Info("Waited for workload pods to become ready before registration")
		return ctrl.Result{RequeueAfter: requeueWhilePending}, nil
	}

	topology := resources.DeclaredTopology(cluster)
	leader, observed, err := r.observeCluster(ctx, topology)
	if err != nil {
		log.Info("Deferred registration because no coordinator answered", "reason", err.Error())
		return ctrl.Result{RequeueAfter: requeueWhilePending}, nil
	}
	defer func() {
		if err := leader.Close(ctx); err != nil {
			log.Error(err, "Failed to close coordinator connection")
		}
	}()

	commands := planner.Plan(topology, observed)
	if len(commands) == 0 {
		log.Info("Confirmed cluster registration is converged")
		return ctrl.Result{}, nil
	}
	for _, command := range commands {
		if err := command.Run(ctx, leader); err != nil {
			return ctrl.Result{}, fmt.Errorf("executing registration command %q: %w", command, err)
		}
		log.Info("Executed registration command", "command", command.String())
	}

	// Registration was issued, not yet observed back; verify convergence on a
	// follow-up reconcile instead of assuming success.
	return ctrl.Result{RequeueAfter: requeueAfterRegistration}, nil
}

// workloadsReady reports whether both role StatefulSets have all their pods
// ready. Registration waits for the full topology: coordinators cannot form a
// Raft cluster and data instances cannot be registered until every advertised
// address resolves to a running pod.
func (r *MemgraphClusterReconciler) workloadsReady(
	ctx context.Context,
	cluster *memgraphcomv1alpha1.MemgraphCluster,
) (bool, error) {
	for _, name := range []string{resources.CoordinatorName(cluster), resources.DataName(cluster)} {
		var sts appsv1.StatefulSet
		if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: cluster.Namespace}, &sts); err != nil {
			return false, fmt.Errorf("getting StatefulSet %s: %w", name, err)
		}
		if sts.Spec.Replicas == nil || sts.Status.ReadyReplicas < *sts.Spec.Replicas {
			return false, nil
		}
	}
	return true, nil
}

// observeCluster connects to the coordinator leader and returns its client
// together with the SHOW INSTANCES view the planner diffs against.
// Coordinators are tried in ordinal order: one reporting itself leader is
// used directly, a follower redirects to the leader it reports, and when no
// leader exists yet (fresh cluster, Raft not formed) the first reachable
// coordinator is used — adding coordinators to it makes it the leader,
// mirroring the HA chart's bootstrap against its first coordinator.
func (r *MemgraphClusterReconciler) observeCluster(
	ctx context.Context,
	topology planner.Topology,
) (memgraph.Client, []memgraph.Instance, error) {
	var errs []error
	for _, coordinator := range topology.Coordinators {
		leader, observed, err := r.showInstances(ctx, coordinator)
		if err != nil {
			errs = append(errs, err)
			continue
		}

		leaderName := ""
		for _, instance := range observed {
			if instance.IsLeader() {
				leaderName = instance.Name
				break
			}
		}
		if leaderName == "" || leaderName == coordinator.Name() {
			return leader, observed, nil
		}

		// This coordinator is a follower; redirect to the leader it reports.
		if err := leader.Close(ctx); err != nil {
			errs = append(errs, err)
		}
		candidate, found := coordinatorByName(topology, leaderName)
		if !found {
			errs = append(errs, fmt.Errorf("%s reported leader %s, which is not declared", coordinator.Name(), leaderName))
			continue
		}
		leader, observed, err = r.showInstances(ctx, candidate)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		return leader, observed, nil
	}
	return nil, nil, fmt.Errorf("no coordinator leader reachable: %w", errors.Join(errs...))
}

func coordinatorByName(topology planner.Topology, name string) (memgraph.CoordinatorSpec, bool) {
	for _, coordinator := range topology.Coordinators {
		if coordinator.Name() == name {
			return coordinator, true
		}
	}
	return memgraph.CoordinatorSpec{}, false
}

// showInstances connects to one coordinator and fetches its cluster view,
// closing the connection again on query failure.
func (r *MemgraphClusterReconciler) showInstances(
	ctx context.Context,
	coordinator memgraph.CoordinatorSpec,
) (memgraph.Client, []memgraph.Instance, error) {
	c, err := r.Memgraph.Connect(ctx, coordinator.BoltServer)
	if err != nil {
		return nil, nil, err
	}
	observed, err := c.ShowInstances(ctx)
	if err != nil {
		if closeErr := c.Close(ctx); closeErr != nil {
			return nil, nil, errors.Join(err, closeErr)
		}
		return nil, nil, err
	}
	return c, observed, nil
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
