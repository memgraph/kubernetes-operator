# Monitoring: scraping a cluster with Prometheus

Every instance of a `MemgraphCluster` serves metrics in the OpenMetrics text format, and `spec.monitoring` creates the objects a monitoring stack you already run discovers the cluster by — or the vmagent and the Vector sidecars that push the metrics and the logs to Memgraph, so Memgraph can monitor the cluster for you. This document is the contract: what is served without asking, what the block creates, what a cluster without Prometheus Operator reports, and what is deliberately left out.

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
- **Endpoint.** Port `metrics`, path `/metrics`, and `interval` only when the spec sets one. The scheme is `http`, or `https` with `tlsConfig.insecureSkipVerify: true` on a cluster with `spec.tls.bolt`: Memgraph serves metrics from the Bolt server context, so the endpoint follows that block (see [`docs/tls.md`](tls.md)).
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

## `spec.monitoring.vmagentRemote`

```yaml
spec:
  monitoring:
    vmagentRemote:
      remoteWrite:
        url: http://vmsingle.monitoring.svc.cluster.local:8428/api/v1/write
        basicAuth:                        # optional
          secretName: monitoring-basic-auth
      scrapeInterval: 15s                 # the default
      externalLabels:                     # optional; how the remote tells clusters apart
        cluster: production
      image:                              # optional; defaults to victoriametrics/vmagent at the pinned tag
        repository: docker.io/victoriametrics/vmagent
        tag: v1.139.0
      resources: {}
```

The HA chart's `vmagentRemote`, which is how Memgraph monitors a customer's cluster. Memgraph cannot reach into your network to scrape the cluster, so the cluster pushes its metrics the other way: a vmagent beside it scrapes every instance and remote-writes the samples, outbound only, to the VictoriaMetrics Memgraph runs, at the URL and with the credentials Memgraph gives you. The same block works for any Prometheus remote-write endpoint, such as a monitoring cluster your own platform team runs. Presence-based like the blocks above: present, the operator runs one vmagent in the cluster's namespace that scrapes every instance's OpenMetrics endpoint and remote-writes the samples to `remoteWrite.url`; removed, the vmagent is deleted. `remoteWrite.url` is the one required field. Nothing about the Memgraph pods changes: the vmagent scrapes the same 9091 the ServiceMonitor names, and both blocks can be on at once (the chart warns about scraping twice because its vmagent scrapes the exporter; here there is no exporter and two scrapers of an endpoint are harmless).

What the operator creates:

- **A Deployment `<cluster>-vmagent`**, one replica, carrying the monitoring marker and a controller owner reference like the ServiceMonitor. The container runs under the restricted security context every container the operator builds runs under, as uid 65534 (the vmagent image names no user of its own), mounts no ServiceAccount token because it needs nothing from the API server, buffers samples the endpoint has not accepted yet on an emptyDir, and reports ready on vmagent's own `/health`.
- **A ConfigMap `<cluster>-vmagent-config`** holding the scrape configuration under `scrape.yml`: one job `memgraph` with a static target per pod the operator runs, addressed by pod DNS on the metrics port (`<cluster>-coordinator-0.<cluster>-coordinator.<namespace>.svc.<clusterDomain>:9091`, and so on), `scrapeInterval` and `externalLabels` under `global`. The targets follow the pods the operator runs, so a scale-up or scale-down rewrites the file; vmagent re-reads it once the kubelet has synced the mount, within a minute or two, with no pod restart and no loss of the buffer. The scheme follows `spec.tls.bolt` the way the ServiceMonitor's does: `https` with `insecure_skip_verify` the moment that block is set.

Changing `remoteWrite.url`, the image or the credentials Secret is a pod-template change the Deployment rolls itself. Nothing lands in the resource's status: whether the endpoint accepts the writes is vmagent's to report, in its logs and on its own metrics (`vmagent_remotewrite_*` on port 8429 inside the pod).

