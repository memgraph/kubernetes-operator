# sysctl init container for vm.max_map_count

**Type**: AFK

## Parent

`specs/operator-mvp/PRD.md` — chart parity. The `memgraph-high-availability` chart's `sysctlInitContainer` block was the one pod-level knob of the chart the operator did not carry; scoped on 2026-09-28.

## What to build

`spec.sysctlInitContainer`, the chart's block under the chart's name with its image knobs and its `enabled` dropped: one field, `maxMapCount`. The block is presence-based like every other optional block of the resource — present, the container runs; absent, it does not — which is the one place this departs from the chart's default-on. Decided on 2026-09-28: the API cannot both default on and treat an absent block as off, and the operator's convention is that absence means off; the sample carries the block written out instead. The quickstart does not: every e2e cluster namespace enforces the restricted Pod Security Standard and the suite applies the quickstart verbatim, so a privileged container there is rejected — the first CI run proved exactly that. The quickstart shows the block commented out, and a dedicated e2e scenario in an unlabeled namespace proves both privileged init containers. What the block buys is what Memgraph expects: it checks `vm.max_map_count` at startup and prints "Max virtual memory areas vm.max_map_count ... is too low" below its floor, and under load too few map areas surface as a crash on `bad_alloc` or `munmap`.

Every pod of both roles runs one privileged root init container first, `init-sysctl`, on the cluster's own Memgraph image, writing `/proc/sys/vm/max_map_count` directly so no `sysctl` binary and no busybox image are involved — the same shape as `init-core-pattern`, which it precedes. The value is a property of the node and not one of the namespaced sysctls a pod's `securityContext.sysctls` may set, so a privileged container is the only in-pod way, exactly as the chart does it. Two things are decided differently from the chart. The container only ever raises: a node already at or above the floor is left as its administrator set it, so the operator never lowers a node shared with something that wanted more. And the default is 524288, the floor Memgraph's own startup check compares against and the value the docs recommend up to 64 GB of RAM, not the chart's 262144, which leaves the warning in every pod's log.

Nothing is pinned and the controller does not change: adding, removing or changing the block is a pod-template change the roll carries. A namespace enforcing PodSecurity `restricted` rejects the pod; there leaving the block out (and `coreDumps.configureCorePattern: false` if dumps are collected) is the answer, and the pods start with whatever the node has.

The same decision was applied to `coreDumps` in this slice for consistency: `coreDumps.<role>.enabled` is gone and a role collects dumps when its block is present. The pin moved with it — a CEL rule on the role block cannot see the block being added or removed, so `has(self.<role>) == has(oldSelf.<role>)` sits on `CoreDumpsSpec`, and the size rule on the role block fires only while the block exists in both versions, which is exactly "while the role collects dumps".

## Acceptance criteria

- [ ] `spec.sysctlInitContainer` is an optional block with `maxMapCount` (schema default `524288`, at least 1, mirrored as a Go constant); no schema default on the block itself
- [ ] Present, both roles' pods carry `init-sysctl` first, privileged root on the Memgraph image, raising `vm.max_map_count` to the floor and only raising; absent, no such container, and `init-core-pattern` is unaffected either way
- [ ] Builder tests cover absence, an empty block on both roles, an overridden floor, and the order beside the core pattern container
- [ ] Envtest covers an empty spec staying without the block, an empty block taking the default floor, and the floor's minimum
- [ ] `examples/minimal-cluster.yaml` stays restricted-clean with the block commented out; `test/e2e/init_containers_test.go` runs both init containers in an unlabeled namespace and reads the node's values from inside the pod
- [ ] `coreDumps.<role>` is a presence-based pointer block without `enabled`; presence is pinned on `CoreDumpsSpec`, size on the role block; envtest covers adding, removing, and an empty block taking the default size
- [ ] `make manifests generate chart-sync` regenerated, `make chart-verify` green
- [ ] The sample, the README and `CLAUDE.md` describe the block, the chart mapping, the changed default, and that absence means off

## Blocked by

- `09-pod-tuning-knobs.md`: the pod template the container joins
- `17-sequenced-rolling-restart.md`: what carries a value change to the pods
