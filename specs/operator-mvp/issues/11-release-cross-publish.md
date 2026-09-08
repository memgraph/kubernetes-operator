# Release pipeline + cross-publish

**Type**: HITL — requires an org-level fine-grained PAT or deploy key for the helm-charts repository, which only a maintainer can create and store.

## Parent

`specs/operator-mvp/PRD.md`

## What to build

The release path from a version tag to installable artifacts: build and push the operator container image, package the install chart, and cross-publish the packaged chart into the existing `memgraph.github.io/helm-charts` index — so users install the operator from the same helm repository they already have configured, while chart source and CRDs stay in this repository. Chart version, image tag, and git tag stay in lockstep per release.

## Acceptance criteria

- [x] Pushing a version tag builds and publishes the operator image with that version
- [x] The same pipeline packages the install chart and publishes it into the `memgraph.github.io/helm-charts` index
- [x] `helm repo update && helm install` from the existing Memgraph helm repo installs the tagged operator version end-to-end
- [x] The appVersion and the operator image tag agree for every release, and the chart's own version is bumped whenever the chart changes
- [x] The cross-repo credential is a scoped fine-grained PAT or deploy key stored as a repository secret, documented for rotation
- [x] A dry-run/prerelease path exists to validate the pipeline without polluting the public index

> **Amended during implementation.** The fourth criterion originally read "Chart version,
> appVersion, and image tag agree for every release" — chart and operator versions locked in
> lockstep. That was relaxed to the Helm-standard arrangement, where the chart's `version` and its
> `appVersion` move independently, so a chart-only fix (a template bug, a new values knob) does
> not require a redundant operator release. The invariant that remains is the load-bearing one:
> `appVersion` is the operator image tag, and the image it names must exist.
>
> Tags therefore name the artifact they release — `v<version>` the operator, `chart-<version>` the
> chart alone. The known cost is that a chart-only release can ship CRDs generated after the
> operator its `appVersion` installs; the pipeline reports that drift in the run summary rather
> than refusing it. See `docs/releasing.md`.

## Blocked by

- `10-operator-install-chart.md`
