# PRD: Memgraph Kubernetes Operator — v1alpha1 MVP

## Problem Statement

Operating a Memgraph high-availability cluster on Kubernetes today means installing the `memgraph-high-availability` Helm chart, which has two structural problems from the user's perspective:

1. **Cluster registration is fire-and-forget.** A post-install Job registers coordinators and data instances once, then exits. If a pod is later rescheduled and loses its registration state, nothing re-registers it — the user must notice the degraded cluster and intervene manually with mgconsole.
2. **The configuration surface fights the user.** Every coordinator and data instance is its own copy-pasted values block backing its own StatefulSet and Services. Growing the cluster means duplicating a ~20-line block and hand-assigning IDs; the topology is spread across many near-identical resources instead of being expressed as "3 coordinators, 2 data instances."

Users of comparable databases (MongoDB, Elasticsearch, CockroachDB) expect a Kubernetes operator: declare the cluster as a single custom resource, and a controller continuously drives reality toward it.

## Solution

A Go operator (kubebuilder) exposing a `MemgraphCluster` custom resource in the `memgraph.com/v1alpha1` API group (short name `mgc`). The user writes one resource declaring topology as two numbers — coordinator count and data-instance count — plus standard pod-level knobs, and the operator:

- Provisions **one StatefulSet per role** (coordinators, data instances) with headless Services, deriving per-pod identity (coordinator ID, advertised addresses) from pod ordinals — no per-instance configuration blocks.
- **Bootstraps the HA cluster**: adds coordinators, registers data instances, and promotes the initial MAIN.
- **Continuously reconciles registration**: every reconcile compares `SHOW INSTANCES` on the coordinator leader against the declared topology and re-issues only the missing registrations, so a wiped or rescheduled pod rejoins the cluster without human action.
- **Reports cluster state** on the CR status: which instance is MAIN, registration convergence, and readiness conditions.

The operator is a redesign, not a port of the chart. It will reach feature parity with the HA chart over subsequent releases, after which the chart is frozen and deprecated with a documented migration path. The standalone `memgraph` and `memgraph-lab` charts are unaffected.

This MVP is deliberately "provision, bootstrap, observe": both replica counts are immutable after creation, and day-2 operations are excluded.

## User Stories

