# Operator install chart

**Type**: AFK

## Parent

`specs/operator-mvp/PRD.md`

## What to build

The Helm chart users install the operator with, living in this repository next to the generated CRDs so they can never drift from the controller version. The chart ships the CRDs, a least-privilege RBAC set scoped to what the controller actually touches (its CRD, StatefulSets, Services, Secrets read, events, leases), and the controller Deployment running non-root with a restricted security context. `helm install` from the local chart on a clean cluster must be the complete install story.

## Acceptance criteria

- [ ] `helm install` from the local chart on a clean cluster yields a running operator that reconciles a `MemgraphCluster`
- [ ] CRDs in the chart are generated from the Go types in the same commit — no hand-edited copies
- [ ] RBAC grants only the verbs/resources the controller uses; the e2e suite passes under that RBAC
- [ ] Controller pod runs non-root with a restricted security context
- [ ] Chart lints clean and install/uninstall is exercised in CI
- [ ] Image tag/repository, resources, and namespace are configurable chart values

## Blocked by

- `02-provisioning-walking-skeleton.md`
