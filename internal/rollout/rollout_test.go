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

package rollout

import (
	"fmt"
	"testing"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
	"github.com/memgraph/kubernetes-operator/internal/memgraph"
)

const (
	oldRevision = "cluster-data-6c9f8b7d5"
	newRevision = "cluster-data-77b4c8f9d"

	// The pods the cases below expect to be restarted, named once so a changed
	// expectation cannot silently pass against a typo.
	dataPod0        = "cluster-data-0"
	dataPod1        = "cluster-data-1"
	dataPod2        = "cluster-data-2"
	coordinatorPod0 = "cluster-coordinator-0"
	coordinatorPod1 = "cluster-coordinator-1"
	coordinatorPod2 = "cluster-coordinator-2"
)

// dataRole builds a data role of the given size whose pods carry the given
// revisions, one per ordinal, all ready. A revision equal to newRevision is a pod
// this roll has already restarted.
func dataRole(revisions ...string) Role {
	role := Role{Replicas: int32(len(revisions)), UpdateRevision: newRevision}
	for ordinal, revision := range revisions {
		role.Pods = append(role.Pods, Pod{
			Name:         fmt.Sprintf("cluster-data-%d", ordinal),
			UID:          fmt.Sprintf("uid-data-%d", ordinal),
			Instance:     fmt.Sprintf("instance_%d", ordinal),
			Ordinal:      int32(ordinal),
			RevisionHash: revision,
			Ready:        true,
		})
	}
	return role
}

// coordinatorRole is dataRole for the coordinator StatefulSet, whose instances
// are named from a 1-based Raft ID.
func coordinatorRole(revisions ...string) Role {
	role := Role{Replicas: int32(len(revisions)), UpdateRevision: newRevision}
	for ordinal, revision := range revisions {
		role.Pods = append(role.Pods, Pod{
			Name:         fmt.Sprintf("cluster-coordinator-%d", ordinal),
			UID:          fmt.Sprintf("uid-coordinator-%d", ordinal),
			Instance:     fmt.Sprintf("coordinator_%d", ordinal+1),
			Ordinal:      int32(ordinal),
			RevisionHash: revision,
			Ready:        true,
		})
	}
	return role
}

// converged is a role whose every pod already runs the current revision.
func converged(role Role) Role {
	for i := range role.Pods {
		role.Pods[i].RevisionHash = role.UpdateRevision
	}
	return role
}

// cluster is a SHOW INSTANCES view: the named data instance is MAIN, every other
// declared one is a replica, the first coordinator leads, and everything is up.
func cluster(dataInstances, coordinators int, main string) []memgraph.Instance {
	view := make([]memgraph.Instance, 0, dataInstances+coordinators)
	for ordinal := range dataInstances {
		name := fmt.Sprintf("instance_%d", ordinal)
		role := memgraph.RoleReplica
		if name == main {
			role = memgraph.RoleMain
		}
		view = append(view, memgraph.Instance{Name: name, Health: memgraph.HealthUp, Role: role})
	}
	for ordinal := range coordinators {
		role := memgraph.RoleFollower
		if ordinal == 0 {
			role = memgraph.RoleLeader
		}
		view = append(view, memgraph.Instance{
			Name:       fmt.Sprintf("coordinator_%d", ordinal+1),
			BoltServer: fmt.Sprintf("coordinator:%d", memgraphcomv1alpha1.BoltPort),
			Health:     memgraph.HealthUp,
			Role:       role,
		})
	}
	return view
}

// down marks the named instance as one the coordinator leader cannot reach,
// leaving the role it is registered with intact — which is what Memgraph reports
// for an instance whose pod is gone.
func down(view []memgraph.Instance, name string) []memgraph.Instance {
	out := make([]memgraph.Instance, len(view))
	copy(out, view)
	for i := range out {
		if out[i].Name == name {
			out[i].Health = "down"
		}
	}
	return out
}

// caughtUp is the replication lag view with every named instance holding all of
// the MAIN's transactions.
func caughtUp(names ...string) []memgraph.ReplicationLag {
	lag := make([]memgraph.ReplicationLag, 0, len(names))
	for _, name := range names {
		lag = append(lag, memgraph.ReplicationLag{
			Instance:  name,
			Databases: []memgraph.DatabaseLag{{Database: "memgraph", CommittedTxns: 42, TxnsBehindMain: 0}},
		})
	}
	return lag
}