### Credentials

`basicAuth.secretName` names a Secret in the cluster's namespace with the keys **`username`** and **`password`** — the keys of a `kubernetes.io/basic-auth` Secret, which is what the chart's `usernameKey` and `passwordKey` default to; here they are fixed, for the reason the TLS Secrets' keys are. Create it with

```sh
kubectl create secret generic monitoring-basic-auth --namespace <cluster namespace> \
  --type=kubernetes.io/basic-auth --from-literal=username=... --from-literal=password=...
```

The password is mounted into the vmagent pod read-only and vmagent reads it from the file, re-reading it every second, so a rotated password takes effect with no restart and is never on the command line (the chart's vmagent takes it through `$(MONITORING_PASSWORD)`, where `ps` shows it). The username is not the secret part: it reaches vmagent through an environment variable from the same Secret, and a changed username rolls the pod. The operator never reads the Secret: it holds no Secret permissions at all.

### What is not a knob

- **`namespace`**, for the reason the ServiceMonitor has none; the vmagent runs beside the cluster and reaches the endpoint over the network like any client would.
- **`httpPort`**: vmagent listens on 8429 inside the pod and nothing outside the pod dials it.
- **The scrape scheme**, which follows `spec.tls.bolt`.
- **`kubernetes.*`**: see below.

## `spec.monitoring.vectorRemote`

```yaml
spec:
  monitoring:
    vectorRemote:
      logsEndpoint: http://victoria-logs.monitoring.svc.cluster.local:9428/insert
      auth:                               # optional
        secretName: logs-basic-auth
      extraLabels:                        # optional; how the remote tells clusters apart
        cluster_id: production
      image:                              # optional; defaults to timberio/vector at the pinned tag
        repository: docker.io/timberio/vector
        tag: 0.49.0-debian
      resources: {}
```

The HA chart's `vectorRemote`, the logs half of how Memgraph monitors a customer's cluster: a Vector sidecar in every pod of both roles reads the instance's own log stream and pushes it, outbound only, to the Loki-compatible endpoint Memgraph gives you — the VictoriaLogs it runs — or to any Loki. Presence-based: present, both roles' pods carry the sidecar; removed, they do not. `logsEndpoint` is the one required field, a base URL to which Vector appends `/loki/api/v1/push` itself; for VictoriaLogs that is its `/insert` path, as above.

How it works:

- **The source is Memgraph itself.** Every instance, both roles, serves a monitoring websocket on port 7444 (Memgraph's `--monitoring-port` default) that streams its log lines as they are written, at whatever `--log-level` the instance runs; there is no switch to turn it off. Only lines written after the sidecar connects are shipped, so an instance's earliest startup lines stay in its container log, and an idle coordinator may ship nothing for a while: NuRaft's own logging goes to `--nuraft-log-file`, not through the stream. The sidecar dials it inside the pod, so no Service, no port on one and no API access is involved, and nothing about the Memgraph container changes. The operator pins `--monitoring-port` and `--monitoring-address`, and admission rejects both in `spec.flags`: the sidecar dials `127.0.0.1:7444`. The websocket shares the Bolt TLS context, so on a cluster with `spec.tls.bolt` the sidecar dials `wss` without verifying a certificate that names no loopback address, for the reason the operator's own dials do not verify.
- **One configuration for both roles**, in a ConfigMap `<cluster>-vector-config` the operator owns and prunes with the block. Everything that differs per pod reaches Vector through the environment: the role, the pod and namespace names, and the credentials.
- **The labels are the chart's**, so the dashboards Memgraph keeps for chart users work unchanged: `app` and `job` are `memgraph`, `role` is `coordinator` or `data`, `namespace` and `pod` are the pod's, `level` is Memgraph's level with `warning` and `critical` renamed to `warn` and `fatal` as the chart does, plus `extraLabels` as written (`cluster_id`, `service_name` and `cluster_env` are what Memgraph's dashboards filter on). A key the operator sets itself is the operator's. Memgraph frames each line as JSON, but escapes only quotes and newlines, so a line carrying a backslash is kept whole with `level=unknown` rather than dropped.
- **The sidecar runs under the pod's identity** and the restricted security context every operator container runs under, with its state on an emptyDir because its root filesystem is read-only. It carries no probe: a sidecar's readiness must never gate the pod's, which is what registration waits on. It starts before Memgraph listens and retries the websocket until it does; the operator raises Vector's connect timeout to a day, because the default 30 seconds bounds the whole retry loop and makes Vector exit when an instance takes longer than that to recover its databases and open the port.
- **Adding or removing the block is a pod-template change**, which the roll carries through the cluster one pod at a time like any other. Nothing lands in the resource's status: whether the endpoint accepts the lines is Vector's to report, in its own container's logs (`kubectl logs <pod> -c vector`).

### Credentials

`auth.secretName` names a Secret in the cluster's namespace with the keys **`username`** and **`password`**, as for the [vmagent](#credentials). Both reach Vector through environment variables it interpolates into its configuration (`${LOGS_USERNAME}`, `${LOGS_PASSWORD}`), so neither is on a command line or in the ConfigMap; a rotated Secret takes effect when the pod next restarts, which a roll of your choosing provides. The operator never reads the Secret.

### What is not a knob

- **`data` and `coordinators`**, the chart's per-role switches: the sidecar runs on both roles, because a coordinator's log is where a failover decision is explained.
- **`websocketPort`**: the sidecar dials Memgraph's default, which the operator leaves alone.
- **The scrape scheme**, which follows `spec.tls.bolt`.
- **`namespace`** on anything, for the reason the ServiceMonitor has none.
- **The chart's `grafanaDashboard`** for logs: see below.

## Not in scope

- **The `mg-exporter`** and JSON metrics the HA chart offers. The operator serves OpenMetrics directly and lets your stack scrape it; its vmagent scrapes Memgraph directly too (the chart's `scrapeMemgraphDirectly: true`), never an exporter.
- **Kubernetes infrastructure metrics through the vmagent** — the chart's `vmagentRemote.kubernetes` block, which has the vmagent also scrape kube-state-metrics, node-exporter and the kubelet (cAdvisor and `/metrics`, through the API server's node proxy) for the kube-prometheus dashboards. Those targets are not this cluster's, and the kubelet job needs a ServiceAccount bound to a cluster-scoped ClusterRole on `nodes`, `nodes/proxy` and `pods` — permissions the operator would then have to hold itself to grant, on objects no owner reference garbage-collects when the cluster goes. A cluster-wide agent is the tool for infrastructure metrics: the VictoriaMetrics operator's `VMAgent`, or kube-prometheus's own, writing to the same remote endpoint.
- **A `scheme` or `tlsConfig` knob** on the ServiceMonitor. Both follow `spec.tls.bolt`, because that is what moves the endpoint to HTTPS; a verified scrape would need pod-IP names on a user-facing certificate, which is why the derived config skips verification (see [`docs/tls.md`](tls.md)).
- **A ServiceMonitor in another namespace.** See above.
- **The chart's "Memgraph Logs" dashboard** as a ConfigMap in your cluster (`vectorRemote.grafanaDashboard`). The logs go to the Grafana on Memgraph's side, which is where that dashboard lives; it needs a VictoriaLogs datasource and the `cluster_id`-labelled metrics the vmagent ships, neither of which a customer's Grafana has. The JSON stays in [memgraph/helm-charts](https://github.com/memgraph/helm-charts) for anyone running their own VictoriaLogs and Grafana.
- **A Prometheus or a Grafana of its own**, or any assertion that one discovers the objects. The e2e suite proves the endpoint answers and the objects are created and pruned; discovery is the stack's contract.
