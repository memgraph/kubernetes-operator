# Mutable topology: data-instance scale-down

**Type**: AFK

## Parent

`specs/operator-mvp/PRD.md`

## What to build

Shrink the data-instance count, which means unregistering members Memgraph will not let go of while one of them is MAIN (`UNREGISTER INSTANCE` fails with `IS_MAIN`). A StatefulSet sheds only its highest ordinals — `spec.ordinals.start` slides the window but never removes a member from the middle — so the operator never chooses a victim: the retiring set is always the top ordinals, and the job is to make them safe to remove. Retiring ordinals are `[spec.dataInstances, liveStatefulSet.spec.replicas)`, derived from the operator's own prior apply, which bounds the range exactly and keeps the guarantee that an instance a human registered is never touched.

The sequence runs inside one reconcile pass against one leader connection, because the planner can predict every intermediate state. `DEMOTE INSTANCE` is emitted only when a retiring instance is observed as MAIN, and it deliberately leaves the cluster MAIN-less: Memgraph triggers automatic failover only on a coordinator leadership change with zero MAINs or when the current MAIN fails its ping (`coordinator_instance.cpp:496`, `:1341`), so a manual demote hands the promotion choice back to the operator, which promotes the lowest-ordinal surviving instance observed `up` through the rule `14-topology-scale-up.md` already installed. `UNREGISTER INSTANCE` then removes each retiring member. Replicas are SYNC — `REGISTER INSTANCE` is issued without `AS ASYNC` or `AS STRICT_SYNC` — so promoting a survivor after a clean demote is not a data-loss gamble, and the MAIN-less window is the few milliseconds between two queries in the same pass.

Unregistration happens before the pods go, so the coordinators never see a registered instance disappear. The smaller replica count is applied in exactly one place: at the end of the registration phase, once the plan is empty. That keeps the pre-apply replica rule from `14` free of any cluster knowledge — it never shrinks — and makes the shrink converge across two passes without a second observation source. The readiness gate stays strict: every pod of the held-at-current StatefulSet must be ready, retiring pods included, so a retiring pod that cannot become ready blocks its own removal and the resource says so rather than acting on a half-known cluster. That is a deliberate trade and belongs in the docs.

Errors are not special-cased. Read-before-write means `ALREADY_REPLICA` or `NO_INSTANCE_WITH_NAME` only appear when something raced the operator; they surface, the reconcile retries, and the plan is recomputed from a fresh observation.

## Acceptance criteria

- [ ] Decreasing `dataInstances` unregisters the retiring instances and then shrinks the StatefulSet, in that order
- [ ] A retiring instance observed as MAIN is demoted and a surviving instance is promoted before it is unregistered; no `UNREGISTER INSTANCE` is ever issued against an observed MAIN
- [ ] Demote, promote and unregister issue within a single reconcile pass against one leader connection
- [ ] The StatefulSet is shrunk only after the plan is empty; a pass with pending commands never lowers the replica count
- [ ] Instances the cluster knows but the spec never declared and that fall outside the retiring range are left untouched
- [ ] `Converged` is False with reason `RetirementInProgress`, naming the retiring instances, until the pods are gone
- [ ] Planner unit tests: MAIN on a retiring ordinal, MAIN on a survivor, several retiring at once, shrink to a single instance, already-unregistered retiring member, mixed grow-and-shrink across roles
- [ ] Retiring PVCs follow `spec.storage.retentionPolicy` on scale-down
- [ ] E2E: on the scaling cluster, `instance_2` is deliberately made MAIN, then the count drops to 2 — MAIN moves to a survivor, `instance_2` is unregistered before its pod terminates, and the cluster reaches `Converged`
- [ ] Docs state that a retiring pod which cannot become ready blocks its own removal

## Blocked by

- `14-topology-scale-up.md`
