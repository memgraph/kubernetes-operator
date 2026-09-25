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

The PRD defines seven modules with two pure cores and one mock seam; an eighth (the rolling-restart decision) was added post-v1 as a third pure core. Keep this separation — it's what makes the logic testable without a cluster:

1. **API types** (`api/v1alpha1/`) — spec/status with kubebuilder + CEL markers. Replica counts (coordinators, data instances) are **mutable in both directions**, bounded by schema floors alone (coordinators ≥ 3 and odd, data instances ≥ 1) — enforced by the CRD, not a webhook. Defaults are declared as CRD schema defaults *and* mirrored as Go constants in `memgraphcluster_types.go` so resource builders behave correctly on specs that never passed admission (unit tests).
2. **Resource builders** — pure functions: spec in, desired Kubernetes objects out (one StatefulSet per role + headless Services; per-pod identity such as coordinator ID and advertised addresses derived from pod ordinals). No API calls, no side effects. Tested golden-style.
3. **Memgraph HA client** — a narrow Go interface (show instances, show replication lag, register instance, add coordinator, set main, demote/unregister instance, remove coordinator, yield leadership) over the Bolt driver. Everything above depends on the interface, never the driver — this is the mock seam.
4. **Registration planner** — pure diff: declared topology + observed `SHOW INSTANCES` in, ordered registration commands out (empty when converged). Reconciliation semantics live here: read-before-write, idempotent, re-issue only missing registrations. A MAIN is promoted only when the cluster has none — at bootstrap, and after the planner itself demoted a data instance that is retiring; a MAIN that is staying is never overridden, because failover belongs to the Raft coordinators. A lowered count is the one removal the planner drives: `DEMOTE INSTANCE` the retiring MAIN, promote a survivor, `UNREGISTER INSTANCE` the retiring data instances, `REMOVE COORDINATOR` the retiring coordinators, and only then does the controller shed their pods (see `specs/operator-mvp/issues/15`, `16`). Moving MAIN off a retiring instance is the one step that can lose data, so it has a precondition nothing else does: a survivor that is both reachable and reported by `SHOW REPLICATION LAG` as holding every transaction the MAIN committed, in every database. Without one, the demotion, the promotion and that instance's unregistration are all left out of the plan and the retiring MAIN keeps serving — a scale-down that pauses, not one that drops writes. Because that state plans *nothing*, an empty plan is not proof a retirement finished: `planner.Retired` is what gates shedding the pods. One command breaks the pure-diff mould: `YIELD LEADERSHIP`, needed because Raft refuses to remove its own leader. It names no successor, so it is always a plan's **last** command and terminal — the controller requeues and re-observes under whichever coordinator won the election. The one mutable thing about a registration is the announced `bolt_server`: the declared topology carries the external address of every exposed member (see external access below) and the planner issues `UPDATE CONFIG FOR INSTANCE|COORDINATOR` whenever the observed one differs, skipping an empty observed address (a view that does not report one) and every retiring member.
5. **Controller** (`internal/controller/`) — fetch CR, server-side-apply builder output, gate on pod readiness, run planner against the HA client, write status/conditions.
6. **Operator install chart** (`charts/memgraph-operator/`) — lives in this repo, cross-published to `memgraph.github.io/helm-charts` at release. Its `crds/` and `rbac/manager-rules.yaml` are **generated** (`make chart-sync`, verified by `make chart-verify`): the manager's ClusterRole comes from the `+kubebuilder:rbac` markers, so tightening or widening the controller's permissions means editing the markers, never the chart. The e2e suite installs the operator through this chart, so every scenario runs under the RBAC users get. The chart's `version` and its `appVersion` (the operator image tag) move **independently**: tag `v<version>` releases the operator, `chart-<version>` releases the chart alone — see `docs/releasing.md`.
7. **Rolling-restart decision** (`internal/rollout/`) — the third pure core: both roles' pods reduced to `{name, revisionHash, ready}` plus each StatefulSet's `UpdateRevision`, the `SHOW INSTANCES` view and `SHOW REPLICATION LAG` in, **exactly one** action out (`Done` / `Wait(reason)` / `Delete(pod)`). Both StatefulSets use `updateStrategy: OnDelete`, so the operator owns every pod restart and this decides which pod is next: data instances before coordinators, the observed MAIN last of its role, the Raft leader last of its. Nothing is persisted — pods already carrying the new revision *are* the ones already restarted, so a mid-roll spec revert or a Raft-driven MAIN move self-corrects. One action per pass and never a list, because every step re-gates on fresh lag. It issues no Bolt commands at all; a roll is invisible to the planner.
8. **E2E harness** (`test/e2e/`, build tag `e2e`) — multi-node KinD with real Memgraph images.

Test philosophy (from the PRD): assert external behavior, never internal call ordering or private state. Builders get golden tests, planner gets pure topology-diff cases, the rolling-restart decision gets pure cases over pod revisions and observed cluster state, controller gets envtest with the HA client mocked.

## Conventions

