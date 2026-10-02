# Memgraph flags: a map, a flag file, and no restart for what Memgraph can change live

`spec.flags` passes Memgraph flags to each role by name. This document is the contract: where the flags go, which ones take effect without a restart and how, which ones roll the pods, what may not be set, and what removing a flag does.

```yaml
spec:
  flags:
    coordinators:
      log-level: INFO
    data:
      log-level: INFO
      query-execution-timeout-sec: "120"
      storage-snapshot-on-exit: "false"
      memory-limit: "4096"
```

Keys are flag names without their leading dashes, in either spelling gflags accepts: `log-level` and `log_level` reach the same flag, and admission rejects a map that spells one flag both ways. Values are checked for shape only, one line of at most 4096 characters, with one exception: `log-level` must be one of `TRACE`, `DEBUG`, `INFO`, `WARNING`, `ERROR`, `CRITICAL`, upper case, because it is the flag everyone touches first and Memgraph would otherwise refuse the value only once the instance sees it. Values are strings, so a number or a boolean is quoted; a boolean is `"true"` or `"false"` and nothing else, because that is the only form Memgraph accepts for one at run time. An empty value is a value: `storage-snapshot-interval: ""` turns periodic snapshots off, the way `--storage-snapshot-interval=` does.

## Changing a flag

Edit the map like any other part of the spec, `kubectl edit mgc memgraph` or a patch:

```sh
# A run-time flag: lands on every instance within a reconcile pass, no restart.
kubectl patch mgc memgraph --type=merge -p '{"spec":{"flags":{"data":{"query-execution-timeout-sec":"300"}}}}'
kubectl exec memgraph-data-0 -c memgraph -- bash -c 'echo "SHOW DATABASE SETTING \"query.timeout\";" | mgconsole'

# A startup-only flag: the operator rolls the pods, data instances first, MAIN last of them.
kubectl patch mgc memgraph --type=merge -p '{"spec":{"flags":{"data":{"storage-gc-cycle-sec":"60"}}}}'
kubectl get mgc memgraph -w   # Updated=False while the roll runs, True once every pod is on the new template
```

A merge patch replaces the whole `data` map, so carry the flags you keep; `kubectl edit` is the safer tool for a map with several entries. `kubectl get mgc memgraph -o jsonpath='{.status.conditions}'` shows whether the operator is still owed a setting (`Converged=False`, reasons below), and the rendered flag file is readable at any time:

```sh
kubectl get configmap memgraph-data-flags -o jsonpath='{.data.memgraph\.flags}'
```

## Where the flags go

The flags do not travel on the command line. Every argument of a container is part of its pod template, so a change to one bumps the StatefulSet revision and the operator would then roll every pod — the opposite of what a run-time flag should do. Instead the operator writes each role's flags into a ConfigMap, `<cluster>-coordinator-flags` and `<cluster>-data-flags`, as a gflags flag file:

```
--also_log_to_stderr=true
--log_level=INFO
--log_retention_days=35
--memory_limit=4096
--query_execution_timeout_sec=120
--storage_snapshot_on_exit=false
```

The file is the operator's own defaults — the HA chart's logging defaults, `log-level` `TRACE`, `also-log-to-stderr` `true`, `log-retention-days` `35` — with the role's `spec.flags` merged over them, the role's value winning. It is mounted read-only at `/etc/memgraph-flags/memgraph.flags` and loaded with `--flag-file`, the first flag on the command line. A flag set from the file counts as explicitly set, exactly like one on the command line.

The command line itself carries only what you may not override: the ports and listen addresses, the data directory, the log file, the metrics format, and the TLS file flags when a TLS mode is on. gflags processes `--flag-file` where it stands and takes the last occurrence of a repeated flag, so a pinned flag after the file wins even if a spelling of it got past admission.

Two consequences of the file worth knowing:

- **A misspelt flag name is silently ignored.** gflags skips a flag-file line naming a flag it does not know, where the same flag on the command line would refuse to start. Check `SHOW DATABASE SETTINGS` for a run-time flag, or the instance's startup log for any other, when a flag seems not to take.
- **A bad value is still fatal at startup**, as on the command line: an instance started on `log-level: LOUD` exits, and the pod crash-loops until the flag is fixed. For a run-time flag the same value is caught earlier and more gently, see below.

## Flags Memgraph can change live

Some flags Memgraph also exposes as run-time settings, the ones `SHOW DATABASE SETTINGS` lists and `SET DATABASE SETTING` changes. For those the operator does both halves: it updates the flag file, so the next start reads the new value, and it issues `SET DATABASE SETTING` on every ready pod of the role, so the running instances pick it up now. Nothing restarts. A run-time setting is local to the instance that receives it, so the operator dials every pod over its own address, coordinators and data instances alike, reads its settings first, and sets only what differs.

The flags this covers, with the setting each maps to, as of Memgraph 3.13.0:

