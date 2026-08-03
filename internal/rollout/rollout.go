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

// Package rollout decides which single pod a changed pod template lets the
// operator restart next. Both role StatefulSets use updateStrategy OnDelete, so
// Kubernetes replaces no pod on its own and this is the only thing that ever
// does — which is also why the trigger is any revision change and not an image
// change: a template edit nobody rolls is a cluster frozen on an old spec.
//
// The decision is a pure function of what is observed — pods with their revision
// and readiness, the coordinator leader's SHOW INSTANCES view, and SHOW
// REPLICATION LAG — and it returns exactly one action per pass. One action and
// never a list, because every step is re-gated on a fresh observation: lag
// measured one pod ago says nothing about the next.
//
// Nothing is remembered between passes. The pods already carrying the new
// revision *are* the ones already restarted, so "what is left" and "what must
// have caught up" are both read off the cluster rather than tracked. That is what
// makes a spec reverted halfway through, or Raft moving MAIN or coordinator
// leadership mid-roll, self-correcting: the next pass simply re-derives the
// answer, with nothing to unwind.
//
// The order is the whole point. Data instances roll before coordinators, the
// observed MAIN is the last data pod to go, and the observed Raft leader is the
// last coordinator. Restarting the MAIN costs one coordinator-driven failover, so
// it happens once, at the end, when every instance Raft could promote in its
// place is already running the new revision. Kubernetes' own RollingUpdate cannot
// express that — it sweeps highest ordinal to lowest, and partition is a
// descending cutoff rather than a set, so a MAIN on any ordinal but 0 is restarted
// mid-sweep and each such restart buys another failover.
//
// The operator never promotes anything here. Killing the MAIN's pod leaves the
// promotion to the Raft coordinators, which is what keeps two control systems
// from choosing a MAIN at once. That is only safe on a Memgraph that reports an
// unreachable MAIN as role=main with health=down: a release that vacates the main
// row instead leaves planner.Plan believing the cluster has no MAIN, and it will
// race the failover with a promotion of its own on every single restart.
package rollout

import (
	"fmt"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
	"github.com/memgraph/kubernetes-operator/internal/memgraph"
)

// Action is what the operator does with the decision.
type Action int

const (
	// Done means every pod of both roles runs its StatefulSet's current
	// revision: there is nothing to restart.
	Done Action = iota

	// Wait means a restart is outstanding but may not proceed yet. The reason
	// and message say what is being waited for, and go straight onto the
	// resource — "why is my upgrade not moving" has to be answerable from
	// kubectl describe alone.
	Wait

	// Delete means the named pod is the next one to restart. Its StatefulSet
	// recreates it at the current revision.
	Delete
)

// Pod is one workload pod as the decision sees it: which Memgraph instance runs
// on it, which pod-template revision it carries, and whether Kubernetes
// considers it ready.
type Pod struct {
	// Name is the pod to delete.
	Name string

	// UID is the pod's identity at the moment it was observed. The delete is
	// conditioned on it, so a pod the StatefulSet already recreated between the
	// observation and the delete is never restarted a second time.
	UID string

	// Instance is the name this pod's Memgraph instance is known by in SHOW
	// INSTANCES — instance_N for data pods, coordinator_N+1 for coordinators.
	// The decision matches observations by this name, never by pod name.
	Instance string

	// Ordinal is the pod's StatefulSet ordinal, which orders restarts within a
	// role.
	Ordinal int32

	// RevisionHash is the pod's controller-revision-hash label, set by the
	// StatefulSet controller.
	RevisionHash string

	// Ready is the pod's Kubernetes readiness, which for these pods is a TCP
	// connect to a port. It is necessary but never sufficient: an instance
	// answering on its port has not necessarily rejoined replication.
	Ready bool
}

// Role is one StatefulSet's pods with the revision they are measured against.
type Role struct {
	// Replicas is how many pods the role must have. A pod that has been deleted
	// and not yet recreated is missing from Pods, and a role short of its
	// replicas is never acted on.
	Replicas int32

	// UpdateRevision is the StatefulSet's status.updateRevision — the revision
	// its current pod template hashes to. Empty while the StatefulSet has no
	// status yet, which reads as nothing to do rather than as everything being
	// outdated.
	UpdateRevision string

	Pods []Pod
}

