# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A kubebuilder-based Go operator for Memgraph high-availability clusters. It exposes one CRD, `MemgraphCluster` (group `memgraph.com`, version `v1alpha1`, short name `mgc`), and replaces the `memgraph-high-availability` Helm chart's fire-and-forget registration Job with continuous, state-aware reconciliation over Bolt.

The repository was reset for this effort (prior attempt is on `archive/pre-operator-mvp`). Work is driven by `specs/operator-mvp/PRD.md` and sliced into PR-gated issues in `specs/operator-mvp/issues/` — read the PRD before making design decisions; it records what is in scope (provision, bootstrap, observe) and what is deliberately out (day-2 ops, scaling, TLS, external access, monitoring).

`AGENTS.md` contains the generic kubebuilder agent guide (scaffolding commands, marker reference, never-edit rules). Follow it, especially: never hand-edit `config/crd/bases/*`, `config/rbac/role.yaml`, `zz_generated.*.go`, or `PROJECT`; never delete `// +kubebuilder:scaffold:*` markers.

## Commands

```sh
make test-unit     # unit tests only (pure packages; excludes e2e and internal/controller)
make test          # unit + envtest (downloads envtest binaries into bin/ on first run)
make lint          # golangci-lint (lint-fix to auto-fix, lint-config to verify config)
make manifests generate   # regenerate CRDs/RBAC + DeepCopy after editing *_types.go or markers
make build         # build manager binary
make run           # run controller locally against current kubeconfig
make test-e2e      # KinD e2e suite — creates/deletes a dedicated Kind cluster; never run against a real cluster
make chart-sync    # regenerate the install chart's CRDs + RBAC rules from the Go sources
make chart-verify  # fail if those generated chart files are stale (CI gate)
make helm-lint     # lint the install chart and render it with defaults and toggles flipped
make test-chart    # helm install/uninstall the chart on a throwaway Kind cluster
make chart-package # package the chart into dist/chart (publishes nothing)
make test-chart-publish  # exercise the cross-publish path offline, against a fake helm-charts repo
```

Run a single test (Ginkgo suites):

```sh
go test ./internal/controller/ -ginkgo.focus="<It/Describe text>"   # needs KUBEBUILDER_ASSETS for envtest, see below
go test ./api/... -run TestName
```

Envtest packages need `KUBEBUILDER_ASSETS`; outside of `make test` set it with:
`KUBEBUILDER_ASSETS=$(bin/setup-envtest use <k8s-version> --bin-dir bin -p path)`

CI (`.github/workflows/`) runs `make lint-config`, `make lint`, `make test-unit`, `make test`, `make chart-verify`, `make helm-lint`, `make chart-version-check`, `make test-chart-publish`, `make test-chart`, and `make test-e2e` on every PR — all must be green. The e2e job boots a licensed Memgraph cluster on a multi-node Kind cluster, with the license flowing from the `MEMGRAPH_ENTERPRISE_LICENSE` / `MEMGRAPH_ORGANIZATION_NAME` repository secrets (set the same env vars to run it locally).

### Toolchain quirks (do not "fix" these)

- Coverage is opt-in (`make test COVER_FLAGS="-coverprofile cover.out"`). Plain `go test -cover` breaks under Go toolchain auto-switching (covdata tool build fails), so `make test` must stay coverage-free by default.
- The golangci-lint Makefile install pins `GOTOOLCHAIN` to the project's Go version; `go install` otherwise builds it with an older toolchain that refuses to lint this project.

## Architecture

The PRD defines seven modules with two pure cores and one mock seam. Keep this separation — it's what makes the logic testable without a cluster:

1. **API types** (`api/v1alpha1/`) — spec/status with kubebuilder + CEL markers. Replica counts (coordinators, data instances) are **immutable after creation, enforced by CEL** in the CRD, not a webhook. Defaults are declared as CRD schema defaults *and* mirrored as Go constants in `memgraphcluster_types.go` so resource builders behave correctly on specs that never passed admission (unit tests).
2. **Resource builders** — pure functions: spec in, desired Kubernetes objects out (one StatefulSet per role + headless Services; per-pod identity such as coordinator ID and advertised addresses derived from pod ordinals). No API calls, no side effects. Tested golden-style.
3. **Memgraph HA client** — a narrow Go interface (show instances, register instance, add coordinator, set main) over the Bolt driver. Everything above depends on the interface, never the driver — this is the mock seam.
4. **Registration planner** — pure diff: declared topology + observed `SHOW INSTANCES` in, ordered registration commands out (empty when converged). Reconciliation semantics live here: read-before-write, idempotent, re-issue only missing registrations. `SET INSTANCE TO MAIN` is issued exactly once at bootstrap (when no MAIN exists); after that, failover belongs to the Raft coordinators — the operator only observes.
5. **Controller** (`internal/controller/`) — fetch CR, server-side-apply builder output, gate on pod readiness, run planner against the HA client, write status/conditions.
6. **Operator install chart** (`charts/memgraph-operator/`) — lives in this repo, cross-published to `memgraph.github.io/helm-charts` at release. Its `crds/` and `rbac/manager-rules.yaml` are **generated** (`make chart-sync`, verified by `make chart-verify`): the manager's ClusterRole comes from the `+kubebuilder:rbac` markers, so tightening or widening the controller's permissions means editing the markers, never the chart. The e2e suite installs the operator through this chart, so every scenario runs under the RBAC users get. The chart's `version` and its `appVersion` (the operator image tag) move **independently**: tag `v<version>` releases the operator, `chart-<version>` releases the chart alone — see `docs/releasing.md`.
7. **E2E harness** (`test/e2e/`, build tag `e2e`) — multi-node KinD with real Memgraph images.

Test philosophy (from the PRD): assert external behavior, never internal call ordering or private state. Builders get golden tests, planner gets pure topology-diff cases, controller gets envtest with the HA client mocked.

## Conventions

- Spec knob names mirror the HA Helm chart's vocabulary where the concept carries over (e.g. the `secrets.name` / `secrets.licenseKey` / `secrets.organizationKey` block) — check the chart before inventing a name.
- No secret material in spec or status; secrets are consumed by reference only.
- No destructive code paths in v1: no finalizer-based storage cleanup, no instance unregistration; PVC retention maps to the StatefulSet PVC retention policy (default `Retain`).
- Workload pods: non-root uid 101 / gid 103, seccomp RuntimeDefault, all capabilities dropped.
- Log messages follow Kubernetes style: capital first letter, no trailing period, past tense, object type named (see AGENTS.md for examples).
