# Memgraph Kubernetes Operator

A Kubernetes operator for running [Memgraph](https://memgraph.com) high-availability clusters. It exposes a `MemgraphCluster` custom resource (API group `memgraph.com/v1alpha1`, short name `mgc`): declare the cluster topology in a single resource and the operator provisions the workloads, bootstraps HA registration, and continuously reconciles registration state.

> **Status: early development (v1alpha1).** The API and its guarantees can still change between releases. Read [what v1alpha1 does and does not do](#what-v1alpha1-does-and-does-not-do) before running it anywhere that matters. The previous attempt at this operator is preserved on the `archive/pre-operator-mvp` branch; the requirements and issue slices driving the current work live in [`specs/operator-mvp/`](specs/operator-mvp/PRD.md).

## Description

The operator replaces the `memgraph-high-availability` Helm chart's fire-and-forget registration Job with a controller that continuously drives the cluster toward its declared topology: one StatefulSet per role (coordinators, data instances), automatic bootstrap and MAIN promotion, and automatic re-registration of instances that lose their registration state. See the [PRD](specs/operator-mvp/PRD.md) for the full design.

## Quickstart

From an empty cluster to a registered, MAIN-elected Memgraph HA cluster.

**You need:** a Kubernetes cluster (v1.25 or newer — the CRD uses CEL validation), `kubectl`, `helm` v3, and a Memgraph enterprise license, which high availability requires. Three coordinators and two data instances need five schedulable pods and ten PersistentVolumeClaims of 1Gi each.

### 1. Install the operator

The install chart ships the `MemgraphCluster` CRD, a least-privilege RBAC set, and the controller Deployment. One release per cluster is enough — the operator watches every namespace.

```sh
helm repo add memgraph https://memgraph.github.io/helm-charts
helm repo update
helm install memgraph-operator memgraph/memgraph-operator \
  --namespace memgraph-operator-system --create-namespace --wait
```

### 2. Create the license Secret

The cluster reads its license from a Secret you own; no license material ever goes into the `MemgraphCluster` resource, which keeps it safe to commit to git.

```sh
kubectl create namespace memgraph
kubectl create secret generic memgraph-secrets \
  --namespace memgraph \
  --from-literal=MEMGRAPH_ENTERPRISE_LICENSE='<your-license-key>' \
  --from-literal=MEMGRAPH_ORGANIZATION_NAME='<your-organization-name>'
```

### 3. Declare the cluster

This is the whole resource — counts, image, and the Secret from the previous step. Everything else (storage, ports, probes, resources, cluster domain) takes its default:

```yaml
apiVersion: memgraph.com/v1alpha1
kind: MemgraphCluster
metadata:
  name: memgraph
spec:
  coordinators: 3
  dataInstances: 2
  image:
    repository: docker.io/memgraph/memgraph
    tag: "3.12.0"
  secrets:
    name: memgraph-secrets
    licenseKey: MEMGRAPH_ENTERPRISE_LICENSE
    organizationKey: MEMGRAPH_ORGANIZATION_NAME
```

It is [`examples/minimal-cluster.yaml`](examples/minimal-cluster.yaml) in this repository, and the end-to-end suite applies that file unmodified on every pull request, so it stays a manifest that works:

```sh
kubectl apply -n memgraph \
  -f https://raw.githubusercontent.com/memgraph/kubernetes-operator/main/examples/minimal-cluster.yaml
```

If your Secret has a different name, or stores the license under different keys, change the `secrets` block to match — that is the only edit the example needs.

### 4. Watch it converge

The operator creates one StatefulSet and one headless Service per role, waits for the pods to become ready, then registers the coordinators and data instances with each other and promotes the initial MAIN. On a cluster that has to pull the Memgraph image, expect a few minutes.

```sh
kubectl get mgc -n memgraph -w
```

While the pods are still starting, `MAIN` is empty and both conditions are `False`; the converged cluster looks like this:

```
NAME       COORDINATORS   DATA   MAIN         READY   CONVERGED   AGE
memgraph   3              2      instance_0   True    True        4m12s
```

- **`MAIN`** is the data instance the coordinators elected as MAIN — the one that accepts writes. It is observed, not decided by the operator, so it changes on failover.
- **`READY`** is True once a MAIN is elected, i.e. the cluster serves writes.
- **`CONVERGED`** is True once every declared coordinator and data instance is registered and reported healthy.

To block a script or a GitOps step on the cluster being usable:

```sh
kubectl wait --namespace memgraph --for=condition=Converged \
  memgraphcluster/memgraph --timeout=10m
```

If it does not converge, `kubectl describe mgc memgraph -n memgraph` gives the condition messages (which pods are not ready, whether a coordinator is unreachable), and the operator logs the rest:

```sh
kubectl logs -n memgraph-operator-system deploy/memgraph-operator-controller-manager
```

The resource's identities follow the pod ordinals. For a cluster named `memgraph`:

| Pod | Registered as | Role |
| --- | --- | --- |
| `memgraph-coordinator-0`, `-1`, `-2` | `coordinator_1`, `coordinator_2`, `coordinator_3` | Raft coordinators |
| `memgraph-data-0`, `-1` | `instance_0`, `instance_1` | data instances (one MAIN, the rest replicas) |

Ask a coordinator for the cluster's own view of itself:

```sh
kubectl exec -n memgraph memgraph-coordinator-0 -c memgraph -- \
  bash -c "echo 'SHOW INSTANCES;' | mgconsole"
```

### 5. Connect over Bolt

Every pod runs Bolt on port 7687, and the Memgraph image ships `mgconsole`, so the shortest path to a query is to pipe Cypher into the MAIN pod (`instance_0` above is `memgraph-data-0`):

```sh
kubectl exec -i -n memgraph memgraph-data-0 -c memgraph -- mgconsole <<'EOF'
CREATE (:Greeting {text: "hello from the operator"});
MATCH (n:Greeting) RETURN n;
EOF
```

`kubectl exec -it -n memgraph memgraph-data-0 -c memgraph -- mgconsole` opens the same client interactively.

Writes only succeed against the MAIN — the other data instances are replicas and accept reads. Check `.status.main` to find it:

```sh
kubectl get mgc memgraph -n memgraph -o jsonpath='{.status.main}'
```

Applications inside the cluster reach an instance at its stable DNS name in the role's headless Service:

```
memgraph-data-0.memgraph-data.memgraph.svc.cluster.local:7687
```

v1alpha1 ships no external access (see below), so to point a local client such as [Memgraph Lab](https://memgraph.com/docs/data-visualization) at the cluster while evaluating, forward the port:

```sh
kubectl port-forward -n memgraph pod/memgraph-data-0 7687:7687
```

### 6. Clean up

```sh
kubectl delete mgc memgraph -n memgraph
```

Deleting the resource removes the StatefulSets and Services through garbage collection, but **the PersistentVolumeClaims are kept** — the default retention policy protects data against an accidental delete. Remove them (and with them the data) explicitly, or set `spec.storage.retentionPolicy: Delete` on dev clusters that should clean up after themselves:

```sh
kubectl delete pvc -n memgraph --all
kubectl delete namespace memgraph
```

Uninstall the operator with `helm uninstall memgraph-operator --namespace memgraph-operator-system`. Helm never deletes CRDs it installed, so `kubectl delete crd memgraphclusters.memgraph.com` once no cluster needs it — see the [chart README](charts/memgraph-operator/README.md) for the values and the upgrade caveat.

## Configuration

Beyond the quickstart's four fields, v1alpha1 exposes storage (PVC size, access mode, storage class, retention) per role, resource requests and limits per role, probe timings per role, custom labels on pods, StatefulSets and Services, the internal ports, the cluster domain used in advertised addresses, and a freeform environment-variable and Memgraph-flag passthrough per role.

[`config/samples/v1alpha1_memgraphcluster.yaml`](config/samples/v1alpha1_memgraphcluster.yaml) spells the full surface out with every default and the reasoning behind it. `kubectl explain mgc.spec --recursive` documents the same fields from the installed CRD.

## What v1alpha1 does and does not do

The MVP is deliberately "provision, bootstrap, observe". It does:

- provision one StatefulSet and headless Service per role, with per-pod identity derived from the pod ordinal;
- bootstrap HA: add the coordinators, register the data instances, and promote the initial MAIN once;
- re-register continuously: every reconcile compares `SHOW INSTANCES` on the coordinator leader against the declared topology and issues only the missing registrations, so an instance that loses its registration state (say, after being rescheduled onto a fresh node) rejoins without human action;
- report the observed MAIN and the readiness and convergence conditions on the resource's status.

What it does not do yet:

- **Scaling.** `coordinators` and `dataInstances` are **immutable after creation** — admission rejects a change with a clear message. Changing the topology means creating a new cluster. Mutable counts are the first item on the post-v1 roadmap.
- **Failover.** The operator issues `SET INSTANCE TO MAIN` exactly once, at bootstrap, when no MAIN exists. After that, leadership belongs entirely to the Raft coordinators; the operator only observes and reports it, so two control systems never fight over which instance is MAIN.
- **Other day-2 operations**: orchestrated or rolling version upgrades, backup and restore, storage-mode changes.
- **Removing instances**: there is no `REMOVE COORDINATOR` or `UNREGISTER INSTANCE`, and no finalizer-based storage cleanup. The operator has no destructive code path.
- **External access** of any kind — no LoadBalancer, NodePort, ingress or gateway. Access is in-cluster (or `kubectl port-forward`) only; the approach is expected to change, so it was deliberately deferred rather than shipped and broken later.
- **TLS**, for Bolt or intra-cluster traffic.
- **Bolt authentication** — the operator connects to the coordinators unauthenticated, so clusters must not enable auth yet.
- **Monitoring** of the Memgraph cluster: no exporter, ServiceMonitor or dashboards. (The operator itself serves controller-runtime metrics; see the [chart README](charts/memgraph-operator/README.md).)
- **Standalone (non-HA) topology.** The API is shaped to grow one without a breaking change, but v1alpha1 provisions HA clusters only.
- **Affinity, tolerations, init containers, sidecars, snapshot-restore fields** and the rest of the HA chart's surface — parity roadmap, not MVP.

## Relationship to the memgraph-high-availability chart

This operator is the successor to the [`memgraph-high-availability`](https://github.com/memgraph/helm-charts) Helm chart. The plan is to grow it to functional parity with the chart, publish a migration guide, and then freeze the chart (security fixes only) with a deprecation timeline. Until then the chart remains the supported way to run HA in production, and this operator is an alpha for evaluating the reconciliation core. The standalone `memgraph` and `memgraph-lab` charts are unaffected and continue independently.

Where a concept carries over, the operator borrows the chart's vocabulary — the `secrets.name` / `secrets.licenseKey` / `secrets.organizationKey` block is the chart's block — so translating a values file is mechanical. The topology is where they deliberately differ: the chart's per-instance blocks and StatefulSet-per-instance model become two integers and one StatefulSet per role.

**Migration is fresh-cluster only.** The operator will never adopt a chart-deployed cluster in place: the resources are shaped differently and the ownership handover cannot be made safe. Moving means standing up a new cluster and transferring the data (backup/restore, or a replication cutover). The step-by-step guide lands when the operator reaches parity — there is nothing to migrate to before then.

## Development

```sh
make test-unit     # unit tests (pure packages)
make test          # unit + envtest
make lint          # golangci-lint
make run           # run the controller locally against the current kubeconfig
make test-e2e      # KinD end-to-end suite; creates and deletes its own Kind cluster
```

Every pull request runs lint, unit, envtest, chart and end-to-end suites. The e2e job boots a licensed Memgraph cluster on a multi-node Kind cluster, with the license coming from repository secrets; set `MEMGRAPH_ENTERPRISE_LICENSE` and `MEMGRAPH_ORGANIZATION_NAME` to run it locally.

Run a development build against a cluster with `make docker-build docker-push IMG=<registry>/kubernetes-operator:tag` followed by `make install` (CRDs) and `make deploy IMG=<registry>/kubernetes-operator:tag`, or install the local chart:

```sh
helm install memgraph-operator ./charts/memgraph-operator \
  --namespace memgraph-operator-system --create-namespace --wait
```

The install chart is maintained in this repository under [`charts/memgraph-operator`](charts/memgraph-operator/README.md), next to the manifests it ships: its CRDs and the manager's RBAC rules are generated from the Go types and the `+kubebuilder:rbac` markers (`make chart-sync`, verified in CI by `make chart-verify`), so the chart can never drift from the controller version it installs. Pushing a version tag cross-publishes the packaged chart into the [`memgraph.github.io/helm-charts`](https://memgraph.github.io/helm-charts) index. The chart version and the operator version move independently — `v0.2.0` releases the operator, `chart-0.4.2` releases the chart alone. See [`docs/releasing.md`](docs/releasing.md).

Development is sliced into PR-gated issues under [`specs/operator-mvp/issues/`](specs/operator-mvp/issues). Run `make help` for all targets, and see the [Kubebuilder documentation](https://book.kubebuilder.io/introduction.html) for the scaffolding conventions this project follows.

## License

Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
