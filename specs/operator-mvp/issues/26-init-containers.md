# Init containers

**Type**: AFK

## Parent

`specs/operator-mvp/PRD.md` — chart parity. The `memgraph-high-availability` chart's `initContainers` block; scoped on 2026-09-28.

## What to build

`spec.initContainers.{coordinators,data}`, the chart's block under the chart's name: a list of `core/v1` Containers per role, appended to that role's pod template after the operator's own init containers (`init-sysctl`, `init-core-pattern`, `init-fix-perms`, whichever the spec asked for) and before Memgraph, in the chart's order. Running last is what lets a user container work on a volume the ownership container has already chowned; it may mount any volume the pod has, `extraVolumes` included.

The entries are schemaless for the reason `userContainers` and `extraVolumes` already are: a full `core/v1` Container schema inlined twice more grows the CRD past the 256KB a client-side `kubectl apply` can carry. The API server keeps the YAML verbatim; a malformed container or a name the pod already has is rejected when the operator applies the StatefulSet and reported as `Converged=False/ApplyFailed`.

The same builder convenience as `userContainers`, through the same function: a container naming no `securityContext` gets the restricted one the Memgraph container runs with, so the chart's own busybox example runs in a namespace enforcing the restricted Pod Security Standard; one naming a `securityContext` keeps it as written, which is how a container that must run as root says so.

Nothing is pinned and the controller does not change: a change to the block is a pod-template change the roll carries. A failing init container keeps the pod from ever starting Memgraph, and so from ever being registered.

Testing follows the house split. Builder tests pin the containers landing on their own role only and after every operator-owned init container, the security context fill, and a named security context surviving. Envtest proves a container's nested fields survive the schemaless round trip. No e2e: the pod template is the contract and the builder tests hold it.

## Acceptance criteria

- [ ] `spec.initContainers.{coordinators,data}` are schemaless `[]core/v1.Container` lists landing on that role's pod template only, after the operator's init containers
- [ ] A container without a `securityContext` gets the restricted one; one with keeps its own
- [ ] Builder tests cover placement per role, order after the three operator-owned init containers, and both security context cases; envtest covers the round trip
- [ ] `make manifests generate chart-sync` regenerated, `make chart-verify` green, the CRD's compact JSON stays under the 256KB client-side apply limit
- [ ] The sample, the README and `CLAUDE.md` describe the block, the chart mapping and the security context fill

## Blocked by

- `24-user-containers.md`: the schemaless per-role container list and the security context fill this reuses
- `25-fix-ownership-init-container.md`: the last operator-owned init container this runs after
