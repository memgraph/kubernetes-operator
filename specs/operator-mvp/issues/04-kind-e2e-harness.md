# KinD e2e harness, PR-gated

**Type**: AFK

## Parent

`specs/operator-mvp/PRD.md`

## What to build

A true end-to-end suite: a multi-node KinD cluster in CI, the operator image built and deployed into it, a `MemgraphCluster` applied with real Memgraph images and the enterprise license supplied via repository secrets (the established practice from the HA chart's CI). The suite asserts the cluster reaches a registered state with a MAIN elected — the real-world proof of slices 02 and 03.

Wire the suite into CI as a required check on every pull request, alongside the existing lint/unit/envtest gates. Keep the harness structured so later slices can add scenarios (re-registration, storage retention) as additional cases rather than new pipelines.

## Acceptance criteria

- [ ] CI boots a multi-node KinD cluster, builds and deploys the operator image, and applies a `MemgraphCluster`
- [ ] The suite asserts all declared instances appear registered in `SHOW INSTANCES` with exactly one MAIN
- [ ] Enterprise license flows from repository secrets; no secret material appears in logs or the repo
- [ ] The e2e job is a required PR check and passes on the current main
- [ ] Adding a new e2e scenario requires only a new test case, not pipeline changes

## Blocked by

- `03-bootstrap-registration.md`