// Decision is the one action a pass may take.
type Decision struct {
	Action Action

	// Pod is the pod to restart, set only for Delete, and carried whole rather
	// than by name. Its UID is what makes the delete conditional, and that UID has
	// to be the one this decision was made against: a caller that re-read the pod
	// by name to find it would get whichever pod exists by then — possibly the
	// replacement — so the precondition would always match and guard nothing.
	Pod Pod

	// Reason and Message describe a Wait or a Delete for the resource's
	// condition. Done needs neither: the caller reports its own converged
	// message.
	Reason  string
	Message string
}

// InProgress reports whether the role has pods still to restart. It is what
// lets the caller loosen its readiness gate by the one pod a restart took down,
// and only while one is actually under way.
func InProgress(role Role) bool {
	return len(outdated(role)) > 0
}

// Next returns the single action to take toward both roles running their
// StatefulSets' current pod template.
//
// Data instances are dealt with first and completely; coordinators only once no
// data pod is outstanding *and* the data plane is whole again, so that at most
// one pod of the cluster is ever down — across both roles, not per role. A role
// whose pods all carry the current revision contributes nothing, which is why a
// cluster with nothing to roll returns Done regardless of how healthy it is:
// readiness is the Ready and Converged conditions' business, not this one's.
func Next(
	data, coordinators Role,
	observed []memgraph.Instance,
	lag []memgraph.ReplicationLag,
) Decision {
	instances := index(observed)
	lags := indexLag(lag)

	if len(outdated(data)) > 0 {
		return nextDataInstance(data, instances, lags)
	}
	if len(outdated(coordinators)) == 0 {
		return Decision{Action: Done}
	}
	// A coordinator's pod does not go while a data pod is missing or unready: at
	// most one pod of the cluster is down at a time, across both roles.
	//
	// Replication lag deliberately does not gate this. A coordinator restart
	// neither reduces the number of instances holding recent writes nor forces a
	// promotion, so a replica still draining its backlog is no reason to hold it —
	// and making it one would let a single chronically lagging replica freeze the
	// coordinators' pod template indefinitely.
	if wait, ok := present(data); !ok {
		return wait
	}
	return nextCoordinator(coordinators, instances)
}

// nextDataInstance picks the next data pod to restart, or says what it is waiting
// for. Non-MAIN pods go first, highest ordinal down, matching the order a
// StatefulSet would have used; the MAIN goes last and alone.
func nextDataInstance(
	data Role,
	instances map[string]memgraph.Instance,
	lags map[string]memgraph.ReplicationLag,
) Decision {
	if wait, ok := present(data); !ok {
		return wait
	}

	// Where MAIN sits comes from the role Raft reports, not from health: an
	// unreachable MAIN is still the cluster's MAIN, and restarting another pod
	// while believing there is none is exactly the mistake to avoid.
	main := mainInstance(instances)
	if main == "" {
		return waiting(memgraphcomv1alpha1.ReasonNoMainElected,
			"Waiting for a MAIN data instance before restarting any data pod")
	}

	pending := outdated(data)
	if next, ok := highestOrdinalExcept(pending, main); ok {
		// Taking another replica down reduces the number of instances holding
		// recent writes, so the ones already restarted have to be back in
		// replication first. This is the gate that waits out a fresh volume's full
		// snapshot resync.
		if wait, ok := replicating(data, instances, lags); !ok {
			return wait
		}
		return restarting(next, fmt.Sprintf(
			"Restarting data instance pod %s, which is not MAIN", next.Name))
	}

	// Only the MAIN is left. Its restart costs a failover, so it is the one step
	// with a precondition of its own.
	mainPod := pending[0]
	if data.Replicas == 1 {
		// A single data instance has no replica to fail over to and never will,
		// so the precondition below can never be satisfied. Refusing would leave
		// its pod template frozen forever, which protects nothing: there is no
		// high availability here to preserve.
		return restarting(mainPod, fmt.Sprintf(
			"Restarting the only data instance pod %s, which interrupts the cluster until it is back", mainPod.Name))
	}
	if !instances[main].IsUp() {
		return waiting(memgraphcomv1alpha1.ReasonWorkloadsNotReady, fmt.Sprintf(
			"Waiting for MAIN data instance %s to be reachable before restarting it", main))
	}
	// A survivor that is reachable and holds every transaction the MAIN has
	// committed. One is enough because the coordinators promote the most
	// up-to-date instance they can reach, so whichever wins is at least as
	// current as the one proven here. Instances observed down are deliberately
	// not counted and deliberately not disqualifying: Raft cannot promote them,
	// and a permanently sick replica must not freeze the cluster's pod template.
	for _, pod := range data.Pods {
		if pod.Instance == main {
			continue
		}
		if instances[pod.Instance].IsUp() && lags[pod.Instance].IsCaughtUp() {
			return restarting(mainPod, fmt.Sprintf(
				"Restarting MAIN data instance pod %s last; %s is caught up and can be promoted in its place",
				mainPod.Name, pod.Instance))
		}
	}
	return waiting(memgraphcomv1alpha1.ReasonNoCaughtUpSurvivor, fmt.Sprintf(
		"Waiting for a data instance that is reachable and caught up with MAIN %s before restarting it; "+
			"the cluster keeps serving until one is", main))
}

