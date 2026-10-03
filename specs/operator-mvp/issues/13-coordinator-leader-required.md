# Require a coordinator leader before acting

**Type**: AFK

## Parent

`specs/operator-mvp/PRD.md`

## What to build

Close a stale-view hole in `observeCluster`: it currently treats a coordinator that reports no leader as the fresh-cluster bootstrap case and uses that coordinator's `SHOW INSTANCES` view as the planning input. The premise is wrong. A fresh coordinator's initial Raft config contains exactly itself (`coordinator_state_manager.cpp:133-134`) and it is started as the leader of that one-member cluster (`raft_state.cpp:377-380` — "By setting it to false, all coordinators are started as leaders"), and the role column is derived from `GetLeaderId()` on the leader *and* follower paths alike (`coordinator_instance.cpp:227-228`). So a fresh coordinator always names itself leader, and an absent leader means `GetLeaderId() == -1`: quorum lost, or a coordinator that stepped down or was removed from the Raft cluster. Such a coordinator answers from its own stale state machine (`ShowInstancesStatusAsFollower`), and every mutating query the operator could issue needs `LEADER_READY` or forwards to a leader — so acting on that view plans against a cluster state that is not current and cannot be written to anyway.

Use a coordinator's view only when it names a leader: itself, or another coordinator the operator then redirects to. Skip coordinators that name none, and when no coordinator names one, issue nothing and requeue. Report it as its own reason, `NoCoordinatorLeader`, rather than folding it into `CoordinatorUnreachable` — reachable coordinators without quorum and unreachable pods have different remedies. Widen the leader-by-name lookup so a reported leader outside the declared coordinator set resolves instead of failing, which also removes the "reported leader X, which is not declared" dead end and is the prerequisite for `16-coordinator-scale-down.md`, where the leader may be a coordinator on its way out.

The fallback has been masking an unfaithful test seam, so `fakeMemgraph` is reworked with it. Today a fresh cluster is modelled as an empty `SHOW INSTANCES` and `ADD COORDINATOR` appends a row with role `follower`, so even a fully bootstrapped fake cluster has no leader at all, and both bootstrap tests pass only because the fallback swallows it — meaning the planner's empty-`bolt_server` rule (`planner.go:104-109`) is never exercised in the controller tests. The fake must serve a fresh coordinator's own row with role `leader` and an empty `bolt_server`, and `ADD COORDINATOR` must fill that server in rather than invent a row.

## Acceptance criteria

- [ ] A coordinator view naming no leader is never used as planning input; the next coordinator is tried instead
- [ ] When no coordinator names a leader, no command is issued, `Ready` and `Converged` go False with reason `NoCoordinatorLeader`, and the reconcile requeues
- [ ] A leader named outside the declared coordinator set is resolved and used, not rejected
- [ ] `fakeMemgraph` serves a fresh coordinator's own row as `leader` with an empty `bolt_server`, and `ADD COORDINATOR` fills the bolt server in
- [ ] Bootstrap envtest cases pass against the faithful fake, exercising the empty-`bolt_server` rule end to end
- [ ] Envtest coverage: coordinators reporting no leader (stale follower view) produce zero commands
- [ ] The comment claiming a fresh cluster has no leader because Raft is not yet formed is corrected

## Blocked by

- `05-continuous-re-registration.md`
- `06-status-and-conditions.md`