// behind is caughtUp for an instance that is missing transactions.
func behind(name string) memgraph.ReplicationLag {
	return memgraph.ReplicationLag{
		Instance:  name,
		Databases: []memgraph.DatabaseLag{{Database: "memgraph", CommittedTxns: 40, TxnsBehindMain: 2}},
	}
}

func TestNothingToRestart(t *testing.T) {
	data := converged(dataRole(newRevision, newRevision, newRevision))
	coordinators := converged(coordinatorRole(newRevision, newRevision, newRevision))

	decision := Next(data, coordinators,
		cluster(3, 3, "instance_0"), caughtUp("instance_0", "instance_1", "instance_2"))

	if decision.Action != Done {
		t.Fatalf("expected Done for a cluster already on the current revision, got %+v", decision)
	}
}

// A StatefulSet with no status yet has no revision to measure pods against.
// Reading that as "every pod is outdated" would delete pods on the strength of a
// missing value.
func TestRoleWithoutRevisionIsLeftAlone(t *testing.T) {
	data := dataRole(oldRevision, oldRevision)
	data.UpdateRevision = ""
	coordinators := coordinatorRole(oldRevision, oldRevision, oldRevision)
	coordinators.UpdateRevision = ""

	if decision := Next(data, coordinators, cluster(2, 3, "instance_0"), nil); decision.Action != Done {
		t.Fatalf("expected Done while no revision is known, got %+v", decision)
	}
	if InProgress(data) {
		t.Error("a role without an update revision has no restart in progress")
	}
}

func TestDataInstancesRestartHighestOrdinalFirstAndSkipMain(t *testing.T) {
	// MAIN sits in the middle on purpose: a StatefulSet's own rolling update
	// would take instance_2, then instance_1 — the MAIN — then instance_0.
	data := dataRole(oldRevision, oldRevision, oldRevision)
	coordinators := converged(coordinatorRole(newRevision, newRevision, newRevision))
	view := cluster(3, 3, "instance_1")
	lag := caughtUp("instance_0", "instance_1", "instance_2")

	decision := Next(data, coordinators, view, lag)
	if decision.Action != Delete || decision.Pod.Name != dataPod2 {
		t.Fatalf("expected the highest non-MAIN ordinal first, got %+v", decision)
	}

	// instance_2 restarted and caught up; instance_1 is MAIN, so instance_0 is next.
	data.Pods[2].RevisionHash = newRevision
	decision = Next(data, coordinators, view, lag)
	if decision.Action != Delete || decision.Pod.Name != dataPod0 {
		t.Fatalf("expected MAIN to be skipped for the lower ordinal, got %+v", decision)
	}

	// Only the MAIN is left.
	data.Pods[0].RevisionHash = newRevision
	decision = Next(data, coordinators, view, lag)
	if decision.Action != Delete || decision.Pod.Name != dataPod1 {
		t.Fatalf("expected the MAIN's pod last, got %+v", decision)
	}
	if decision.Pod.UID != "uid-data-1" {
		t.Errorf("expected the observed UID to be carried for a conditional delete, got %q", decision.Pod.UID)
	}
}

func TestRestartedInstanceMustBeReadyBeforeTheNextGoes(t *testing.T) {
	data := dataRole(oldRevision, oldRevision, newRevision)
	data.Pods[2].Ready = false
	coordinators := converged(coordinatorRole(newRevision, newRevision, newRevision))

	decision := Next(data, coordinators, cluster(3, 3, "instance_0"),
		caughtUp("instance_0", "instance_1", "instance_2"))

	if decision.Action != Wait || decision.Reason != memgraphcomv1alpha1.ReasonWorkloadsNotReady {
		t.Fatalf("expected to wait on the unready pod, got %+v", decision)
	}
}

// A pod deleted and not yet recreated is absent from the role, and the restart
// waits for it rather than treating one fewer pod as one fewer thing to check.
func TestMissingPodStopsTheRoll(t *testing.T) {
	data := dataRole(oldRevision, oldRevision, newRevision)
	data.Pods = data.Pods[:2]
	coordinators := converged(coordinatorRole(newRevision, newRevision, newRevision))

	decision := Next(data, coordinators, cluster(3, 3, "instance_0"), caughtUp("instance_0", "instance_1"))

	if decision.Action != Wait || decision.Reason != memgraphcomv1alpha1.ReasonWorkloadsNotReady {
		t.Fatalf("expected to wait for the deleted pod to be recreated, got %+v", decision)
	}
}

