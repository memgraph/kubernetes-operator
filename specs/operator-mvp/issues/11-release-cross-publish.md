# Release pipeline + cross-publish

**Type**: HITL — requires an org-level fine-grained PAT or deploy key for the helm-charts repository, which only a maintainer can create and store.

## Parent

`specs/operator-mvp/PRD.md`

## What to build

The release path from a version tag to installable artifacts: build and push the operator container image, package the install chart, and cross-publish the packaged chart into the existing `memgraph.github.io/helm-charts` index — so users install the operator from the same helm repository they already have configured, while chart source and CRDs stay in this repository. Chart version, image tag, and git tag stay in lockstep per release.

## Acceptance criteria

- [ ] Pushing a version tag builds and publishes the operator image with that version
- [ ] The same pipeline packages the install chart and publishes it into the `memgraph.github.io/helm-charts` index
- [ ] `helm repo update && helm install` from the existing Memgraph helm repo installs the tagged operator version end-to-end
- [ ] Chart version, appVersion, and image tag agree for every release
- [ ] The cross-repo credential is a scoped fine-grained PAT or deploy key stored as a repository secret, documented for rotation
- [ ] A dry-run/prerelease path exists to validate the pipeline without polluting the public index

## Blocked by

- `10-operator-install-chart.md`
