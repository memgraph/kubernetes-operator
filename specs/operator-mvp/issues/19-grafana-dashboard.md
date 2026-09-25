# Grafana dashboard ConfigMap

**Type**: AFK

## Parent

`specs/operator-mvp/PRD.md` — the second half of the monitoring version scoped on 2026-09-25; the first half is `18-openmetrics-servicemonitor.md`.

## What to build

Let a cluster ask the operator to provision the HA chart's "Memgraph OpenMetrics" Grafana dashboard the way the chart does: as a ConfigMap a Grafana sidecar discovers by label. `spec.monitoring.grafanaDashboard` is an optional struct, presence-based like `serviceMonitor` beside it. Present, the operator creates one ConfigMap in the cluster's namespace holding the dashboard JSON; absent again, the ConfigMap is pruned through the same marker-label path issue 18 built.

The JSON comes from `charts/memgraph-high-availability/dashboards/memgraph_openmetrics.json` in `memgraph/helm-charts`, about 460 KB, comfortably under the 1 MiB ConfigMap limit. It is copied into this repository and compiled into the binary with `go:embed`, with a comment naming the path it was copied from; updating it is a manual copy when the chart's dashboard changes, done as part of a release. Fetching it at runtime would make reconciliation depend on the network, and a submodule or sync script is machinery for a file that changes a few times a year. The dashboard binds to a datasource template variable, so it needs no per-cluster edit.

Two knobs. `labels` defaults to `grafana_dashboard: "1"`, the label the kube-prometheus-stack sidecar selects on, so an empty block works out of the box; a user whose sidecar selects on something else overrides it. `annotations` is free-form, and has a real consumer here: the sidecar reads `grafana_folder` to file the dashboard. No `namespace`, for the reason issue 18 gives; the sidecar watches other namespaces when told to (`sidecar.dashboards.searchNamespace: ALL`), and the docs say so.

Two things about running it. The controller server-side-applies every desired object on every pass, and a converged cluster resyncs every 30 seconds, so the dashboard is 460 KB sent to the API server per cluster per resync, merged server-side with no etcd write when nothing changed. That is applied uniformly like everything else rather than skipped when the cached copy matches; around a megabyte a minute per cluster is noise next to the Prometheus scraping it, and a second code path for a cost nobody has measured is not worth having until someone sees it on a graph. Owning ConfigMaps also means watching them, and unlike Services there is a ConfigMap in every namespace, so the manager's ConfigMap informer is scoped by the managed-by label the way the Pod informer already is. A `configmaps` RBAC marker with the same verbs as the other pruned kinds joins the controller.

Tests: a golden test pins the ConfigMap's shape with both knobs set and the default label with neither; envtest covers creation and prune; the e2e asserts the ConfigMap appears with the label on a cluster with the block and disappears when the block goes. Nothing in status.

## Acceptance criteria

- [ ] `spec.monitoring.grafanaDashboard` is an optional struct with `labels` (default `grafana_dashboard: "1"`) and `annotations`; no `enabled`, no `namespace`
- [ ] The dashboard JSON is embedded with `go:embed`, with a comment naming its origin in `memgraph/helm-charts`
- [ ] Present, one ConfigMap named after the cluster is created in the cluster's namespace with the JSON under `memgraph_openmetrics.json`, carrying the labels, the annotations, the monitoring marker and a controller owner reference; golden test pins the shape and the default label
- [ ] Removing the block prunes the ConfigMap; envtest covers creation and prune
- [ ] The manager's ConfigMap cache is restricted to `app.kubernetes.io/managed-by=memgraph-operator`; the `configmaps` RBAC marker is added and `make manifests generate chart-sync` regenerated with `make chart-verify` green
- [ ] E2E: the ConfigMap appears with the label on a cluster with the block and is gone after the block is removed
- [ ] `docs/monitoring.md` gains the dashboard section: the default label, `grafana_folder`, the sidecar namespace setting, and how the JSON is updated

## Blocked by

- `18-openmetrics-servicemonitor.md`