func TestRestartedInstanceMustBeReachableAndCaughtUp(t *testing.T) {
	data := dataRole(oldRevision, oldRevision, newRevision)
	coordinators := converged(coordinatorRole(newRevision, newRevision, newRevision))
	view := cluster(3, 3, "instance_0")

	// Ready, but the coordinator leader does not reach it yet.
	decision := Next(data, coordinators, down(view, "instance_2"), caughtUp("instance_0", "instance_2"))
	if decision.Action != Wait || decision.Reason != memgraphcomv1alpha1.ReasonWorkloadsNotReady {
		t.Fatalf("expected to wait for the restarted instance to be reachable, got %+v", decision)
	}

	// Reachable, but still draining its backlog — the fresh-volume resync case.
	decision = Next(data, coordinators, view, append(caughtUp("instance_0"), behind("instance_2")))
	if decision.Action != Wait || decision.Reason != memgraphcomv1alpha1.ReasonWaitingForCatchUp {
		t.Fatalf("expected to wait for the restarted instance to catch up, got %+v", decision)
	}

	// An empty lag view means nothing is known, which is not permission to proceed.
	decision = Next(data, coordinators, view, nil)
	if decision.Action != Wait || decision.Reason != memgraphcomv1alpha1.ReasonWaitingForCatchUp {
		t.Fatalf("expected an unknown lag to read as not caught up, got %+v", decision)
	}
}

// An instance that was already lagging before the roll began must not block it:
// it has not been restarted, so it is held to readiness alone. The one step that
// genuinely needs a caught-up instance asks for one directly.
func TestNotYetRestartedInstanceIsNotHeldToLag(t *testing.T) {
	data := dataRole(oldRevision, oldRevision, oldRevision)
	coordinators := converged(coordinatorRole(newRevision, newRevision, newRevision))

	decision := Next(data, coordinators, cluster(3, 3, "instance_0"),
		append(caughtUp("instance_0"), behind("instance_1")))

	if decision.Action != Delete || decision.Pod.Name != dataPod2 {
		t.Fatalf("expected a lagging untouched instance not to block the roll, got %+v", decision)
	}
}

func TestMainIsNotRestartedWithoutACaughtUpSurvivor(t *testing.T) {
	data := dataRole(newRevision, newRevision, oldRevision)
	data.Pods[2].Instance = "instance_2"
	coordinators := converged(coordinatorRole(newRevision, newRevision, newRevision))
	view := cluster(3, 3, "instance_2")

	// Both survivors are behind: the cluster keeps serving and the roll parks.
	decision := Next(data, coordinators, view,
		[]memgraph.ReplicationLag{behind("instance_0"), behind("instance_1")})
	if decision.Action != Wait || decision.Reason != memgraphcomv1alpha1.ReasonNoCaughtUpSurvivor {
		t.Fatalf("expected to park at the MAIN without a caught-up survivor, got %+v", decision)
	}

	// One caught-up survivor is enough: the coordinators promote the most
	// up-to-date instance they can reach, so whoever wins is at least as current.
	decision = Next(data, coordinators, view, append(caughtUp("instance_1"), behind("instance_0")))
	if decision.Action != Delete || decision.Pod.Name != dataPod2 {
		t.Fatalf("expected one caught-up survivor to permit the MAIN's restart, got %+v", decision)
	}
}

// A caught-up instance the leader cannot reach is not a promotion candidate: Raft
// cannot promote what it cannot see. Its registration outliving its reachability
// is exactly why health and lag are both asked, and why lag alone is never enough.
func TestUnreachableSurvivorIsNoSurvivor(t *testing.T) {
	data := dataRole(newRevision, oldRevision)
	coordinators := converged(coordinatorRole(newRevision, newRevision, newRevision))
	view := down(cluster(2, 3, "instance_1"), "instance_0")

	decision := Next(data, coordinators, view, caughtUp("instance_0", "instance_1"))

	if decision.Action != Wait || decision.Reason != memgraphcomv1alpha1.ReasonNoCaughtUpSurvivor {
		t.Fatalf("expected the MAIN to park without a reachable survivor, got %+v", decision)
	}
}

