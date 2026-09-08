# Mutable topology: coordinator scale-down

**Type**: AFK

## Parent

`specs/operator-mvp/PRD.md`

## What to build

Shrink the coordinator count, which means removing Raft members — and Raft refuses to remove its own leader (`REMOVE COORDINATOR` returns `RAFT_CANNOT_REMOVE_LEADER`). Since a StatefulSet sheds only its highest ordinals, the leader may well sit on one of them, so the operator moves leadership out of the retiring range first. `YIELD LEADERSHIP` is the lever (`MemgraphCypher.g4:669`): it must be issued *on* the current leader, which is the connection the controller already holds, and it cannot name a successor — `yield_leadership()` is called with no successor argument (`raft_state.cpp:557`), so NuRaft picks. That makes it the one command whose outcome the planner cannot predict, so it is always emitted **last** in a plan and is terminal: the controller stops after it and requeues to re-observe under whichever coordinator won. Everything the planner could safely order before it still issues in that same pass. Retiring ordinals are `[spec.coordinators, liveStatefulSet.spec.replicas)`, and because coordinators must stay odd and at or above 3, a shrink always retires an even number of them.

Raft membership is given up before the pods are, so no removed member's pod outlives its vote. As in `15-data-instance-scale-down.md`, the smaller replica count is applied once the plan is empty, and the strict readiness gate still covers the retiring pods.

A coordinator removed from Raft keeps running and keeps its state on purpose, and this is what makes re-growing safe. On committing the config that drops it, NuRaft fires `RemovedFromCluster` and sets `steps_to_down_ = 2` (`handle_commit.cxx:725-750`); after two election timeouts it persists `allow_election_timer(false)` and cancels its schedulers (`handle_timeout.cxx:208-234`). It never calls `system_exit`, so the container does not die and the readiness gate is not tripped — it simply goes dormant, still serving Bolt, not campaigning. Two comments state the intent outright: the removal of self from the persisted config is deliberately disabled "for the next launch", and the persistent election-timer flag exists "for the case re-joining this replica to the original cluster". A later `ADD COORDINATOR` is accepted unconditionally by `handle_join_cluster_req` (`handle_join_leave.cxx:138-196`), which takes the leader's term, saves the new config and resets commit indices; with `snapshot_distance_ = 5` and `reserved_log_items_ = 5` (`raft_state.cpp:325-326`) the leader's log is compacted hard enough that a rejoiner almost always receives a snapshot install, replacing its state machine wholesale. A dormant server appends nothing, so its log is always a prefix of the leader's and cannot diverge. No PVC wipe, no removal bookkeeping and no re-add guard are needed; a re-added coordinator on a retained volume is in the same position as one whose pod crashed and stayed down. Verify this once by hand during this issue rather than gating it in CI.

The stale view a dormant coordinator would otherwise serve is already handled: `13-coordinator-leader-required.md` requires a named leader before any view is used, and a stepped-down coordinator reports none.

## Acceptance criteria

- [ ] Decreasing `coordinators` removes the retiring members from Raft and then shrinks the StatefulSet, in that order
- [ ] When the observed leader is retiring, the plan ends with `YIELD LEADERSHIP` and nothing after it; the following pass re-observes and removes under the new leader
- [ ] `REMOVE COORDINATOR` is never issued against the observed leader
- [ ] The StatefulSet is shrunk only after the plan is empty; a pass with pending commands never lowers the replica count
- [ ] `Converged` is False with reason `RetirementInProgress`, or `LeadershipTransferInProgress` while a yield is pending, until the pods are gone
- [ ] Planner unit tests: leader on a retiring ordinal, leader on a survivor, two coordinators retiring at once, an already-removed retiring member, a retiring coordinator alongside retiring data instances
- [ ] Retiring PVCs follow `spec.storage.retentionPolicy` on scale-down
- [ ] E2E: on the scaling cluster, leadership is forced onto `coordinator_4` via `YIELD LEADERSHIP`, then the count drops to 3 — leadership moves to a survivor, both retiring members leave the Raft cluster, and the cluster reaches `Converged`
- [ ] Manual verification recorded in this issue: shrink 5 to 3 and re-grow to 5 under `Retain`, confirming `SHOW INSTANCES` converges with the retained coordinator volumes

## Blocked by

- `15-data-instance-scale-down.md`