// nextCoordinator picks the next coordinator pod to restart. Non-leaders go
// first, highest ordinal down, and the Raft leader last — its restart costs an
// election, which is harmless while the data plane has a MAIN, but there is no
// reason to pay it more than once.
func nextCoordinator(coordinators Role, instances map[string]memgraph.Instance) Decision {
	if wait, ok := present(coordinators); !ok {
		return wait
	}
	// A coordinator is proven back by the leader reaching it, which with three or
	// more coordinators and one pod down at a time is the quorum question itself.
	// Raft membership is no use here: it survives a pod restart untouched, so it
	// never reads as absent.
	for _, pod := range updated(coordinators) {
		if !instances[pod.Instance].IsUp() {
			return waiting(memgraphcomv1alpha1.ReasonWorkloadsNotReady, fmt.Sprintf(
				"Waiting for restarted coordinator %s to be reachable before restarting the next one", pod.Instance))
		}
	}

	leader := leaderInstance(instances)
	if leader == "" {
		return waiting(memgraphcomv1alpha1.ReasonNoCoordinatorLeader,
			"Waiting for the coordinators to elect a leader before restarting any coordinator pod")
	}

	pending := outdated(coordinators)
	if next, ok := highestOrdinalExcept(pending, leader); ok {
		return restarting(next, fmt.Sprintf(
			"Restarting coordinator pod %s, which does not hold Raft leadership", next.Name))
	}
	return restarting(pending[0], fmt.Sprintf(
		"Restarting coordinator pod %s last; it holds Raft leadership, so the surviving members elect a successor",
		pending[0].Name))
}

// replicating reports whether every data pod already carrying the current
// revision — which is exactly the set this roll has restarted — is back in
// replication: reachable by the coordinator leader, and holding every transaction
// the MAIN has committed.
//
// Pods still on the old revision are held to readiness alone on purpose. They have
// not been touched yet, so an instance that was already lagging before the roll
// began does not get to block it; the step that genuinely needs a caught-up
// instance asks for one directly, and asks for one rather than all.
//
// The restarted pods are not required to report role=replica, even though that is
// what they will normally be. A failover unrelated to the roll can move MAIN onto
// one of them, and demanding replica there would deadlock the roll against a
// perfectly healthy cluster. Reachable and caught up is the property that matters,
// and the MAIN reports itself caught up by definition.
func replicating(
	role Role,
	instances map[string]memgraph.Instance,
	lags map[string]memgraph.ReplicationLag,
) (Decision, bool) {
	for _, pod := range updated(role) {
		if !instances[pod.Instance].IsUp() {
			return waiting(memgraphcomv1alpha1.ReasonWorkloadsNotReady, fmt.Sprintf(
				"Waiting for restarted data instance %s to be reachable before restarting the next pod",
				pod.Instance)), false
		}
		if !lags[pod.Instance].IsCaughtUp() {
			return waiting(memgraphcomv1alpha1.ReasonWaitingForCatchUp, fmt.Sprintf(
				"Waiting for restarted data instance %s to catch up with MAIN before restarting the next pod",
				pod.Instance)), false
		}
	}
	return Decision{}, true
}

