# Releasing

A release turns a git tag into two artifacts:

- the operator image, `docker.io/memgraph/kubernetes-operator:<appVersion>`, and
- the install chart, packaged and published into the
  [`memgraph.github.io/helm-charts`](https://memgraph.github.io/helm-charts) index — the same
  helm repository users already have configured for the Memgraph charts.

Chart source, CRDs and RBAC stay in this repository. Only the packaged `.tgz` and its index entry
land in `memgraph/helm-charts`, as a GitHub release named `memgraph-operator-<version>` plus a
line in `index.yaml` on that repository's `gh-pages` branch — the same layout chart-releaser
produces for the charts maintained there.

## Two versions

`charts/memgraph-operator/Chart.yaml` declares both, and they move independently:

| Field | Names | Changes when |
| --- | --- | --- |
| `version` | the chart | anything under `charts/memgraph-operator` changes |
| `appVersion` | the operator image the chart installs by default | a new operator image is released |

`appVersion` is what `deployment.yaml` uses when `image.tag` is left empty, so it is the operator
version a default install runs.

Because the two are independent, a tag has to say which one it releases:

| Tag | Releases | Must equal |
| --- | --- | --- |
| `v0.2.0` | the operator image, and the chart alongside it | `appVersion` |
| `chart-0.4.2` | the chart alone — no image is built | `version` |

The workflow refuses a tag whose version the tagged tree does not declare, before anything is
built. Bump the chart on a pull request, then tag the merge commit.

CI also refuses a pull request that changes the chart without bumping its `version`: a published
chart version is immutable, so a change that keeps its version is a change users never receive.

### The one thing to watch

The chart's `crds/` are generated from the Go types in the commit being released, but its
`appVersion` points at whenever the operator was last released. Nothing forces those to be the
same commit. A chart-only release cut after the API has moved on therefore ships a CRD with
fields the operator it installs has never seen.

The release run reports this rather than refusing it — the versions are deliberately independent
— as a warning in the job summary listing what changed. When it fires, either release the
operator too (`v<version>`, which realigns both) or cut the chart release from the commit its
`appVersion` names.

## Releasing the operator

1. Open a pull request bumping `appVersion` — and `version`, since the chart changed — in
   `charts/memgraph-operator/Chart.yaml`:

   ```sh
   $ make -s chart-version chart-app-version
   ```

2. Merge it once CI is green.
3. Tag the merge commit and push:

   ```sh
   git switch main && git pull
   git tag v0.2.0 && git push origin v0.2.0
   ```

The [Release workflow](../.github/workflows/release.yml) then:

- refuses to overwrite an existing image tag, then builds and pushes `linux/amd64` and
  `linux/arm64` — plus `:latest`, for stable versions only;
- verifies the chart's generated CRDs and RBAC still match the Go sources, lints it, packages it,
  cuts the `memgraph-operator-<version>` release in `memgraph/helm-charts` and merges its entry
  into the index;
- installs the published chart from `https://memgraph.github.io/helm-charts` on a Kind cluster
  and asserts the running Deployment is the image just released;
- creates the GitHub release here, with the packaged chart attached.

## Releasing the chart alone

For a template fix, a new values knob, or chart documentation — no operator change:

1. Bump only `version` in `Chart.yaml` on a pull request, and merge.
2. Tag with the `chart-` prefix:

   ```sh
   git tag chart-0.4.2 && git push origin chart-0.4.2
   ```

No image is built. The run checks that the image named by `appVersion` exists on Docker Hub
before publishing, so the chart can never point at an operator that was never released.

## Rehearsing a release

Two ways, for different questions.

**Dry run** — *does the pipeline work?* Run the workflow from the Actions tab with **publish**
off. Everything happens except publication: both architectures are built, the chart is packaged,
the index merge is computed and printed as a diff, and the chart is installed on Kind from the
local package with the locally built image. Nothing reaches Docker Hub or the index.

**Prerelease** — *does publishing work?* Declare the prerelease version in `Chart.yaml` like any
other (`appVersion: "0.2.0-rc.1"`, or `version: 0.4.2-rc.1` for a chart-only one), then tag it:
`v0.2.0-rc.1`, `chart-0.4.2-rc.1`. The tag still has to name what the chart declares, so the
candidate is a commit like any other release. This publishes for real, against the real index,
but:

- helm hides prerelease versions from `helm search` and `helm install` unless `--devel` is
  passed, so nobody installs one by accident;
- the image is not tagged `:latest`;
- the GitHub releases are marked as prereleases.

The public index gains an entry that ordinary use never sees. Prereleases are the only way to
exercise the credentials, the cross-repo push and GitHub Pages' propagation before a real
release depends on them.

## Credentials

Three repository secrets on `memgraph/kubernetes-operator`. The run checks all of them are
present before it builds anything.

| Secret | Used for |
| --- | --- |
| `DOCKERHUB_USERNAME` | pushing the operator image |
| `DOCKERHUB_TOKEN` | pushing the operator image |
| `HELM_CHARTS_TOKEN` | creating the release in `memgraph/helm-charts` and pushing its index |

`DOCKERHUB_USERNAME` / `DOCKERHUB_TOKEN` are the same pair the other Memgraph repositories use to
publish images, and are typically inherited from the organization.

### `HELM_CHARTS_TOKEN`

A **fine-grained personal access token**, scoped to nothing but the charts repository:

- **Resource owner**: `memgraph`
- **Repository access**: *Only select repositories* → `memgraph/helm-charts`
- **Repository permissions**: **Contents: Read and write** (this covers both creating the release
  and pushing `index.yaml` to `gh-pages`). Metadata read is added automatically. Nothing else.
- **Expiration**: 90 days.

A deploy key is not enough on its own: it can push to `gh-pages`, but cannot create the GitHub
release the index entry points at. A token owned by a machine account is preferable to a personal
one — releases in `memgraph/helm-charts` are attributed to whoever owns it, and a personal token
dies with the person's access.

Creating the token requires admin on `memgraph/helm-charts`, which is why this part of the
release setup cannot be automated from here.

**Rotation**, before expiry or whenever someone with access leaves:

1. Create the replacement with the scope above.
2. Update the `HELM_CHARTS_TOKEN` secret on `memgraph/kubernetes-operator`.
3. Verify with a prerelease tag — it is the only path that exercises the token end to end.
4. Delete the old token.

An expired token fails the run at the credential check with the secret named, before anything is
published; nothing is left half-done.

## When a release goes wrong

Every publishing step is skipped when its result already exists, so **re-running a failed release
is safe and finishes the job** rather than duplicating it. If a run fails between pushing the
image and indexing the chart, re-run it and it will complete:

- an image tag already pushed **from that same commit** is left alone and the build skipped —
  recognised by its `org.opencontainers.image.revision` label;
- an image tag that exists but came from anywhere else stops the run, because that is someone
  else's tag, not this release's;
- an existing charts release has the package re-attached;
- an index that already lists the version is left untouched.

What cannot be undone is a published version number. Chart versions and image tags are immutable
by convention and by the trust users place in them — to fix a bad release, release the next
version.

## Doing it by hand

The pipeline is a thin wrapper around targets you can run locally:

```sh
make chart-version          # the chart version
make chart-app-version      # the operator version it installs
make chart-version-check    # chart changed => version bumped (against BASE_REF)
make chart-package          # package into dist/chart, publish nothing
make test-chart-publish     # exercise the whole publish path offline
GH_TOKEN=... make chart-publish   # publish for real
```

`hack/chart-publish.sh` publishes nothing without `--publish`; with it, it prints the exact index
diff it is about to push. `make test-chart-publish` runs the whole path — package, release,
index merge, re-run — against a local stand-in for `memgraph/helm-charts`, offline, and CI runs
it on every pull request.

## A note on the legacy image tags

`docker.io/memgraph/kubernetes-operator` already carries tags `0.0.1` through `1.0.0`, published
by the earlier operator attempt now archived on `archive/pre-operator-mvp`. They are unrelated to
this operator. The release run refuses to overwrite any existing tag, so they cannot be clobbered
by accident — but it does mean the current `0.x` line sorts below them on Docker Hub.
