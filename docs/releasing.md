# Releasing

Two artifacts reach users, through two repositories:

- the operator image, `docker.io/memgraph/kubernetes-operator:<appVersion>`, released from here
  by running the Release workflow, which also tags the commit `v<appVersion>`, and
- the install chart, in the [`memgraph.github.io/helm-charts`](https://memgraph.github.io/helm-charts)
  index, released from [`memgraph/helm-charts`](https://github.com/memgraph/helm-charts) the same
  way as every other Memgraph chart.

Chart source, CRDs and RBAC are maintained here, where they are generated from the Go sources and
tested against the operator. `memgraph/helm-charts` carries a copy, and its own Release Charts
workflow publishes it. Nothing in this repository writes into that one.

## Two versions

`charts/memgraph-operator/Chart.yaml` declares both, and they move independently:

| Field | Names | Changes when |
| --- | --- | --- |
| `version` | the chart | anything under `charts/memgraph-operator` changes |
| `appVersion` | the operator image the chart installs by default | a new operator image is released |

`appVersion` is what `deployment.yaml` uses when `image.tag` is left empty, so it is the operator
version a default install runs. The release takes its version from it and creates the
`v<appVersion>` tag itself, refusing before anything is built if that tag already exists on
another commit. Bump the chart on a pull request, then release from `main`.

CI also refuses a pull request that changes the chart without bumping its `version`: a published
chart version is immutable, so a change that keeps its version is a change users never receive.

### The one thing to watch

The chart's `crds/` are generated from the Go types of the commit it is copied from, but its
`appVersion` points at whenever the operator was last released. A chart-only release cut after
the API has moved on therefore ships a CRD with fields the operator it installs has never seen.
Before copying a chart whose `appVersion` you did not bump, check
`git diff v<appVersion> -- charts/memgraph-operator/crds` is empty, or release the operator too.

## Releasing the operator

1. Open a pull request bumping `appVersion` — and `version`, since the chart changed — in
   `charts/memgraph-operator/Chart.yaml`:

   ```sh
   $ make -s chart-version chart-app-version
   ```

2. Merge it once CI is green.
3. Run the [Release workflow](../.github/workflows/release.yml) on `main` with **publish** ticked,
   from **Actions → Release → Run workflow**, or:

   ```sh
   gh workflow run release.yml --ref main -f publish=true
   ```

The workflow then:

- refuses to overwrite an existing image tag, then builds and pushes `linux/amd64` and
  `linux/arm64` — plus `:latest`, for stable versions only;
- verifies the chart's generated CRDs and RBAC still match the Go sources, lints and packages it;
- installs the packaged chart on a Kind cluster and asserts the running Deployment is the image
  just pushed;
- creates the `v<appVersion>` tag on the commit it built, and the GitHub release here with the
  packaged chart attached.

Then publish the chart, below.

## Publishing the chart

After an operator release, or on its own for a template fix, a new values knob or chart
documentation (bump only `version`, on a pull request here, first):

1. Copy `charts/memgraph-operator/` from the released commit (the `v<appVersion>` tag, or the
   merge commit for a chart-only release) over `charts/memgraph-operator/` in
   `memgraph/helm-charts`, and merge that pull request.
2. Run its **Release Charts** workflow from the Actions tab. chart-releaser releases every chart
   whose version is not released yet and adds it to the index.

Publish the chart only once the image its `appVersion` names is on Docker Hub, or a default
install pulls an image that does not exist.

## Rehearsing a release

Two ways, for different questions.

**Dry run** — *does the pipeline work?* Run the Release workflow with **publish** off, from any
branch (`gh workflow run release.yml --ref <branch>`): both architectures are built, the chart is
packaged and installed on Kind with the locally built image, and nothing reaches Docker Hub, a
tag or a GitHub release.

**Prerelease** — *does publishing work?* Declare the prerelease version in `Chart.yaml` like any
other (`appVersion: "0.2.0-rc.1"`), merge, and release it from `main` with **publish** on; the
tag is `v0.2.0-rc.1`. This publishes for real, but the
image is not tagged `:latest` and the GitHub release is marked as a prerelease. A prerelease chart
version in `memgraph/helm-charts` is likewise hidden from `helm install` unless `--devel` is
passed.

## Credentials

Two repository secrets on `memgraph/kubernetes-operator`, checked before anything is built:

| Secret | Used for |
| --- | --- |
| `DOCKERHUB_USERNAME` | pushing the operator image |
| `DOCKERHUB_TOKEN` | pushing the operator image |

They are the same pair the other Memgraph repositories use to publish images. The GitHub release
and the tag use the workflow's own `GITHUB_TOKEN`.

## When a release goes wrong

**Re-running a failed release is safe and finishes the job** rather than duplicating it:

- an image tag already pushed **from that same commit** is left alone and the build skipped —
  recognised by its `org.opencontainers.image.revision` label;
- an image tag that exists but came from anywhere else stops the run, because that is someone
  else's tag, not this release's;
- a `v<appVersion>` tag already on the commit being released is accepted, and an existing GitHub
  release left alone.

What cannot be undone is a published version number. Chart versions and image tags are immutable
by convention and by the trust users place in them — to fix a bad release, release the next
version.

## Doing it by hand

```sh
make chart-version          # the chart version
make chart-app-version      # the operator version it installs
make chart-version-check    # chart changed => version bumped (against BASE_REF)
make chart-package          # package into dist/chart
```

## A note on the legacy image tags

`docker.io/memgraph/kubernetes-operator` already carries tags `0.0.1` through `1.0.0`, published
by the earlier operator attempt now archived on `archive/pre-operator-mvp`. They are unrelated to
this operator. The release run refuses to overwrite any existing tag, so they cannot be clobbered
by accident — but it does mean the current `0.x` line sorts below them on Docker Hub.
