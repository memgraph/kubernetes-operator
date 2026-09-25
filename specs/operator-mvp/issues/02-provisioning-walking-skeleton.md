# Provisioning walking skeleton

**Type**: AFK

## Parent

`specs/operator-mvp/PRD.md`

## What to build

The first real vertical slice: applying a minimal `MemgraphCluster` — coordinator count, data-instance count, image (repository/tag/pullPolicy), and the chart-compatible secrets block (`secrets.name`, `secrets.licenseKey`, `secrets.organizationKey`) — makes the controller provision one StatefulSet for all coordinators and one for all data instances, each backed by a headless Service. Pods boot licensed Memgraph in HA roles with identity-dependent flags (coordinator ID, advertised FQDN addresses) derived from the pod ordinal. No registration yet — the demo is "apply one CR, watch licensed coordinator and data pods reach ready."

Structure the code along the module boundaries from the PRD: pure resource builders (spec in, desired objects out — no API calls) invoked by the controller via server-side apply. Workload pods follow Memgraph security conventions: non-root uid 101 / gid 103, seccomp RuntimeDefault, all capabilities dropped.

## Acceptance criteria

- [ ] Applying a minimal `MemgraphCluster` produces exactly two StatefulSets (coordinators, data) with matching headless Services
- [ ] All pods reach ready with the enterprise license consumed from the referenced Secret using the configured key names
- [ ] Coordinator IDs and advertised addresses are derived from pod ordinals; all pods within a role are uniform
- [ ] Resource builders are pure and covered by golden-style unit tests (defaults and ordinal-derived identity)
- [ ] Controller behavior covered by envtest: CR in, expected objects created; re-reconcile is idempotent
- [ ] Deleting the CR removes the workloads (PVC handling comes in a later slice)

## Blocked by

- `01-repo-reset-scaffold.md`
