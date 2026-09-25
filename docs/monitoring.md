# Monitoring: scraping a cluster with Prometheus

Every instance of a `MemgraphCluster` serves metrics in the OpenMetrics text format, and `spec.monitoring` creates the objects a monitoring stack you already run discovers the cluster by. This document is the contract: what is served without asking, what the block creates, what a cluster without Prometheus Operator reports, and what is deliberately left out.

## What every cluster serves, with no spec at all

Memgraph's enterprise build starts its metrics HTTP server whether or not anything scrapes it, on both roles. The operator does not switch it on or off — it cannot — but it makes the endpoint a stated part of the pods rather than an accident of the image:

- Both roles' containers declare a port named `metrics` on **9091**, and both headless Services (`<cluster>-coordinator`, `<cluster>-data`) publish it under the same name.
- Both roles run with `--metrics-port=9091` and `--metrics-format=OpenMetrics`. The format is pinned even though Memgraph 3.13 defaults to it: it protects a cluster on a 3.11 or 3.12 image, where JSON is still the default, and it makes the format something the operator states rather than something the image tag happens to ship. JSON is deprecated in Memgraph and the operator never selects it.

So a Prometheus you already run scrapes the cluster with nothing from this block: a `ServiceMonitor` or `PodMonitor` of your own against the `metrics` port, or a plain Kubernetes service-discovery scrape config. That is the intended path for anyone with their own conventions; `spec.monitoring` is for the rest.

