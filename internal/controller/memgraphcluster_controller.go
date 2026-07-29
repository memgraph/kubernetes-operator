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
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
// headless Service, at the replica counts replicaCounts derives. Then, once
// every pod is ready, it reconciles cluster registration: observe SHOW
// INSTANCES on the coordinator leader, diff against the declared topology, and
// issue only the missing commands — which is all growing a live cluster takes,
// because a raised count declares members the observed cluster does not have
// registered yet. A lowered dataInstances count runs the same loop in reverse:
// the members beyond the declared count are demoted if one of them holds MAIN,
// unregistered, and only then are their pods shed.
//
// All interaction is read-before-write and idempotent, so an operator restart
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

	replicas, err := r.replicaCounts(ctx, &cluster)
	if err != nil {
		return ctrl.Result{}, err
	}

	if err := r.applyDesired(ctx, &cluster,
		resources.CoordinatorHeadlessService(&cluster),
		resources.DataHeadlessService(&cluster),
		resources.CoordinatorStatefulSet(&cluster, replicas.coordinators.applied),
		resources.DataStatefulSet(&cluster, replicas.data.applied),
	); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("Applied desired workload objects for MemgraphCluster", "memgraphcluster", req.NamespacedName)

	return r.reconcileRegistration(ctx, &cluster, replicas)
}

// applyDesired server-side-applies the desired workload objects, each owned by
// the cluster so garbage collection removes it with the CR.
//
// A rejected apply is retried forever behind the scenes, so it is reported on
// the resource before the error is returned: without that the conditions keep
// describing the cluster that is still running while the declared spec never
// lands, and the rejection is only visible in the operator's log. Both
// conditions go False — the workloads are not the declared ones, so neither
// serving nor convergence can be claimed for the spec the user asked for.
func (r *MemgraphClusterReconciler) applyDesired(
	ctx context.Context,
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	desired ...client.Object,
) error {
	for _, obj := range desired {
		if err := controllerutil.SetControllerReference(cluster, obj, r.Scheme); err != nil {
			return fmt.Errorf("setting owner reference on %T %s: %w", obj, obj.GetName(), err)
		}
		if err := r.apply(ctx, obj); err != nil {
			applyErr := fmt.Errorf("applying %T %s: %w", obj, obj.GetName(), err)
			msg := truncateMessage(applyErr.Error())
			if statusErr := r.writeStatus(ctx, cluster, lastObserved(cluster),
				notReadyCondition(memgraphcomv1alpha1.ReasonApplyFailed, msg),
				notConvergedCondition(memgraphcomv1alpha1.ReasonApplyFailed, msg),
			); statusErr != nil {
				return errors.Join(applyErr, statusErr)
			}
			return applyErr
		}
	}
	return nil
}

// roleReplicas is one role's replica arithmetic for a reconcile pass: how many
// replicas the spec declares, and how many the operator applies to the role's
// StatefulSet.
type roleReplicas struct {
	// name is the StatefulSet's name, so a condition message names the object
	// the user can look at.
	name     string
	declared int32
	applied  int32
}

// replicaCounts is both roles' replica arithmetic.
type replicaCounts struct {
	coordinators roleReplicas
	data         roleReplicas
}

// retirementMessage names the members a lowered count is shedding, and is empty
// when none are. It is non-empty for exactly as long as the retirement is
// unfinished: the retiring sets are derived from the replica counts the
// operator's own StatefulSets still run, so they empty only once the shrink that
// removes those pods has been applied.
func retirementMessage(topology planner.Topology) string {
	var retiring []string
	if names := coordinatorNames(topology.RetiringCoordinators); len(names) > 0 {
		retiring = append(retiring, "coordinator(s) "+strings.Join(names, ", "))
	}
	if names := instanceNames(topology.RetiringDataInstances); len(names) > 0 {
		retiring = append(retiring, "data instance(s) "+strings.Join(names, ", "))
	}
	if len(retiring) == 0 {
		return ""
	}
	return "Retiring " + strings.Join(retiring, " and ") + " before their pods are shed"
}