// present reports whether every pod of the role exists and is ready — the pod a
// previous pass deleted included, which is what serialises the restarts down to
// one at a time.
func present(role Role) (Decision, bool) {
	if int32(len(role.Pods)) != role.Replicas {
		return waiting(memgraphcomv1alpha1.ReasonWorkloadsNotReady,
			"Waiting for the pod restarted last to be recreated"), false
	}
	for _, pod := range role.Pods {
		if !pod.Ready {
			return waiting(memgraphcomv1alpha1.ReasonWorkloadsNotReady, fmt.Sprintf(
				"Waiting for pod %s to become ready", pod.Name)), false
		}
	}
	return Decision{}, true
}

// outdated are the role's pods not carrying its current revision, which is the
// work left to do.
//
// A StatefulSet without a status yet has no revision to compare against, and a
// pod without the label cannot be classified; both read as up to date. Guessing
// the other way would delete pods on the strength of a missing value.
func outdated(role Role) []Pod {
	if role.UpdateRevision == "" {
		return nil
	}
	var pending []Pod
	for _, pod := range role.Pods {
		if pod.RevisionHash != "" && pod.RevisionHash != role.UpdateRevision {
			pending = append(pending, pod)
		}
	}
	return pending
}

// updated are the role's pods already carrying its current revision — the ones
// this roll has restarted, once it is under way.
func updated(role Role) []Pod {
	if role.UpdateRevision == "" {
		return nil
	}
	var done []Pod
	for _, pod := range role.Pods {
		if pod.RevisionHash == role.UpdateRevision {
			done = append(done, pod)
		}
	}
	return done
}

// highestOrdinalExcept is the outstanding pod with the highest ordinal that does
// not run the named instance.
//
// The exclusion is the point: it is how "the MAIN last" and "the Raft leader
// last" are expressed. So is reporting false — that says the named instance is
// the only pod left to restart, which is the step both callers guard with
// preconditions the earlier ones do not need.
//
// Taking the highest ordinal is only a convention. Any deterministic order would
// be correct; this is the one a StatefulSet's own rolling update uses, so the
// restart sequence looks familiar and the tests can assert on it.
func highestOrdinalExcept(pending []Pod, instance string) (Pod, bool) {
	var next Pod
	found := false
	for _, pod := range pending {
		if pod.Instance == instance {
			continue
		}
		if !found || pod.Ordinal > next.Ordinal {
			next, found = pod, true
		}
	}
	return next, found
}

// mainInstance is the data instance Raft reports as MAIN, regardless of whether
// the coordinator leader can currently reach it, or empty when none is reported.
func mainInstance(instances map[string]memgraph.Instance) string {
	for name, instance := range instances {
		if instance.IsMain() {
			return name
		}
	}
	return ""
}

// leaderInstance is the coordinator reported as Raft leader, or empty when none
// is.
func leaderInstance(instances map[string]memgraph.Instance) string {
	for name, instance := range instances {
		if instance.IsLeader() {
			return name
		}
	}
	return ""
}

// create a map: instanceName -> instance
func index(observed []memgraph.Instance) map[string]memgraph.Instance {
	instances := make(map[string]memgraph.Instance, len(observed))
	for _, instance := range observed {
		instances[instance.Name] = instance
	}
	return instances
}

// indexLag keys replication lag by instance name. A name the view does not cover
// reads back as the zero value, which reports itself as not caught up — the safe
// answer for an instance nothing is known about, and the answer for every
// instance when there is no MAIN to measure against.
func indexLag(lag []memgraph.ReplicationLag) map[string]memgraph.ReplicationLag {
	lags := make(map[string]memgraph.ReplicationLag, len(lag))
	for _, instance := range lag {
		lags[instance.Instance] = instance
	}
	return lags
}

func waiting(reason, message string) Decision {
	return Decision{Action: Wait, Reason: reason, Message: message}
}

func restarting(pod Pod, message string) Decision {
	return Decision{
		Action:  Delete,
		Pod:     pod,
		Reason:  memgraphcomv1alpha1.ReasonRollingRestartInProgress,
		Message: message,
	}
}
