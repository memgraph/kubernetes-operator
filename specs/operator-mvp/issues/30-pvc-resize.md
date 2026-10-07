# Grow claim sizes on a live cluster

**Type**: AFK

## Parent

`specs/operator-mvp/PRD.md` — storage, which `08-storage-configuration.md` made create-time only and PR #43 pinned field by field. Scoped in the 2026-10-05 interview.

## What to build

The three claim sizes — `storage.<role>.libPVCSize`, `storage.<role>.logPVCSize` while the log claim exists, `coreDumps.<role>.size` while the role collects dumps — may grow on a live cluster and never shrink. Their CEL transition rules move from `compareTo(...) == 0` to `>= 0`; every other claim-template field stays pinned.

Kubernetes forbids changing a live StatefulSet's `volumeClaimTemplates`, so the size goes to the claims and the StatefulSet is rebuilt around its pods, ECK's way. A new pure core, `internal/volumes`, decides one role's step per pass from its desired and live template sizes, its claims and its pods: patch every claim carrying the role's selector labels whose request is below the desired size, retained ones included; once none is left, delete the StatefulSet with `propagationPolicy: Orphan` and end the pass, so the next apply recreates it with the grown templates. Until then the apply restates the live sizes. The recreate restarts nothing: the StatefulSet controller adopts a pod whose volumes name the claims its ordinal gets (`storageMatches` compares names, never sizes), and the revision hashes the pod template alone. That is what tells a size change from adding or removing a template, which changes the pods' volumes and wedges the roll (2026-09-22).

While a StatefulSet is missing, `replicaCounts` takes its count from the highest pod ordinal plus one, so a count lowered in the window, or a retirement under way, is still carried out by retiring members first. No gate keeps resizes and scales apart.

No StorageClass pre-check: the API server's `PersistentVolumeClaimResize` admission refuses the patch on a class without `allowVolumeExpansion`, which is `Converged=False/VolumeExpansionRefused` naming the claim and blocks the recreate. `Converged` stays False (`VolumeExpansionInProgress`, naming each claim and its pod) until every mounted claim's `status.capacity` reaches its request; an unmounted claim counts once only its filesystem is left to grow. `ControllerResizeInfeasible`/`NodeResizeInfeasible` is `VolumeExpansionFailed`.

No pod is restarted for a resize. Decided after proving the mechanism on a real StatefulSet controller: `FileSystemResizePending` is transient under a running pod on every online driver (about a minute, until the kubelet's sync grows the filesystem), and an offline-only driver refuses to grow a volume any pod uses, which a restart does not change. `docs/storage-resize.md` lists the drivers and the stop-the-cluster procedure for them.

RBAC: `delete` on `statefulsets`, `get;list;watch;patch` on `persistentvolumeclaims`. The claim informer is scoped by `app.kubernetes.io/name=memgraph` (claims carry the StatefulSet's selector alone, no managed-by label), and a claim watch maps a claim to its cluster by labels. No admission policy narrows the StatefulSet delete.

## Acceptance criteria

- [ ] Admission accepts growing all three sizes on both roles, a size respelled in other units, and refuses shrinking each; the other claim-template pins are unchanged
- [ ] `internal/volumes` table tests: patch every claim below the size, retained included; recreate only once nothing is left to patch and only for a grown template; unbound and foreign claims ignored; mounted claims waited on with their pod named; unmounted claims done once only the filesystem is left; infeasible reported; `Observe` parses claim names strictly
- [ ] Envtest: claims patched with the StatefulSet left at its live size; orphan delete with the `orphan` finalizer; nothing applied while the StatefulSet terminates; recreate at the grown size and at the count its pods run although the spec was lowered meanwhile; `VolumeExpansionRefused` on a class without expansion with the StatefulSet untouched; `VolumeExpansionFailed`; a pending filesystem resize restarts nothing and names its pod; `Converged` True only once mounted claims hold the size
- [ ] E2E on `hack/kind-csi-hostpath.sh` (CSI hostpath, standard deployment — the distributed one ships no resizer): growing lib, log and core dumps on both roles changes no pod UID and no claim UID, recreates both StatefulSets, and under `Delete` the claims survive and are owned by the new StatefulSets; under `Retain` a claim retained by a scale-down is grown, a re-grow mounts it at the new size, and a claim the re-grow adds starts at it
- [ ] `make manifests generate chart-sync` regenerated, `make chart-verify` green
- [ ] `docs/storage-resize.md`, the README, the sample and `CLAUDE.md` describe it

## Blocked by

- `08-storage-configuration.md`
- `17-sequenced-rolling-restart.md`