The headless Services publish not-ready addresses (the pods need each other's DNS before they are ready), so a recovering instance is scraped and fails. That is the `up=0` a monitoring stack wants to see, rather than a target that vanishes while the instance replays its WAL.

## `spec.monitoring.serviceMonitor`

```yaml
spec:
  monitoring:
    serviceMonitor:
      labels:
        release: kube-prometheus-stack   # what your Prometheus selects ServiceMonitors by
      annotations: {}
      interval: 15s                      # optional; omitted, Prometheus's global default applies
```

The block is presence-based, like `externalAccess`: present, the operator creates one `ServiceMonitor` (`monitoring.coreos.com/v1`) named after the cluster, in the cluster's namespace, and keeps it; removed, the ServiceMonitor is deleted on the next pass. There is no `enabled` field. An empty block (`serviceMonitor: {}`) is valid and creates the object with the operator's own labels alone.

What the object is:

- **Selector.** `app.kubernetes.io/name: memgraph` and `app.kubernetes.io/instance: <cluster>`, which both headless Services carry. The external Services of an exposed cluster carry the same labels, but no `metrics` port, so they yield no targets.
- **Endpoint.** Port `metrics`, path `/metrics`, scheme `http`, and `interval` only when the spec sets one.
- **Labels.** Yours under the operator's identity labels, which win a key collision, plus the marker `memgraph.com/monitoring: "true"` the operator prunes by.
- **Ownership.** A controller owner reference to the `MemgraphCluster`, so deleting the cluster deletes the ServiceMonitor with everything else.

`labels` replaces the HA chart's `kubePrometheusStackReleaseName`: it is whatever your Prometheus's `serviceMonitorSelector` matches. With kube-prometheus-stack that is the release label shown above.

### Why there is no `namespace`

The chart lets you put the ServiceMonitor in the namespace Prometheus runs in. The operator does not, on purpose. It garbage-collects and prunes through owner references, and owner references cannot cross namespaces: a ServiceMonitor elsewhere would leak when the block is removed or the cluster deleted, and the operator would need cluster-wide list and delete rights just to find it. Prometheus Operator solved the same problem from its side. Tell your Prometheus to look in the cluster's namespace:

```yaml
# On the Prometheus object
spec:
  serviceMonitorNamespaceSelector: {}       # every namespace
  # or
  serviceMonitorNamespaceSelector:
    matchLabels:
      kubernetes.io/metadata.name: memgraph
```

With kube-prometheus-stack that is `prometheus.prometheusSpec.serviceMonitorNamespaceSelector`, plus `serviceMonitorSelectorNilUsesHelmValues: false` if you would rather not label the ServiceMonitor with the release name at all.

### A cluster without Prometheus Operator

`ServiceMonitor` is a CRD that belongs to whoever installs Prometheus Operator; the operator never bundles it. When the operator starts it discovers once whether the kind is served at `v1`, and only then watches it. A cluster that asks for `serviceMonitor` on a Kubernetes cluster without the CRD is not served silently: the resource reports

```
Converged=False  reason=ApplyFailed
  The ServiceMonitor kind the operator needs is not served by this cluster
  (ServiceMonitor is not served): it builds monitoring.coreos.com/v1
  ServiceMonitors, which Prometheus Operator installs. Install it, then
  restart the operator
```

while `Ready` and everything else about the cluster are unaffected. Installing the CRD later takes an operator restart, which is documented here rather than detected, exactly as for the Gateway API. Dropping the block clears the condition.

## `spec.monitoring.grafanaDashboard`

```yaml
spec:
  monitoring:
    grafanaDashboard:
      labels:
        grafana_dashboard: "1"     # the default; what your Grafana sidecar selects on
      annotations:
        grafana_folder: Memgraph   # optional; the sidecar files the dashboard here
```

Presence-based like the block above. Present, the operator creates one ConfigMap named `<cluster>-grafana-dashboard` in the cluster's namespace, holding the HA chart's "Memgraph OpenMetrics" dashboard under the key `memgraph_openmetrics.json`; removed, the ConfigMap is deleted. The dashboard binds to a datasource template variable, so it needs no edit to point at your Prometheus. An empty block (`grafanaDashboard: {}`) works with kube-prometheus-stack's sidecar out of the box.

- **`labels`** default to `grafana_dashboard: "1"`, the label the kube-prometheus-stack sidecar selects dashboard ConfigMaps by. A label set you name **replaces** the default rather than adding to it, so a sidecar configured with another label gets exactly that.
- **`annotations`** are free-form; the sidecar reads `grafana_folder` from them to file the dashboard in a Grafana folder.
- **The JSON is compiled into the operator**, copied from `charts/memgraph-high-availability/dashboards/memgraph_openmetrics.json` in [memgraph/helm-charts](https://github.com/memgraph/helm-charts). It changes with operator releases, by copying the chart's file over `internal/resources/dashboards/memgraph_openmetrics.json`, not with the spec. Fetching it at reconcile time would make reconciliation depend on the network for a file that changes a few times a year.
- **No `namespace`**, for the reason the ServiceMonitor has none. The sidecar watches only its own namespace by default; with kube-prometheus-stack, `grafana.sidecar.dashboards.searchNamespace: ALL` (or the cluster's namespace) makes it look here.

The ConfigMap is applied on every reconcile pass like every other object the operator owns, which is about 460 KB to the API server per cluster per 30-second resync and no etcd write when nothing changed. That is noise next to a Prometheus scraping the same cluster, and there is deliberately no second code path that skips the apply when the cached copy matches: it can come if someone sees the cost on a graph.

## Not in scope

- **The `mg-exporter`**, JSON metrics, vmagent remote write and the Vector log sidecar the HA chart offers. The operator serves OpenMetrics directly and lets your stack scrape it.
- **`scheme` and `tlsConfig`** on the ServiceMonitor. They only mean something once Memgraph serves metrics over HTTPS, which the operator has no support for yet. They arrive with TLS, driven by the same spec that turns it on.
- **A ServiceMonitor in another namespace.** See above.
- **The chart's "Memgraph Logs" dashboard**, which reads logs the Vector sidecar ships; without the sidecar there is nothing for it to show.
- **A Prometheus or a Grafana of its own**, or any assertion that one discovers the objects. The e2e suite proves the endpoint answers and the objects are created and pruned; discovery is the stack's contract.
