# Mutable topology: scale-up

**Type**: AFK

## Parent

`specs/operator-mvp/PRD.md`

## What to build

Make both replica counts mutable and support growing a live cluster, reversing the contract `07-cel-immutability-validation.md` established. The CEL immutability rules come off `coordinators` and `dataInstances`; the floors replace them: coordinators get `Minimum=3` (a hard floor at creation as well as on update — this operator builds real HA clusters) with the existing odd rule retained, data instances keep `Minimum=1`. Nothing else constrains a change: any odd coordinator target ≥ 3, any data target ≥ 1, both roles changeable in one edit, any step size. Bootstrap already issues three `ADD COORDINATOR`s back to back, so sequential Raft config changes are proven; a transient Raft rejection is a retryable error the next reconcile clears.

Growth itself needs almost nothing new — the planner's existing diff already emits `ADD COORDINATOR` and `REGISTER INSTANCE` for declared members it does not observe. What this issue builds is the structure the shrink slices then hang off, so the API is cut once. Resource builders take the replica count as an argument (`CoordinatorStatefulSet(cluster, replicas)`), keeping them pure while the controller decides the count: `declared` when growing or unchanged, and deliberately `current` when `declared < current`, so this issue can never shrink a StatefulSet. `planner.Topology` gains `RetiringCoordinators` and `RetiringDataInstances`, empty here and populated by 15 and 16. The promotion rule is tightened while it is being touched: prefer the lowest-ordinal declared instance observed `up`, falling back to `declared[0]`, which also closes the bootstrap hole where `SET INSTANCE TO MAIN` against a down instance only writes Raft state and leaves the cluster MAIN-less until the coordinators retry.

Observability and storage semantics move with the API. `status.coordinators` and `status.dataInstances` publish the registered counts as observed (pure observation — status stays out of reconcile input), surfaced as `priority=1` print columns so the default table stays narrow. `Converged` widens from "registration matches the declared topology" to "registration matches *and* both StatefulSets' replicas equal the declared counts", so `kubectl wait --for=condition=Converged` means the scale is genuinely finished. `persistentVolumeClaimRetentionPolicy.whenScaled` stops being hard-wired `Retain` and follows `spec.storage.retentionPolicy`, one knob meaning one thing; the comment at `statefulset.go:381-386` claiming nothing ever scales down is deleted.

Collateral: the retention e2e declares `coordinators: 1`, now rejected, so it moves to 3 and its PVC assertion moves from 4 to 8; the envtest immutability cases invert to acceptance and gain floor rejections; `make manifests generate chart-sync` regenerates the CRDs and the chart's copy; README's scaling limitation section is rewritten.

## Acceptance criteria

- [ ] Increasing either count on a live `MemgraphCluster` is accepted and the new members are registered without human action
- [ ] `coordinators` below 3 or even is rejected at creation and on update; `dataInstances` below 1 is rejected
- [ ] Resource builders remain pure, taking the replica count as an argument; unit tests cover the count the controller derives, including that a lower declared count never shrinks the applied StatefulSet
- [ ] A MAIN promotion targets the lowest-ordinal declared instance observed `up`, with `declared[0]` as fallback; planner unit tests cover a down `instance_0`
- [ ] `status.coordinators` and `status.dataInstances` report observed registered counts and appear as `priority=1` print columns
- [ ] `Converged` is False while a StatefulSet's replicas differ from the declared count, True once registration and both replica counts match
- [ ] `whenScaled` follows `spec.storage.retentionPolicy`; builder tests cover both values
- [ ] E2E: a dedicated scaling cluster in its own namespace (`createLogStorageClaim: false`, explicit small resource requests, teardown awaited) grows 3/2 to 5/3 and reaches `Converged` with every new member registered
- [ ] Retention e2e updated for the coordinator floor; chart CRDs regenerated and `make chart-verify` green

## Blocked by

- `13-coordinator-leader-required.md`
