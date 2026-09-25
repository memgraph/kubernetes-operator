# Storage configuration

**Type**: AFK

## Parent

`specs/operator-mvp/PRD.md`

## What to build

Give users control over persistence, mirroring the HA chart's storage vocabulary per role: lib and log PVC size, access mode, and storage class for coordinators and data instances. Add `storage.retentionPolicy` (`Retain` | `Delete`, default `Retain`) mapped directly onto the StatefulSet PVC retention policy, so deleting the CR preserves data by default while dev clusters can opt into self-cleanup. The operator carries no finalizer-based storage cleanup of its own — the StatefulSet machinery is the only deleter.

## Acceptance criteria

- [ ] PVC size, access mode, and storage class are configurable per role for lib and log volumes
- [ ] Default retention: deleting the CR leaves PVCs behind
- [ ] With `Delete` retention, deleting the CR removes the PVCs via StatefulSet retention machinery
- [ ] No operator-owned finalizer performs storage deletion
- [ ] Builder golden tests cover storage defaults, overrides, and both retention policies
- [ ] E2E scenario: default-retention CR deletion leaves PVCs intact

## Blocked by

- `02-provisioning-walking-skeleton.md`
