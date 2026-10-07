# User containers

**Type**: AFK

## Parent

`specs/operator-mvp/PRD.md` — chart parity. The `memgraph-high-availability` chart's `userContainers` block; scoped on 2026-09-28.

## What to build

`spec.userContainers.{coordinators,data}`, the chart's block under the chart's name: a list of `core/v1` Containers per role, appended to that role's pod template after the operator's own containers (Memgraph first so `kubectl logs` without `-c` keeps showing the database, then the core dumps uploader when the role has one). A user container may mount any volume the pod has, `extraVolumes` included, which is how a log shipper reads the log volume.

The entries are schemaless (`+kubebuilder:validation:Schemaless`, `PreserveUnknownFields`), for the reason `extraVolumes` already is: a full `core/v1` Container schema inlined twice grows the CRD past the 256KB a client-side `kubectl apply` can carry. The API server keeps the YAML verbatim; a malformed container or a name the pod already has (`memgraph`, `core-dumps-uploader`, the init containers) is rejected when the operator applies the StatefulSet and reported as `Converged=False/ApplyFailed`.

One builder convenience, in the spirit of the scheduling block's selector fill: a container naming no `securityContext` gets the same restricted one as the Memgraph container, so the chart's own busybox example runs in a namespace enforcing the restricted Pod Security Standard, which every e2e namespace does. A container naming a `securityContext` keeps it as written; that is how one that must write to its root filesystem or run as another user says so.

Nothing is pinned and the controller does not change: a change to the block is a pod-template change the roll carries. A user container counts toward pod readiness, so one that crash-loops keeps the pod from being registered, exactly as the uploader does.

Testing follows the house split. Builder tests pin the containers landing on their own role only and after the uploader, the security context fill, and a named security context surviving. Envtest proves a container's nested fields survive the schemaless round trip. No e2e: the pod template is the contract and the builder tests hold it.

## Acceptance criteria

- [ ] `spec.userContainers.{coordinators,data}` are schemaless `[]core/v1.Container` lists landing on that role's pod template only, after the operator's containers
- [ ] A container without a `securityContext` gets the restricted one; one with keeps its own
- [ ] Builder tests cover placement per role, order after the uploader, and both security context cases; envtest covers the round trip
- [ ] `make manifests generate chart-sync` regenerated, `make chart-verify` green, the CRD's compact JSON stays under the 256KB client-side apply limit
- [ ] The sample, the README and `CLAUDE.md` describe the block, the chart mapping and the security context fill

## Blocked by

- `09-pod-tuning-knobs.md`: the per-role block shape and the pod template these containers join
- `17-sequenced-rolling-restart.md`: what carries a change to the pods