1. As a database operator, I want to declare my entire HA cluster as a single `MemgraphCluster` resource, so that the cluster topology lives in one reviewable, GitOps-committable object.
2. As a database operator, I want to set the number of coordinators and data instances as single integer fields, so that I don't copy-paste per-instance configuration blocks.
3. As a database operator, I want the operator to register all coordinators and data instances automatically after the pods start, so that I never run mgconsole registration commands by hand.
4. As a database operator, I want the operator to promote an initial MAIN automatically during bootstrap, so that the cluster is writable without manual promotion.
5. As a database operator, I want a data instance that lost its registration (e.g. after rescheduling onto a fresh node) to be re-registered automatically, so that transient infrastructure events don't silently degrade my cluster.
6. As a database operator, I want the operator to leave failover decisions entirely to the Raft coordinators after bootstrap, so that two control systems never fight over which instance is MAIN.
7. As a database operator, I want to see which instance is currently MAIN in the CR status, so that I can inspect cluster health with `kubectl get mgc` instead of querying coordinators.
8. As a database operator, I want status conditions telling me whether the cluster is converged (all declared instances registered and healthy), so that my monitoring and GitOps tooling can gate on it.
9. As a database operator, I want to specify the Memgraph image repository, tag, and pull policy, so that I control exactly which Memgraph version runs.
10. As a database operator, I want to reference my existing Kubernetes Secret for the enterprise license and organization name (configurable secret name and key names, matching the chart's `secrets` block), so that credentials never appear in the CR and my current Secret works unchanged.
11. As a database operator, I want to configure PVC size, access mode, and storage class for lib and log storage per role, so that storage matches my cluster's capabilities.
12. As a database operator, I want to choose whether PVCs are retained or deleted when the CR is deleted (default: retained), so that production data survives accidental deletion while dev clusters clean up after themselves.
13. As a database operator, I want to configure resource requests and limits per role, so that pods are scheduled and bounded appropriately.
14. As a database operator, I want to tune probe timings per role (startup, readiness, liveness), so that large snapshot restores don't get killed mid-load.
15. As a database operator, I want to set custom labels on pods, StatefulSets, and Services per role, so that the resources integrate with my organization's selectors and policies.
16. As a database operator, I want to configure the internal ports (bolt, management, replication, coordinator), so that I can resolve port conflicts with other workloads or policies.
17. As a database operator, I want to set the cluster domain used in advertised FQDNs, so that the operator works on clusters with a non-default DNS domain.
18. As a database operator, I want to pass additional non-secret Memgraph flags and environment variables per role, so that I can use any Memgraph flag without waiting for a typed CRD field.
19. As a database operator, I want attempts to change the coordinator or data-instance count on a live cluster to be rejected at admission time with a clear message, so that I cannot accidentally trigger an unsupported topology change in v1.
20. As a database operator, I want the operator's registration logic to be idempotent (read cluster state before issuing commands), so that an operator crash or restart mid-reconcile causes no harm.
21. As a database operator, I want to install the operator itself with a Helm chart from the existing `memgraph.github.io/helm-charts` repository, so that I use the same helm repo I already have configured.
22. As a database operator, I want the operator to run namespaced with least-privilege RBAC generated by the install chart, so that it passes my cluster's security review.
23. As a platform engineer, I want the operator to run as a non-root user with a restricted security context (matching Memgraph's uid 101 / gid 103 conventions for workload pods), so that it complies with restricted Pod Security Standards.
24. As a platform engineer, I want the CR to be safe to store in git (no secret material in spec or status), so that GitOps workflows need no redaction.
25. As a developer evaluating Memgraph, I want a minimal `MemgraphCluster` example that boots a working cluster with only image, counts, and a license secret reference, so that first contact takes minutes.
26. As a Memgraph chart user planning migration, I want the operator's secret block and knob names to mirror the chart's vocabulary where concepts carry over, so that translating my values file is mechanical.
27. As a contributor, I want the resource-building and registration-planning logic to be pure and unit-testable without a cluster, so that I can develop and verify changes quickly.
28. As a maintainer, I want every pull request gated on unit, envtest, and KinD end-to-end suites, so that broken registration logic never merges.

## Implementation Decisions

### Strategy and repository layout

- The operator ultimately **replaces the HA Helm chart**: build to functional parity, publish a migration guide, then freeze the chart (security fixes only) with a deprecation timeline. Standalone and Lab charts continue independently.
- Two repositories: `helm-charts` stays untouched (its GitHub Pages URL is load-bearing for existing users); the operator lives in the existing `memgraph/kubernetes-operator` repository. The prior contents are a discarded attempt: parked on an archive branch, with a fresh kubebuilder scaffold force-pushed to `main`.
- The **operator install chart lives in the operator repository** (next to the generated CRDs so they can never drift), and the release workflow cross-publishes the packaged chart into the existing `memgraph.github.io/helm-charts` index.
- Implementation is **Go with kubebuilder** — the operator's value is state-aware reconciliation over Bolt, which helm/ansible-based operators cannot express.

### API

- Kind `MemgraphCluster`, group `memgraph.com`, version `v1alpha1`, short name `mgc`.
- v1alpha1 supports **HA topology only**, but topology fields are shaped so a standalone (single-instance) mode can be added later without a breaking API change — no structurally mandatory HA-only fields.
- Topology is declared as **two integer replica counts** (coordinators, data instances). Both are **immutable after creation, enforced by CEL validation** — no webhook needed for this in v1.
- All pods within a role are **uniform**; identity-dependent flags (coordinator ID, advertised addresses) are derived from the StatefulSet pod ordinal.
- Spec knobs in v1: image (repository, tag, pull policy), cluster domain, internal ports, lib/log PVC configuration per role, storage retention policy, probe timings per role, resources per role, labels per role, and a freeform non-secret env/args passthrough per role. Knob vocabulary mirrors the HA chart where the concept carries over.
- License and organization are consumed via a **secret reference block identical in shape to the chart's** (`secrets.name`, `secrets.licenseKey`, `secrets.organizationKey`). A Bolt-auth secret reference joins this block in a later version.
- **No Memgraph version enforcement**: the image tag is plain user input; the operator assumes the HA query surface (`SHOW INSTANCES`, `REGISTER INSTANCE`, `ADD COORDINATOR`, `SET INSTANCE TO MAIN`) is stable across versions.
- Storage: `storage.retentionPolicy` (`Retain` | `Delete`, default `Retain`) maps directly onto the StatefulSet PVC retention policy (`whenDeleted`). The operator carries **no finalizer-based storage cleanup** — no destructive code paths in v1.

### Workload architecture

- **One StatefulSet for all coordinators and one for all data instances**, each backed by a headless Service — a deliberate redesign away from the chart's StatefulSet-per-instance model.
- Workload pods keep the established Memgraph security conventions: non-root (uid 101 / gid 103), seccomp RuntimeDefault, all capabilities dropped.

### Reconciliation semantics

- The reconcile loop does **continuous registration reconciliation, hands-off leadership**: each reconcile queries `SHOW INSTANCES` on the coordinator leader, diffs against declared topology, and issues only the missing `ADD COORDINATOR` / `REGISTER INSTANCE` commands. Registration state that a pod loses is restored automatically.
- The operator issues `SET INSTANCE TO MAIN` **exactly once, at bootstrap** (when no MAIN exists). After that, failover belongs to the Raft coordinators; the operator only observes and reports MAIN in status.
- Reconciliation is **idempotent and read-before-write**: cluster state is always queried before commands are issued, so operator crashes or restarts mid-reconcile are harmless.
- The CR status carries the observed MAIN instance and convergence/readiness conditions.

### Module structure

Seven modules, with two deep pure cores and one mock seam:

1. **API types** — `MemgraphCluster` spec/status types with CEL markers; generated CRD manifests. The public contract.
2. **Resource builders** — pure functions from spec to desired Kubernetes objects (StatefulSets, headless Services, PVC templates, probes, ordinal-derived args, secret/env wiring). No API calls, no side effects.
3. **Memgraph HA client** — a narrow Go interface (show instances, register instance, add coordinator, set main) over the Bolt driver. All higher layers depend on the interface, never the driver — this is the mock seam for testing.
4. **Registration planner** — pure diff logic: declared topology plus observed instances in, ordered registration commands out (empty when converged). The reconciliation semantics above live here.
5. **Controller** — reconciler wiring: fetch CR, server-side-apply builder output, gate on pod readiness, run planner against the HA client, write status and conditions.
6. **Operator install chart** — CRDs, RBAC, controller Deployment; cross-published to the existing helm repo index at release time.
7. **E2E harness** — multi-node KinD suite booting a real licensed Memgraph cluster.

## Testing Decisions

- A good test asserts **external behavior, not implementation**: given a spec, the right Kubernetes objects exist; given a topology and an observed cluster state, the right registration commands (and only those) are planned; given a degraded real cluster, it converges. Tests never assert on internal call ordering or private state.
- **Resource builders**: golden-style unit tests — spec in, expected objects out — covering defaults, overrides, and ordinal-derived identity.
- **Registration planner**: pure unit tests over topology-diff cases — fresh cluster bootstrap, single lost registration, fully converged no-op, coordinator missing, MAIN already present (no promotion issued).
- **Controller**: envtest (real kube-apiserver, no kubelet) with the HA client mocked behind its interface — asserting created resources, status updates, and that immutability violations are rejected.
- **E2E**: KinD multi-node cluster, real Memgraph images, enterprise license supplied via repository secrets (established practice from the HA chart's CI); asserts a `MemgraphCluster` reaches a registered, MAIN-elected state, and that deleting a registered pod's state leads to automatic re-registration.
- **CI gate**: unit, envtest, and e2e suites all run on every pull request. Chaos/soak testing is explicitly deferred to the separate HA chaos-testing project.
- Prior art: the HA chart's CI already boots licensed multi-node clusters (Minikube) with the license as a repository secret; the e2e suite follows that pattern on KinD.

## Out of Scope

- **Day-2 operations**: rolling/no-downtime upgrades, scaling (both counts are immutable in v1), backup/restore orchestration, storage-mode changes.
- **External access** of any kind (LoadBalancer, NodePort, ingress, gateway) — deliberately deferred because the approach is expected to change; v1 is in-cluster access only.
- **Embedded ingress-nginx controller installation** — permanently dropped, not just deferred; users bring their own ingress controller.
- **TLS** (bolt and intra-cluster) — later version.
- **Monitoring** (Prometheus exporter, ServiceMonitor, Grafana dashboards, vmagent/Vector integrations) — later version.
- **Bolt authentication support** (and the operator authenticating its own connections) — later version.
- **Standalone (non-HA) topology** — designed-for but not implemented in v1alpha1.
- **In-place adoption of chart-deployed clusters** — never; migration is fresh-cluster only (backup/restore or replication cutover), documented when parity is reached.
- **Affinity strategies, tolerations, init containers, sidecar/user containers, core-dump handling, snapshot-restore fields** from the chart — parity roadmap, not MVP.
- **Memgraph version compatibility logic** — no version parsing, gating, or branching.
- **Coordinator or data-instance removal** (`REMOVE COORDINATOR`, `UNREGISTER INSTANCE`) — arrives with mutable counts post-v1.

## Further Notes

- Parity with the HA chart is the milestone for *deprecating the chart*, not for the first release; the MVP ships early as an alpha to validate the reconciliation core while parity features land incrementally.
- Post-v1 roadmap order (indicative): data-instance scale-up (registration on grow), then scale-down with MAIN-safety, then external access, TLS, monitoring, orchestrated upgrades.
- The chaos-testing project (ArgoCD, ChaosMesh, VictoriaMetrics on EKS) is the natural proving ground for the operator's re-registration behavior once both exist.
- The full decision log behind this PRD was produced in a structured design interview on 2026-07-23 (15 resolved decisions).