| Flag | Setting |
| --- | --- |
| `log-level` | `log.level` |
| `also-log-to-stderr` | `log.to_stderr` |
| `log-min-duration-ms` | `log.min_duration_ms` |
| `log-failed-queries` | `log.failed_queries` |
| `log-query-plan` | `log.query_plan` |
| `query-execution-timeout-sec` | `query.timeout` |
| `storage-snapshot-interval` | `storage.snapshot.interval` |
| `storage-access-timeout-sec` | `storage.access_timeout_sec` |
| `storage-gc-aggressive` | `storage-gc-aggressive` |
| `storage-omit-vector-index-properties-on-return` | `storage.omit_vector_index_properties_on_return` |
| `cartesian-product-enabled` | `cartesian-product-enabled` |
| `debug-query-plans` | `debug-query-plans` |
| `hops-limit-partial-results` | `hops_limit_partial_results` |
| `timezone` | `timezone` |
| `bolt-server-name-for-init` | `server.name` |
| `file-download-conn-timeout-sec` | `file.download_conn_timeout_sec` |
| `aws-region` | `aws.region` |
| `aws-endpoint-url` | `aws.endpoint_url` |

The table lives in the operator (`internal/settings`) and is checked against Memgraph's source on an image bump. On an older Memgraph that does not yet know a setting at run time, the `SET` is refused and reported as below; the flag file still carries the value for the next restart.

Every other flag is read at startup only. A change to one changes the pod template — through an annotation hashing the startup-only flags, so a run-time change leaves the template untouched — and the operator rolls both roles' pods in the usual order: data instances before coordinators, the MAIN last of its role, the Raft leader last of its, each step gated on replication lag. An edit that changes both kinds does both: the run-time values land on every pod at once, and the roll carries the rest.

## What the resource reports

Run-time settings are applied on the pass that finds the cluster registered, before the roll picks its next pod. What that pass could not finish shows on the `Converged` condition:

- `Converged=False` with reason **`SettingsPending`** names the ready pods that did not answer. The usual cause is a Bolt endpoint lagging its pod's readiness by a few seconds; the pass retries on a delay. A pod that is not ready is not dialed and not reported — it is the roll's or the kubelet's business, and the flag file has its settings for when it comes up.
- `Converged=False` with reason **`SettingsRejected`** names the pod, the exact `SET DATABASE SETTING` and Memgraph's error, verbatim: a value the setting's validator refuses (`Boolean value supports only 'false' or 'true'`, `Unsupported log level`), or `Unknown setting name` on a Memgraph that does not have the setting at run time. It is retried every pass and only a changed flag clears it. The operator never escalates to a restart on its own: a value an instance refuses at run time it would refuse at startup too, and crash-loop on.

`Ready` is unaffected by either: a refused log level does not make a MAIN stop serving. `Updated` is the roll's, as before.

## Removing a flag

Removing a key removes its line from the flag file and issues no `SET`. For a startup-only flag that is a template change like any other, and the roll brings every pod onto Memgraph's default. For a run-time flag the running instances keep their current value until they next restart without the flag — and for the few settings Memgraph persists across restarts (`timezone`, `storage-gc-aggressive`, `hops-limit-partial-results`, `storage-omit-vector-index-properties-on-return`, `bolt-server-name-for-init`, `file-download-conn-timeout-sec`, the AWS settings, the three slow-query log settings), even then: Memgraph restores the last value it was set to whenever the flag is not given explicitly, and has no way to reset one to its default. Set the default by hand with `SET DATABASE SETTING`, or write it into the map, if you need it back before the core offers a reset.

One exception, by design: a flag the operator has a default for (`log-level`, `also-log-to-stderr`, `log-retention-days`) goes back to that default when you remove your override, because the flag file then says so and the operator keeps the running instances in line with the file.

## What may not be set

Admission rejects these keys, in either spelling:

- **`bolt-port`, `management-port`, `coordinator-port`, `bolt-address`, `monitoring-address`, `monitoring-port`, `metrics-port`**: the Services, probes, registered addresses, metrics scrapers and the Vector sidecar reach every instance at the fixed ports and on every interface.
- **`coordinator-id`, `coordinator-hostname`**: a coordinator's identity, derived from its pod ordinal.
- **`data-directory`, `log-file`**: where the lib and log claims are mounted. `log-file` follows the log claim: empty when the role has none, which is what keeps a read-only root filesystem from crash-looping.
- **`bolt-cert-file`, `bolt-key-file`, `cluster-cert-file`, `cluster-key-file`, `cluster-ca-file`**: derived from `spec.tls`, see [TLS](tls.md).
- **`metrics-format`**: always OpenMetrics, see [monitoring](monitoring.md).
- **`aws-access-key`, `aws-secret-key`**: the CR carries no secret material. Set them on each instance with `SET DATABASE SETTING "aws.access_key" TO "..."` for now; a Secret reference may follow if there is demand.

Everything else is yours, including the operator's own logging defaults. Memgraph's [configuration reference](https://memgraph.com/docs/database-management/configuration) lists the flags.