- Spec knob names mirror the HA Helm chart's vocabulary where the concept carries over (e.g. the `secrets.name` / `secrets.licenseKey` / `secrets.organizationKey` block) — check the chart before inventing a name.
- No secret material in spec or status; secrets are consumed by reference only.
- Storage is never deleted by the operator: no finalizer-based cleanup; PVC retention (deletion *and* scale-down) maps to the StatefulSet PVC retention policy (default `Retain`). The only cluster members the operator removes are the ones a lowered replica count retires; a coordinator removed from Raft keeps running and keeps its state on purpose, which is what makes re-growing onto a retained volume safe.
- Workload pods: non-root uid 101 / gid 103, seccomp RuntimeDefault, all capabilities dropped, `terminationGracePeriodSeconds: 300` (a ceiling, not a delay — an instance killed mid-shutdown recovers from its WAL and lengthens the catch-up a roll waits on).
- **Readiness probe only, no liveness or startup probe**, on both roles. Memgraph recovers every database inside the `DbmsHandler` constructor before any server starts, so during recovery nothing listens and no probe can tell slow from hung; a TCP liveness check could only kill a recovery that outlived a guessed budget (the HA chart's 2h), and a Bolt listener that disappears after startup means the process exited, which restarts the container anyway. A recovering pod is simply unready. The field converged on the same answer (ECK, CockroachDB, MongoDB Community run no liveness; Strimzi and cass-operator make it a process check; Percona deprecated its 7200s startup delay). Do not reintroduce them; a core health endpoint reporting recovery *progress* is the only thing that would justify a liveness check.
- StatefulSet `volumeClaimTemplates` cannot be changed, added or removed on a live cluster, and rebuilding the StatefulSet around its pods deadlocks the roll (the StatefulSet controller only recovers adopted pods in ascending ordinal order, MAIN first). Every claim-template field is therefore pinned by a CEL transition rule on the type that carries it: `storage.<role>.{libPVCSize,libStorageAccessMode,libStorageClassName,createLogStorageClaim}`, the `log*` knobs while the log claim exists, `coreDumps.<role>.{enabled,size}` and `coreDumps.storageClassName` while a role collects dumps. Sizes are compared as quantities, so a unit change is not a change. The doc comment on `RoleStorageSpec` carries the argument; read it before touching any claim template knob.
- Both StatefulSets use `updateStrategy: OnDelete`, so **nothing but the operator ever restarts a workload pod**. A pod-template change no reconcile acts on takes effect never, which is what the `Updated` condition exists to report. `RollingUpdate` cannot express the required order (it sweeps highest ordinal to lowest, and `partition` is a descending cutoff, not a set), so a MAIN on any ordinal but 0 would be restarted mid-sweep and each such restart buys another failover.
- The operator never promotes a MAIN outside bootstrap and the scale-down handover: a roll deletes the MAIN's pod and lets the Raft coordinators promote. That is only safe on a Memgraph reporting an unreachable MAIN as `role=main, health=down` — a release that vacates the `main` row makes `planner.Plan` believe there is no MAIN and race the failover.
- Log messages follow Kubernetes style: capital first letter, no trailing period, past tense, object type named (see AGENTS.md for examples).
- **External access** (`spec.externalAccess`, `type: LoadBalancer | Gateway`) is operator-owned on both sides: the external objects *and* the announced `bolt_server` of every exposed member. Both modes build the same Services, one shared by the coordinators plus one per data instance (selected by the StatefulSet pod-name label); with `LoadBalancer` they are the way in, with `Gateway` they are ClusterIPs behind one operator-owned Gateway (`gatewayClassName` required, listeners `coordinators-bolt` on 7687 and `data-<n>-bolt` on `dataPortBase + n`, default 9000) fed by one TCPRoute each. The address is discovered, never declared: external-dns hostname annotation (on the Service, or in Gateway mode on the TCPRoute, then the Gateway), then the reported hostname, then the IP, pod DNS until any exists (`Converged=False/ExternalAddressPending`). `{ordinal}` in per-instance annotation values is substituted with the pod ordinal and CEL requires it on a data hostname annotation. Only the bolt address ever leaves the cluster network; the operator itself always dials coordinators on pod DNS (`resources.CoordinatorEndpoints`), never through the LoadBalancer or Gateway. External objects follow the data pods the operator *runs* (applied count), are pruned by label once the spec stops describing them, and removing the block reverts every `bolt_server` to pod DNS. The Gateway API CRDs are never bundled: `main.go` discovers once at start (`controller.GatewayAPIServed`) whether `Gateway` and `TCPRoute` are served at `v1`, watches the kinds only if so, and a cluster asking for `Gateway` without that gets `Converged=False/ApplyFailed` naming what is missing. TCPRoute is `v1` only from Gateway API v1.6 (Envoy Gateway ≥ v1.9); older releases serve it as `v1alpha2` alone and v1.6 stops serving `v1alpha2`, so no single version works on both sides — the operator requires ≥ v1.6. The e2e suite gets LoadBalancer addresses from MetalLB and a Gateway from Envoy Gateway (`hack/kind-metallb.sh`, `hack/kind-envoy-gateway.sh`, run by `make setup-test-e2e`); envtest loads the Gateway API CRDs from the `sigs.k8s.io/gateway-api` module.