// One chronically lagging replica must not be able to freeze the cluster's pod
// template. The MAIN's restart needs one survivor Raft could promote, not every
// survivor, so a replica that never catches up does not block it.
func TestOneLaggingReplicaDoesNotBlockTheMain(t *testing.T) {
	data := dataRole(newRevision, newRevision, oldRevision)
	coordinators := converged(coordinatorRole(newRevision, newRevision, newRevision))

	decision := Next(data, coordinators, cluster(3, 3, "instance_2"),
		append(caughtUp("instance_1"), behind("instance_0")))

	if decision.Action != Delete || decision.Pod.Name != dataPod2 {
		t.Fatalf("expected one caught-up survivor to be enough despite a lagging one, got %+v", decision)
	}
}

// A single data instance has no replica and never will, so the MAIN's
// precondition can never be met. Refusing would freeze its pod template forever
// and protect nothing.
func TestSingleDataInstanceIsRestartedWithAcknowledgedDowntime(t *testing.T) {
	data := dataRole(oldRevision)
	coordinators := converged(coordinatorRole(newRevision, newRevision, newRevision))

	decision := Next(data, coordinators, cluster(1, 3, "instance_0"), caughtUp("instance_0"))

	if decision.Action != Delete || decision.Pod.Name != dataPod0 {
		t.Fatalf("expected the only data instance to be restarted, got %+v", decision)
	}
	if !containsAll(decision.Message, "only data instance", "interrupts") {
		t.Errorf("expected the message to name the interruption, got %q", decision.Message)
	}
}

// Without a MAIN there is nothing to measure lag against and no telling what the
// cluster is doing, so no data pod is taken down.
func TestNoMainStopsTheDataRoll(t *testing.T) {
	data := dataRole(oldRevision, oldRevision)
	coordinators := converged(coordinatorRole(newRevision, newRevision, newRevision))
	view := cluster(2, 3, "")

	decision := Next(data, coordinators, view, nil)

	if decision.Action != Wait || decision.Reason != memgraphcomv1alpha1.ReasonNoMainElected {
		t.Fatalf("expected to wait for a MAIN before restarting data pods, got %+v", decision)
	}
}

// An unreachable MAIN keeps its role in Raft, so it is still found — but it is not
// restarted while the cluster cannot serve from it.
func TestUnreachableMainIsNotRestarted(t *testing.T) {
	data := dataRole(newRevision, oldRevision)
	coordinators := converged(coordinatorRole(newRevision, newRevision, newRevision))
	view := down(cluster(2, 3, "instance_1"), "instance_1")

	decision := Next(data, coordinators, view, caughtUp("instance_0"))

	if decision.Action != Wait || decision.Reason != memgraphcomv1alpha1.ReasonWorkloadsNotReady {
		t.Fatalf("expected an unreachable MAIN not to be restarted, got %+v", decision)
	}
}

func TestCoordinatorsWaitForEveryDataPod(t *testing.T) {
	data := converged(dataRole(newRevision, newRevision))
	coordinators := coordinatorRole(oldRevision, oldRevision, oldRevision)
	lag := caughtUp("instance_0", "instance_1")

	// A data pod is still coming back. One pod of the cluster is down at a time
	// across both roles, so no coordinator goes on top of it.
	unready := converged(dataRole(newRevision, newRevision))
	unready.Pods[1].Ready = false
	decision := Next(unready, coordinators, cluster(2, 3, "instance_0"), lag)
	if decision.Action != Wait || decision.Reason != memgraphcomv1alpha1.ReasonWorkloadsNotReady {
		t.Fatalf("expected coordinators to wait for every data pod to be ready, got %+v", decision)
	}

	// Replication lag, by contrast, does not gate a coordinator restart: it neither
	// reduces the instances holding recent writes nor forces a promotion. Making it
	// a gate would let one lagging replica freeze the coordinators' template.
	decision = Next(data, coordinators, cluster(2, 3, "instance_0"),
		append(caughtUp("instance_0"), behind("instance_1")))
	if decision.Action != Delete || decision.Pod.Name != coordinatorPod2 {
		t.Fatalf("expected a lagging replica not to block the coordinator roll, got %+v", decision)
	}

	// Healthy: the coordinator roll starts, highest ordinal first, leader excluded.
	decision = Next(data, coordinators, cluster(2, 3, "instance_0"), lag)
	if decision.Action != Delete || decision.Pod.Name != coordinatorPod2 {
		t.Fatalf("expected the highest non-leader coordinator first, got %+v", decision)
	}
}

