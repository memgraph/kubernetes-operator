# CEL immutability + validation

**Type**: AFK

## Parent

`specs/operator-mvp/PRD.md`

## What to build

Enforce the v1 contract that topology is fixed at creation: CEL validation rules on the CRD reject any change to the coordinator count or data-instance count on a live cluster, with a clear admission-time message telling the user scaling is not yet supported. Add creation-time validation (sane count ranges, required fields) and spec defaulting so a minimal CR is valid — all through CRD machinery, no admission webhook in v1.

## Acceptance criteria

- [ ] Updating either replica count on an existing `MemgraphCluster` is rejected at admission with a message stating counts are immutable in v1
- [ ] Invalid counts and missing required fields are rejected at creation with actionable messages
- [ ] Optional fields receive documented defaults; a minimal CR (counts, image, secrets) validates
- [ ] No admission webhook is introduced; all rules live in the CRD schema
- [ ] Envtest coverage: mutation attempts rejected, valid creates accepted, defaults materialize

## Blocked by

- `02-provisioning-walking-skeleton.md`
