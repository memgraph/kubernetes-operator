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
	"k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	// resyncInterval is how often a converged cluster is re-observed to catch
	// registration drift. A pod that loses its registration (rescheduled onto
	// a fresh node, wiped storage) while still running produces no watch event
	// — its StatefulSet is unchanged — so a lost registration would otherwise
	// go undetected until an unrelated reconcile. This periodic resync is what
	// makes re-registration continuous rather than one-shot.
	resyncInterval = 30 * time.Second
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

// The install chart's ClusterRole is generated from these markers, so they are
// the operator's permission surface: nothing broader is ever granted. The
// verbs are only the ones this reconciler issues — it reads MemgraphClusters
// and patches their status, and server-side-applies (create plus patch) the
// workloads without ever updating or deleting them, because deletion belongs
// to garbage collection via the owner references. The finalizers subresource
// is needed to set those owner references: they block owner deletion, which
// clusters running the OwnerReferencesPermissionEnforcement admission plugin
// only allow with update access to the owner's finalizers.
// +kubebuilder:rbac:groups=memgraph.com,resources=memgraphclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=memgraph.com,resources=memgraphclusters/status,verbs=get;patch
// +kubebuilder:rbac:groups=memgraph.com,resources=memgraphclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;patch
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;patch

// Reconcile drives the cluster toward the declared MemgraphCluster spec in
// two stages. First it server-side-applies the builders' desired objects: one
// StatefulSet per role (coordinators, data instances), each backed by a
// headless Service. Then, once every pod is ready, it reconciles cluster
// registration: observe SHOW INSTANCES on the coordinator leader, diff
// against the declared topology, and issue only the missing commands. All
// interaction is read-before-write and idempotent, so an operator restart
// mid-bootstrap is harmless. Registration reconciliation is continuous, not
// one-shot: a converged cluster is re-observed on a periodic resync, so a
// registration a pod loses (rescheduled, wiped storage) is re-issued without
// human action. An apply the API server rejects — an edit to a field
// Kubernetes treats as immutable, a quota denial — is reported on the resource
// as ApplyFailed rather than only in the log, because no amount of retrying
// will clear it. Deletion needs no handling here — every object
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
			applyErr := fmt.Errorf("applying %T %s: %w", obj, obj.GetName(), err)
			// A rejected apply is retried forever behind the scenes, so report
			// it on the resource: without this the conditions keep describing
			// the cluster that is still running while the declared spec never
			// lands, and the rejection is only visible in the operator's log.
			// Both conditions go False — the workloads are not the declared
			// ones, so neither serving nor convergence can be claimed for the
			// spec the user asked for.
			msg := truncateMessage(applyErr.Error())
			if statusErr := r.writeStatus(ctx, &cluster, cluster.Status.Main,
				notReadyCondition(memgraphcomv1alpha1.ReasonApplyFailed, msg),
				notConvergedCondition(memgraphcomv1alpha1.ReasonApplyFailed, msg),
			); statusErr != nil {
				return ctrl.Result{}, errors.Join(applyErr, statusErr)
			}
			return ctrl.Result{}, applyErr
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
		msg := "Waiting for all workload pods to become ready"
		if statusErr := r.writeStatus(ctx, cluster, cluster.Status.Main,
			notReadyCondition(memgraphcomv1alpha1.ReasonWorkloadsNotReady, msg),
			notConvergedCondition(memgraphcomv1alpha1.ReasonWorkloadsNotReady, msg),
		); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{RequeueAfter: requeueWhilePending}, nil
	}

	topology := resources.DeclaredTopology(cluster)
	leader, observed, err := r.observeCluster(ctx, topology)
	if err != nil {
		log.Info("Deferred registration because no coordinator answered", "reason", err.Error())
		msg := "No coordinator answered SHOW INSTANCES"
		if statusErr := r.writeStatus(ctx, cluster, cluster.Status.Main,
			notReadyCondition(memgraphcomv1alpha1.ReasonCoordinatorUnreachable, msg),
			notConvergedCondition(memgraphcomv1alpha1.ReasonCoordinatorUnreachable, msg),
		); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{RequeueAfter: requeueWhilePending}, nil
	}
	defer func() {
		if err := leader.Close(ctx); err != nil {
			log.Error(err, "Failed to close coordinator connection")
		}
	}()

	main := observedMain(observed)
	commands := planner.Plan(topology, observed)
	if len(commands) == 0 {
		// Converged, but keep re-observing: a registration a pod loses later
		// produces no watch event, so drift is only caught by resyncing.
		log.Info("Confirmed cluster registration is converged")
		converged := trueCondition(memgraphcomv1alpha1.ConditionConverged,
			memgraphcomv1alpha1.ReasonAllInstancesRegistered,
			fmt.Sprintf("All %d declared instances are registered", len(topology.Coordinators)+len(topology.DataInstances)))
		if statusErr := r.writeStatus(ctx, cluster, main, readyOrNot(main), converged); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{RequeueAfter: resyncInterval}, nil
	}

	// Report the in-progress state before mutating the cluster: a MAIN already
	// serving stays Ready while a lost registration is restored; a fresh
	// bootstrap has no MAIN yet, so Ready is False until one is elected.
	inProgress := notConvergedCondition(memgraphcomv1alpha1.ReasonRegistrationInProgress,
		fmt.Sprintf("Issuing %d registration command(s) to converge the cluster", len(commands)))
	if statusErr := r.writeStatus(ctx, cluster, main, readyOrNot(main), inProgress); statusErr != nil {
		return ctrl.Result{}, statusErr
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

// observedMain returns the name of the data instance reported as MAIN, or the
// empty string when none is elected yet.
func observedMain(observed []memgraph.Instance) string {
	for _, instance := range observed {
		if instance.IsMain() {
			return instance.Name
		}
	}
	return ""
}

// readyOrNot builds the Ready condition from whether a MAIN is elected: the
// cluster serves writes exactly when a data instance is MAIN.
func readyOrNot(main string) metav1.Condition {
	if main == "" {
		return notReadyCondition(memgraphcomv1alpha1.ReasonNoMainElected,
			"No data instance has been promoted to MAIN yet")
	}
	return trueCondition(memgraphcomv1alpha1.ConditionReady,
		memgraphcomv1alpha1.ReasonMainElected, "Data instance "+main+" is MAIN")
}

// maxConditionMessage bounds a condition message well under the API's own
// 32Ki limit: an apply rejection can carry a long field list, and the useful
// part — what the API server refused — comes first.
const maxConditionMessage = 1024

func truncateMessage(message string) string {
	if len(message) <= maxConditionMessage {
		return message
	}
	return message[:maxConditionMessage-3] + "..."
}

func trueCondition(condType, reason, message string) metav1.Condition {
	return metav1.Condition{Type: condType, Status: metav1.ConditionTrue, Reason: reason, Message: message}
}

func notReadyCondition(reason, message string) metav1.Condition {
	return metav1.Condition{
		Type: memgraphcomv1alpha1.ConditionReady, Status: metav1.ConditionFalse, Reason: reason, Message: message,
	}
}

func notConvergedCondition(reason, message string) metav1.Condition {
	return metav1.Condition{
		Type: memgraphcomv1alpha1.ConditionConverged, Status: metav1.ConditionFalse, Reason: reason, Message: message,
	}
}

// writeStatus patches the status subresource with the observed MAIN and the
// given conditions. It uses the status subresource exclusively — spec is never
// touched — and skips the patch when nothing changed, so a converged cluster
// re-observed on every resync does not churn the resource version.
func (r *MemgraphClusterReconciler) writeStatus(
	ctx context.Context,
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	main string,
	conditions ...metav1.Condition,
) error {
	base := cluster.DeepCopy()
	cluster.Status.Main = main
	for _, condition := range conditions {
		condition.ObservedGeneration = cluster.Generation
		apimeta.SetStatusCondition(&cluster.Status.Conditions, condition)
	}
	if equality.Semantic.DeepEqual(base.Status, cluster.Status) {
		return nil
	}
	if err := r.Status().Patch(ctx, cluster, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("patching MemgraphCluster status: %w", err)
	}
	return nil
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