func TestCoordinatorLeaderIsRestartedLast(t *testing.T) {
	data := converged(dataRole(newRevision, newRevision))
	coordinators := coordinatorRole(oldRevision, oldRevision, oldRevision)
	view := cluster(2, 3, "instance_0")
	lag := caughtUp("instance_0", "instance_1")

	// coordinator_1, on ordinal 0, is the leader.
	coordinators.Pods[2].RevisionHash = newRevision
	decision := Next(data, coordinators, view, lag)
	if decision.Action != Delete || decision.Pod.Name != coordinatorPod1 {
		t.Fatalf("expected the leader to be skipped, got %+v", decision)
	}

	coordinators.Pods[1].RevisionHash = newRevision
	decision = Next(data, coordinators, view, lag)
	if decision.Action != Delete || decision.Pod.Name != coordinatorPod0 {
		t.Fatalf("expected the leader's pod last, got %+v", decision)
	}
}

// Raft membership survives a pod restart untouched, so reachability is what proves
// a coordinator is back — and it is asked before the next one goes.
func TestRestartedCoordinatorMustBeReachable(t *testing.T) {
	data := converged(dataRole(newRevision, newRevision))
	coordinators := coordinatorRole(oldRevision, oldRevision, newRevision)
	view := down(cluster(2, 3, "instance_0"), "coordinator_3")

	decision := Next(data, coordinators, view, caughtUp("instance_0", "instance_1"))

	if decision.Action != Wait || decision.Reason != memgraphcomv1alpha1.ReasonWorkloadsNotReady {
		t.Fatalf("expected to wait for the restarted coordinator, got %+v", decision)
	}
}

func TestNoCoordinatorLeaderStopsTheCoordinatorRoll(t *testing.T) {
	data := converged(dataRole(newRevision, newRevision))
	coordinators := coordinatorRole(oldRevision, oldRevision, oldRevision)
	view := cluster(2, 0, "instance_0")
	for ordinal := range 3 {
		view = append(view, memgraph.Instance{
			Name:       fmt.Sprintf("coordinator_%d", ordinal+1),
			BoltServer: fmt.Sprintf("coordinator:%d", memgraphcomv1alpha1.BoltPort),
			Health:     memgraph.HealthUp,
			Role:       memgraph.RoleFollower,
		})
	}

	decision := Next(data, coordinators, view, caughtUp("instance_0", "instance_1"))

	if decision.Action != Wait || decision.Reason != memgraphcomv1alpha1.ReasonNoCoordinatorLeader {
		t.Fatalf("expected to wait for a Raft leader, got %+v", decision)
	}
}

// A spec reverted halfway through inverts which pods are outdated, and the roll
// walks back with nothing to unwind — the point of deriving the state every pass
// rather than tracking it.
func TestRevertedSpecRollsBack(t *testing.T) {
	data := dataRole(newRevision, newRevision, oldRevision)
	coordinators := converged(coordinatorRole(newRevision, newRevision, newRevision))

	// The spec goes back: the old revision is now the current one, so the two pods
	// already restarted are the outdated ones.
	data.UpdateRevision = oldRevision
	coordinators.UpdateRevision = oldRevision
	for i := range coordinators.Pods {
		coordinators.Pods[i].RevisionHash = oldRevision
	}

	decision := Next(data, coordinators, cluster(3, 3, "instance_0"),
		caughtUp("instance_0", "instance_1", "instance_2"))

	if decision.Action != Delete || decision.Pod.Name != dataPod1 {
		t.Fatalf("expected the roll to reverse onto the highest re-outdated non-MAIN pod, got %+v", decision)
	}
}

// A failover unrelated to the roll can move MAIN onto a pod already restarted.
// Demanding role=replica of the restarted pods would deadlock against a perfectly
// healthy cluster, so reachable and caught up is what is asked.
func TestMainMovingOntoARestartedPodDoesNotDeadlock(t *testing.T) {
	data := dataRole(newRevision, oldRevision, oldRevision)
	coordinators := converged(coordinatorRole(newRevision, newRevision, newRevision))

	decision := Next(data, coordinators, cluster(3, 3, "instance_0"),
		caughtUp("instance_0", "instance_1", "instance_2"))

	if decision.Action != Delete || decision.Pod.Name != dataPod2 {
		t.Fatalf("expected the roll to continue with MAIN on a restarted pod, got %+v", decision)
	}
}

func containsAll(s string, substrings ...string) bool {
	for _, substring := range substrings {
		found := false
		for i := 0; i+len(substring) <= len(s); i++ {
			if s[i:i+len(substring)] == substring {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
