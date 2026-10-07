# Memgraph flags: a map, a flag file, and no restart for what Memgraph can change live

`spec.flags` passes Memgraph flags to each role by name, and under `coordinators` also the cluster-wide coordinator settings that are not flags at all. This document is the contract: where the keys go, how the operator decides what each one is, which ones take effect without a restart and how, which ones roll the pods, what may not be set, and what removing a key does.

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

A merge patch merges the map key by key: a key it names is set, a key it leaves out is kept, and a key set to `null` is removed, as in `{"spec":{"flags":{"data":{"storage-gc-cycle-sec":null}}}}`. `kubectl get mgc memgraph -o jsonpath='{.status.conditions}'` shows whether the operator is still owed a setting (`Converged=False`, reasons below), and the rendered flag file is readable at any time:

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

The file is the operator's own defaults — the HA chart's logging defaults, `log-level` `INFO`, `also-log-to-stderr` `true`, `log-retention-days` `35` — with the role's `spec.flags` merged over them, the role's value winning. It is mounted read-only at `/etc/memgraph-flags/memgraph.flags` and loaded with `--flag-file`, the first flag on the command line. A flag set from the file counts as explicitly set, exactly like one on the command line.

The command line itself carries only what you may not override: the ports and listen addresses, the data directory, the log file, the metrics format, and the TLS file flags when a TLS mode is on. gflags processes `--flag-file` where it stands and takes the last occurrence of a repeated flag, so a pinned flag after the file wins even if a spelling of it got past admission.

Two consequences of the file worth knowing:

- **A name Memgraph does not have restarts nothing.** gflags skips a flag-file line naming a flag it does not know, so the operator asks the running Memgraph, never a list of its own: `SHOW CONFIG` lists every flag, `SHOW COORDINATOR SETTINGS` every coordinator setting. A key in neither is reported as `Converged=False` with reason `UnknownFlags`, naming it per role, and no pod is restarted for it. Fixing or removing it clears the condition, again with no restart. Memgraph hides four flags from `SHOW CONFIG`; the operator counts `also-log-to-stderr` and `scheduler` as flags and admission rejects the other two, `license-key` and `organization-name`.
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

The table lives in the operator (`internal/settings`) and is checked against Memgraph's source on an image bump. On an older Memgraph that does not yet know a setting at run time, the `SET` is refused and reported as below; the flag file still carries the value for the next restart.

Every other flag is read at startup only. A change to one changes the pod template — through an annotation hashing the startup-only flags, so a run-time change leaves the template untouched — and the operator rolls both roles' pods in the usual order: data instances before coordinators, the MAIN last of its role, the Raft leader last of its, each step gated on replication lag. An edit that changes both kinds does both: the run-time values land on every pod at once, and the roll carries the rest.

## What the resource reports

Run-time settings are applied on the pass that finds the cluster registered, before the roll picks its next pod. What that pass could not finish shows on the `Converged` condition:

- `Converged=False` with reason **`SettingsPending`** names the ready pods that did not answer. The usual cause is a Bolt endpoint lagging its pod's readiness by a few seconds; the pass retries on a delay. A pod that is not ready is not dialed and not reported — it is the roll's or the kubelet's business, and the flag file has its settings for when it comes up.
- `Converged=False` with reason **`SettingsRejected`** names the pod, the exact `SET DATABASE SETTING` and Memgraph's error, verbatim: a value the setting's validator refuses (`Boolean value supports only 'false' or 'true'`, `Unsupported log level`), or `Unknown setting name` on a Memgraph that does not have the setting at run time. It is retried every pass and only a changed flag clears it. The operator never escalates to a restart on its own: a value an instance refuses at run time it would refuse at startup too, and crash-loop on.

`Ready` is unaffected by either: a refused log level does not make a MAIN stop serving. `Updated` is the roll's, as before.

## Removing a flag