func coordinatorNames(coordinators []memgraph.CoordinatorSpec) []string {
	names := make([]string, 0, len(coordinators))
	for _, coordinator := range coordinators {
		names = append(names, coordinator.Name())
	}
	return names
}

func instanceNames(instances []memgraph.DataInstanceSpec) []string {
	names := make([]string, 0, len(instances))
	for _, instance := range instances {
		names = append(names, instance.Name)
	}
	return names
}

// yieldedLeader is the retiring coordinator a plan ends by moving Raft leadership
// off, or the empty string when the plan does not do that. A yield is always the
// plan's last command, because nothing after it could be planned: the election
// picks the successor, so the pass stops there and the next one observes the
// cluster under whoever won.
func yieldedLeader(commands []planner.Command) string {
	if len(commands) == 0 {
		return ""
	}
	yield, ok := commands[len(commands)-1].(planner.YieldLeadership)
	if !ok {
		return ""
	}
	return yield.Leader
}

// replicaCounts resolves the replica count to apply per role: the declared count
// while the cluster grows or holds its size, and deliberately the current count
// while a lowered count would shrink it. Shedding pods means removing members
// from the Memgraph cluster first — the coordinators otherwise keep expecting
// instances whose pods are gone, and a removed coordinator's vote must be given
// up before its pod is — so this rule never shrinks anything, which keeps it free
// of any knowledge about the cluster's state.
//
// A lowered count of either role is carried out at the end of the registration
// phase instead, once the retiring members have actually left the cluster (see
// reconcileRegistration).
func (r *MemgraphClusterReconciler) replicaCounts(
	ctx context.Context,
	cluster *memgraphcomv1alpha1.MemgraphCluster,
) (replicaCounts, error) {
	var counts replicaCounts
	for _, role := range []struct {
		name     string
		declared int32
		resolved *roleReplicas
	}{
		{resources.CoordinatorName(cluster), resources.DeclaredCoordinators(cluster), &counts.coordinators},
		{resources.DataName(cluster), resources.DeclaredDataInstances(cluster), &counts.data},
	} {
		current, err := r.currentReplicas(ctx, cluster.Namespace, role.name)
		if err != nil {
			return replicaCounts{}, err
		}
		// The larger of the two, so growing applies the declared count while
		// shrinking holds the current one.
		*role.resolved = roleReplicas{
			name: role.name, declared: role.declared, applied: max(role.declared, current),
		}
	}
	return counts, nil
}

// currentReplicas is the replica count the operator's own previous apply left on
// a role's StatefulSet, or zero when the cluster has not been provisioned yet.
func (r *MemgraphClusterReconciler) currentReplicas(
	ctx context.Context,
	namespace, name string,
) (int32, error) {
	var sts appsv1.StatefulSet
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &sts); err != nil {
		if apierrors.IsNotFound(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("getting StatefulSet %s: %w", name, err)
	}
	if sts.Spec.Replicas == nil {
		return 0, nil
	}
	return *sts.Spec.Replicas, nil
}

