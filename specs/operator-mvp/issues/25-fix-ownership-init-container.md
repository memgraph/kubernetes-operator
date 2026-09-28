# Ownership-fixing init container

**Type**: AFK

## Parent

`specs/operator-mvp/PRD.md` — chart parity. The `memgraph-high-availability` chart's `fixOwnershipInitContainer` block; scoped on 2026-09-28.

## What to build

`spec.fixOwnershipInitContainer`, the chart's block under the chart's name with its `enabled`, its image knobs and the chart's `memgraphUserId`/`memgraphGroupId` dropped, which leaves it no fields. The block is presence-based like every other optional block of the resource — present, the container runs; absent, it does not — which here matches the chart's default of off, so the sample carries the block commented out. What it buys: every pod sets `fsGroup` to the memgraph group, which is how a volume normally arrives writable to the non-root memgraph user, but some storage drivers (rancher.io/local-path, Kind's default, among them) do not honor it and hand over a volume root owned by root:root. There Memgraph can neither create its data directory under the lib mount nor its log file under the log mount, and a data directory that exists but is owned by another user fails `VerifyStorageDirectoryOwnerAndProcessUserOrDie` at startup ("The process is running as user memgraph, but '...' is owned by user root").

Every pod of both roles runs one init container, `init-fix-perms`, after the node-tuning ones (`init-sysctl`, `init-core-pattern`), in the chart's order. It mounts exactly the claims the Memgraph container will use — the lib volume, the log volume when the role has a log claim, the core dumps volume when the role collects dumps — and runs `chown -R 101:103` over each. Three things are decided differently from the chart. The uid and gid are not knobs: they are fixed in the Memgraph and MAGE images and the operator already runs every pod as them, so a knob could only disagree with the pod. The image knobs are dropped as they were for the sysctl container: it runs the cluster's own Memgraph image, already on the node. And unlike the two node-tuning containers it is not privileged: it runs as root with `CAP_CHOWN` alone, the chart's own posture, so a namespace enforcing the baseline Pod Security Standard admits it while `restricted` rejects it for running as root.

Nothing is pinned and the controller does not change: adding or removing the block is a pod-template change the roll carries. The chown is unconditional and recursive on every pod start, as in the chart; a volume already owned correctly costs one walk of the data directory, which Memgraph keeps in few, large files.

## Acceptance criteria

- [ ] `spec.fixOwnershipInitContainer` is an optional block with no fields and no schema default; an empty block round-trips as present
- [ ] Present, both roles' pods carry `init-fix-perms` last, root with `CAP_CHOWN` alone on the Memgraph image, mounting and chowning the lib volume, the log volume when the role has one and the core dumps volume when the role collects dumps; absent, no such container
- [ ] Builder tests cover absence, both roles, a role without a log claim, and the mounts and order beside the two node-tuning containers
- [ ] Envtest covers an empty spec staying without the block and an empty block staying present
- [ ] `test/e2e/init_containers_test.go` asks for all three init containers in its unlabeled namespace and reads the ownership of every mounted volume from inside the pod
- [ ] `make manifests generate chart-sync` regenerated, `make chart-verify` green
- [ ] The sample (block commented out), the README and `CLAUDE.md` describe the block, the chart mapping, the dropped knobs, and that absence means off

## Blocked by

- `23-sysctl-max-map-count.md`: the init container shape and the e2e scenario this joins