Removing a key removes its line from the flag file and issues no `SET`. For a startup-only flag that is a template change like any other, and the roll brings every pod onto Memgraph's default. For a run-time flag the running instances keep their current value until they next restart without the flag — and for the few settings Memgraph persists across restarts (`timezone`, `storage-gc-aggressive`, `hops-limit-partial-results`, `storage-omit-vector-index-properties-on-return`, `bolt-server-name-for-init`, `file-download-conn-timeout-sec`, the three slow-query log settings), even then: Memgraph restores the last value it was set to whenever the flag is not given explicitly, and has no way to reset one to its default. Set the default by hand with `SET DATABASE SETTING`, or write it into the map, if you need it back before the core offers a reset.

One exception, by design: a flag the operator has a default for (`log-level`, `also-log-to-stderr`, `log-retention-days`) goes back to that default when you remove your override, because the flag file then says so and the operator keeps the running instances in line with the file.

## Coordinator settings

The coordinators keep a handful of cluster-wide settings in Raft that are not flags: `enabled_reads_on_main`, `sync_failover_only`, `max_failover_replica_lag`, `max_replica_read_lag`, `instance_down_timeout_sec`, `instance_health_check_frequency_sec`, `global_read_only` and `deltas_batch_progress_size`. They go in the same map as the coordinators' flags:

```yaml
spec:
  flags:
    coordinators:
      log-level: INFO
      enabled-reads-on-main: "true"
      instance-down-timeout-sec: "7"
```

The operator tells a coordinator setting from a flag by asking: a key `SHOW COORDINATOR SETTINGS` lists is one, whatever Memgraph version adds next. It issues `SET COORDINATOR SETTING` once, for the keys whose value differs, over the coordinator connection it already holds for registration; any coordinator accepts the write, because a follower forwards it to the Raft leader. Nothing restarts. The line is in the flag file too, where gflags ignores it, so the file stays a function of the spec alone.

Values are strings as for flags. Admission checks `"true"`/`"false"` for the three boolean settings and digits for the five numeric ones, and rejects a coordinator setting under `flags.data`. A refused `SET` is `SettingsRejected`. Raft persists the setting, so removing the key issues no `SET` and the value stays. `SHOW COORDINATOR SETTINGS` answers with no rows while no ready leader is reachable; the operator then reports `SettingsPending` and writes nothing.

## How a changed key decides a restart

The pod template carries two annotations. `memgraph.com/flags` lists every key that is not a run-time flag with a digest of its value, and `memgraph.com/template-hash` is a hash of everything else in the template: the image, the containers, the environment, the volumes. A pod keeps both from the template it was created from.

When a pod is not on the current template, the operator first compares the template hash. If it differs, something besides the flags changed, and the pod is restarted in the usual order whatever the flags say: a coordinator setting added in the same edit as a new image tag still restarts the pods for the image. If only the flags changed, the operator lists the keys whose digest differs and asks Memgraph what they are. If they are all coordinator settings or names Memgraph does not have, the pod needs no restart and counts as current. If any is a flag, the pod is restarted.

`SHOW CONFIG` is answered by the instance that receives it, not forwarded, so it describes that pod's Memgraph version. The operator asks a ready pod that was created from its role's current template, data instances first, and falls back to the coordinator leader only when no pod is on the current template yet. During an image upgrade the leader is the last pod on the old version, and asking it would make a flag only the new version has look unknown. While no answer can be had, the operator restarts nothing and reports `Updated=False` with reason `FlagsUnclassified`, naming the keys.

## What may not be set

Admission rejects these keys, in either spelling:

- **`bolt-port`, `management-port`, `coordinator-port`, `bolt-address`, `monitoring-address`, `monitoring-port`, `metrics-port`**: the Services, probes, registered addresses, metrics scrapers and the Vector sidecar reach every instance at the fixed ports and on every interface.
- **`coordinator-id`, `coordinator-hostname`**: a coordinator's identity, derived from its pod ordinal.
- **`data-directory`, `log-file`**: both on the lib claim. `log-file` is `/var/lib/memgraph/logs/memgraph.log`, in a directory of its own because Memgraph prunes files older than `log-retention-days` from the log file's directory, and empty when `storage.<role>.fileLogging` is false, which is what keeps the image's own `log_file` default from crash-looping a read-only root filesystem. The log files count toward `libPVCSize`.
- **`bolt-cert-file`, `bolt-key-file`, `cluster-cert-file`, `cluster-key-file`, `cluster-ca-file`**: derived from `spec.tls`, see [TLS](tls.md).
- **`metrics-format`**: always OpenMetrics, see [monitoring](monitoring.md).
- **`license-key`, `organization-name`**: the CR carries no secret material; the license comes from the `secrets` block.
- **`aws-access-key`, `aws-secret-key`, `aws-region`, `aws-endpoint-url`**: all four come from the `awsCredentials` block's Secret (below), so the flag file and the Secret can never disagree.

Everything else is yours, including the operator's own logging defaults. Memgraph's [configuration reference](https://memgraph.com/docs/database-management/configuration) lists the flags.

## AWS credentials

The data instances' AWS integration (`LOAD PARQUET` and the other S3 readers) takes all four of its values from one Secret, the way the license does:

```sh
kubectl create secret generic aws-s3-credentials \
  --from-literal=AWS_ACCESS_KEY_ID=... \
  --from-literal=AWS_SECRET_ACCESS_KEY=... \
  --from-literal=AWS_REGION=eu-west-1 \
  --from-literal=AWS_ENDPOINT_URL=          # empty for AWS itself, e.g. http://minio:9000 otherwise
```

```yaml
spec:
  awsCredentials:
    secretName: aws-s3-credentials
```

| Secret key | Flag | Setting |
| --- | --- | --- |
| `AWS_ACCESS_KEY_ID` | `aws-access-key` | `aws.access_key` |
| `AWS_SECRET_ACCESS_KEY` | `aws-secret-key` | `aws.secret_key` |
| `AWS_REGION` | `aws-region` | `aws.region` |
| `AWS_ENDPOINT_URL` | `aws-endpoint-url` | `aws.endpoint_url` |

The keys are fixed: they are the names the AWS SDKs read, and the credentials are the ones the core dumps uploader's Secret already holds, so one Secret can serve both. **All four are required**, `AWS_ENDPOINT_URL` too even when it is empty: Kubernetes leaves a reference to a variable it could not set as literal text, so a missing key would start Memgraph on the string `$(MEMGRAPH_AWS_ENDPOINT_URL)`. A pod whose Secret lacks a key does not start, as with the license. Coordinators get none of the four; they run no queries that read from AWS.

- **At startup** a data instance runs with `--aws-access-key=$(MEMGRAPH_AWS_ACCESS_KEY) --aws-secret-key=$(MEMGRAPH_AWS_SECRET_KEY) --aws-region=$(MEMGRAPH_AWS_REGION) --aws-endpoint-url=$(MEMGRAPH_AWS_ENDPOINT_URL)`, every variable read from the Secret, so the pod spec names the Secret and never a value. Memgraph persists all four settings across restarts and restores them over its own `AWS_*` environment fallback, but not over a flag on the command line, so a restarted instance always starts on what the Secret holds now.
- **While running**, a change to the Secret reaches every ready data instance with `SET DATABASE SETTING`, read before it is written, within a reconcile pass and without a restart. The operator watches the Secret by metadata and reads it uncached; no value is logged, evented or written to status. A Secret or key that is missing, or a ready instance that does not answer, is `Converged=False/SettingsPending`; a refused `SET` is `SettingsRejected`, naming the setting and not its value.
- **Adding the block or pointing it at another Secret** changes the pod template and rolls the data instances; the live `SET` has already reached every ready one by then. **Removing it** issues no `SET`, like removing any flag: the instances keep the last values until `SET DATABASE SETTING "aws.access_key" TO ""` (and the other three) clears them.