// reconcileRegistration converges cluster registration once the workloads are
// ready: find the coordinator leader, plan against its SHOW INSTANCES view,
// and execute the planned commands against that one connection. Unreachable
// coordinators are retried on a delay rather than surfaced as errors — Bolt
// endpoints lagging pod readiness is a normal startup phase, not a failure.
//
// The readiness gate is deliberately strict about the pods a lowered count is
// retiring too: they belong to the StatefulSet the operator is still holding at
// its current size, so a retiring pod that cannot become ready blocks its own
// removal, and the resource reports WorkloadsNotReady rather than the operator
// acting on a half-known cluster.
//
// This is also where a scale-down finishes. Once the plan comes back empty —
// meaning the retiring instances have been unregistered and the retiring
// coordinators have left the Raft cluster — the shrinking role's StatefulSet is
// applied at the declared count, shedding their pods. That is the one place the
// operator ever lowers a replica count, so the coordinators never see a registered
// instance's pod disappear, and no removed member's pod outlives its vote.
func (r *MemgraphClusterReconciler) reconcileRegistration(
	ctx context.Context,
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	replicas replicaCounts,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	ready, err := r.workloadsReady(ctx, cluster)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ready {
		log.Info("Waited for workload pods to become ready before registration")
		msg := "Waiting for all workload pods to become ready"
		if statusErr := r.writeStatus(ctx, cluster, lastObserved(cluster),
			notReadyCondition(memgraphcomv1alpha1.ReasonWorkloadsNotReady, msg),
			notConvergedCondition(memgraphcomv1alpha1.ReasonWorkloadsNotReady, msg),
		); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{RequeueAfter: requeueWhilePending}, nil
	}

	topology := resources.DeclaredTopology(cluster)
	// The members a lowered count is shedding are the ordinals the operator's own
	// previous apply still runs beyond the declared count, so the range is bounded
	// by what the operator itself created.
	topology.RetiringCoordinators = resources.RetiringCoordinators(cluster, replicas.coordinators.applied)
	topology.RetiringDataInstances = resources.RetiringDataInstances(cluster, replicas.data.applied)
	// Whether a retirement is in flight is decided once per pass, from the
	// topology alone: it is what both the condition and the shrink below key off.
	retiring := retirementMessage(topology)

	leader, observed, err := r.observeCluster(ctx, topology)
	if err != nil {
		log.Info("Deferred registration because no coordinator leader was usable", "reason", err.Error())
		reason, msg := memgraphcomv1alpha1.ReasonCoordinatorUnreachable, "No coordinator answered SHOW INSTANCES"
		if errors.Is(err, errNoCoordinatorLeader) {
			// The coordinators are up but have no leader between them, so their
			// views are stale and no registration command would be accepted.
			reason, msg = memgraphcomv1alpha1.ReasonNoCoordinatorLeader,
				"No coordinator reported a leader, so the cluster has no Raft quorum"
		}
		if statusErr := r.writeStatus(ctx, cluster, lastObserved(cluster),
			notReadyCondition(reason, msg),
			notConvergedCondition(reason, msg),
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

	latest := observe(topology, observed)
	commands := planner.Plan(topology, observed)
	if len(commands) == 0 {
		// No retiring member belongs to the cluster any more — the plan would
		// carry an UNREGISTER INSTANCE or a REMOVE COORDINATOR otherwise — so
		// their pods can go.
		if retiring != "" {
			if err := r.shedRetiredPods(ctx, cluster, topology, replicas); err != nil {
				return ctrl.Result{}, err
			}
			if statusErr := r.writeStatus(ctx, cluster, latest, readyOrNot(latest.main),
				notConvergedCondition(memgraphcomv1alpha1.ReasonRetirementInProgress, retiring),
			); statusErr != nil {
				return ctrl.Result{}, statusErr
			}
			// The shrink was applied, not yet observed back: the next pass sees the
			// lowered count, finds nothing retiring, and reports convergence.
			return ctrl.Result{RequeueAfter: requeueAfterRegistration}, nil
		}

		// Converged, but keep re-observing: a registration a pod loses later
		// produces no watch event, so drift is only caught by resyncing.
		log.Info("Confirmed cluster registration is converged")
		converged := trueCondition(memgraphcomv1alpha1.ConditionConverged,
			memgraphcomv1alpha1.ReasonAllInstancesRegistered,
			fmt.Sprintf("All %d declared instances are registered", len(topology.Coordinators)+len(topology.DataInstances)))
		if statusErr := r.writeStatus(ctx, cluster, latest, readyOrNot(latest.main), converged); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{RequeueAfter: resyncInterval}, nil
	}

	// Report the in-progress state before mutating the cluster: a MAIN already
	// serving stays Ready while a lost registration is restored; a fresh
	// bootstrap has no MAIN yet, so Ready is False until one is elected. A
	// retirement in flight is named as such — it is the more specific operation,
	// and the one whose pending members a user wants to see. A pending leadership
	// yield is more specific still: it is the one step whose outcome nobody can
	// predict, so a scale-down circling it says so rather than looking stuck on
	// the removal it cannot reach yet.
	reason := memgraphcomv1alpha1.ReasonRegistrationInProgress
	message := fmt.Sprintf("Issuing %d registration command(s) to converge the cluster", len(commands))
	if retiring != "" {
		reason, message = memgraphcomv1alpha1.ReasonRetirementInProgress, retiring
	}
	if yielded := yieldedLeader(commands); yielded != "" {
		reason = memgraphcomv1alpha1.ReasonLeadershipTransferInProgress
		message = fmt.Sprintf(
			"Retiring coordinator %s holds Raft leadership, which cannot be removed: yielding it to another member",
			yielded)
	}
	inProgress := notConvergedCondition(reason, message)
	if statusErr := r.writeStatus(ctx, cluster, latest, readyOrNot(latest.main), inProgress); statusErr != nil {
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

// shedRetiredPods applies the shrinking roles' StatefulSets at their declared
// replica counts — the one place the operator ever lowers a replica count. It is
// reached only after the plan came back empty, so every pod it sheds belongs to a
// member that has already left the Memgraph cluster: an unregistered data
// instance, or a coordinator whose Raft vote is gone.
func (r *MemgraphClusterReconciler) shedRetiredPods(
	ctx context.Context,
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	topology planner.Topology,
	replicas replicaCounts,
) error {
	log := logf.FromContext(ctx)
	for _, role := range []struct {
		retiring int
		replicas roleReplicas
		build    func(*memgraphcomv1alpha1.MemgraphCluster, int32) *appsv1.StatefulSet
	}{
		{len(topology.RetiringCoordinators), replicas.coordinators, resources.CoordinatorStatefulSet},
		{len(topology.RetiringDataInstances), replicas.data, resources.DataStatefulSet},
	} {
		if role.retiring == 0 {
			continue
		}
		if err := r.applyDesired(ctx, cluster, role.build(cluster, role.replicas.declared)); err != nil {
			return err
		}
		log.Info("Shrank a StatefulSet to the declared replica count",
			"statefulset", role.replicas.name, "replicas", role.replicas.declared)
	}
	return nil
}

// observation is everything a reconcile pass observed about the cluster that
// reaches the resource's status: which data instance is MAIN, and how many of
// each role's declared members are registered. It is observation only — no
// reconcile decision reads it back off the status.
type observation struct {
	main                    string
	registeredCoordinators  int32
	registeredDataInstances int32
}

// observe reads the coordinator leader's cluster view into the status fields.
func observe(topology planner.Topology, observed []memgraph.Instance) observation {
	coordinators, dataInstances := planner.Registered(topology, observed)
	return observation{
		main:                    observedMain(observed),
		registeredCoordinators:  coordinators,
		registeredDataInstances: dataInstances,
	}
}

// lastObserved is the observation already published on the resource. The paths
// that could not observe the cluster this pass republish it: an unready pod or
// an unreachable coordinator says nothing about what the last reachable leader
// reported.
func lastObserved(cluster *memgraphcomv1alpha1.MemgraphCluster) observation {
	return observation{
		main:                    cluster.Status.Main,
		registeredCoordinators:  cluster.Status.Coordinators,
		registeredDataInstances: cluster.Status.DataInstances,
	}
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

// writeStatus patches the status subresource with the pass's observation and the
// given conditions. It uses the status subresource exclusively — spec is never
// touched — and skips the patch when nothing changed, so a converged cluster
// re-observed on every resync does not churn the resource version.
func (r *MemgraphClusterReconciler) writeStatus(
	ctx context.Context,
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	observed observation,
	conditions ...metav1.Condition,
) error {
	base := cluster.DeepCopy()
	cluster.Status.Main = observed.main
	cluster.Status.Coordinators = observed.registeredCoordinators
	cluster.Status.DataInstances = observed.registeredDataInstances
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

// errNoCoordinatorLeader reports that coordinators answered SHOW INSTANCES but
// none of them named a leader. It is distinguished from unreachable
// coordinators because the two need different remedies, and it is reported as
// such on the resource.
var errNoCoordinatorLeader = errors.New("no coordinator reported a leader")

// observeCluster connects to the coordinator leader and returns its client
// together with the SHOW INSTANCES view the planner diffs against.
// Coordinators are tried in ordinal order: one reporting itself leader is used
// directly, and one reporting another coordinator as leader redirects to it.
//
// A coordinator that names no leader is skipped, never used as planning input.
// Its view is not the fresh-cluster case: a coordinator starts with itself as
// the only member of its Raft configuration and as the leader of that
// one-member cluster, so a fresh coordinator always names itself. An absent
// leader means the coordinator lost track of one — quorum gone, or it stepped
// down or was removed from the Raft cluster — and it then answers from its own
// state machine, which can be arbitrarily stale. Every mutating query the
// planner could issue needs a leader anyway, so such a view describes a cluster
// state that is neither current nor writable.
func (r *MemgraphClusterReconciler) observeCluster(
	ctx context.Context,
	topology planner.Topology,
) (memgraph.Client, []memgraph.Instance, error) {
	var errs []error
	leaderless := false
	for _, coordinator := range topology.Coordinators {
		conn, observed, err := r.showInstances(ctx, coordinator.BoltServer)
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
		if leaderName == coordinator.Name() {
			return conn, observed, nil
		}
		if err := conn.Close(ctx); err != nil {
			errs = append(errs, err)
		}
		if leaderName == "" {
			leaderless = true
			errs = append(errs, fmt.Errorf("%s reported no leader", coordinator.Name()))
			continue
		}

		// This coordinator is a follower; redirect to the leader it reports.
		address, found := leaderAddress(topology, observed, leaderName)
		if !found {
			errs = append(errs, fmt.Errorf("%s reported leader %s without a Bolt address",
				coordinator.Name(), leaderName))
			continue
		}
		conn, observed, err = r.showInstances(ctx, address)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		return conn, observed, nil
	}
	if leaderless {
		return nil, nil, fmt.Errorf("%w: %w", errNoCoordinatorLeader, errors.Join(errs...))
	}
	return nil, nil, fmt.Errorf("no coordinator answered SHOW INSTANCES: %w", errors.Join(errs...))
}

// leaderAddress resolves the Bolt address of the coordinator reported as
// leader. The declared topology is preferred — it is the address the operator
// itself registered — but the reported view is a valid fallback, because the
// leader need not be one of the declared coordinators: a coordinator on its way
// out of the cluster can hold leadership while it is still being removed.
func leaderAddress(topology planner.Topology, observed []memgraph.Instance, name string) (string, bool) {
	for _, coordinator := range topology.Coordinators {
		if coordinator.Name() == name {
			return coordinator.BoltServer, true
		}
	}
	for _, instance := range observed {
		if instance.Name == name && instance.BoltServer != "" {
			return instance.BoltServer, true
		}
	}
	return "", false
}

// showInstances connects to one coordinator's Bolt address and fetches its
// cluster view, closing the connection again on query failure.
func (r *MemgraphClusterReconciler) showInstances(
	ctx context.Context,
	address string,
) (memgraph.Client, []memgraph.Instance, error) {
	c, err := r.Memgraph.Connect(ctx, address)
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
