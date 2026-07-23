# Repo reset + kubebuilder scaffold + CI skeleton

**Type**: HITL — involves a destructive force-push and archiving the old attempt; a human must bless and execute the push.

## Parent

`specs/operator-mvp/PRD.md`

## What to build

Reset the `memgraph/kubernetes-operator` repository for the fresh operator effort. Park the existing contents (a discarded prior attempt) on an archive branch, then force-push a clean kubebuilder scaffold to `main`: Go module, `MemgraphCluster` API skeleton in group `memgraph.com/v1alpha1` (short name `mgc`), a hello-world reconciler, and the PRD plus these issues carried into the new history.

Stand up the CI skeleton alongside: lint, unit tests, and an envtest run against the placeholder reconciler, all triggered on every pull request. The suites may be near-empty — the point is that the pipeline exists and is green before feature work starts, so every later slice lands PR-gated.

## Acceptance criteria

- [ ] Old repository contents preserved on an `archive/`-prefixed branch
- [ ] `main` holds a fresh kubebuilder scaffold with kind `MemgraphCluster`, group `memgraph.com`, version `v1alpha1`, short name `mgc`
- [ ] `specs/operator-mvp/` (PRD + issues) committed as part of the new history
- [ ] CI runs lint, unit, and envtest suites on every pull request and is green
- [ ] Generated CRD manifests install cleanly on a local cluster and `kubectl get mgc` resolves

## Blocked by

None - can start immediately
