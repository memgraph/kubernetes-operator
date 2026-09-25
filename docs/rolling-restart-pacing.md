# Rolling restart pacing: what a pod restart waits for, and why DNS sets the pace

Both role StatefulSets use `updateStrategy: OnDelete`, so a pod-template change is rolled out by the
operator deleting one pod at a time (`internal/rollout`). On a real cluster the step from one data
pod to the next takes noticeably longer than Memgraph needs to start, and the reason is not the
operator and not Memgraph: it is CoreDNS remembering that the deleted pod's hostname did not exist.
This page records the gates, a measured breakdown, the DNS mechanism, how to diagnose it, and the
options that were considered.

## What the roll waits for

Before the next pod is deleted, the operator requires, in this order:

1. **Every pod of the role exists and is Ready.** The replacement of the pod deleted last has to be
   recreated by the StatefulSet and pass its TCP readiness probe. Reported as `Updated=False` with
   reason `WorkloadsNotReady` ("Waiting for pod ... to become ready").
2. **Every data pod already carrying the current revision is back in replication**: the coordinator
   leader reports it `health=up` in `SHOW INSTANCES`, and `SHOW REPLICATION LAG` reports it zero
   transactions behind MAIN in every database. Reported as `WorkloadsNotReady` ("Waiting for
   restarted data instance ... to be reachable") or `WaitingForCatchUp`.
3. **For the MAIN, which goes last**, at least one other data instance that is both reachable and
   caught up, because deleting the MAIN's pod forces a coordinator-driven failover and the
   coordinators must have something safe to promote. Reported as `NoCaughtUpSurvivor`.

Readiness alone is deliberately not enough. A TCP connect on the Bolt port only says Memgraph is
listening; it says nothing about whether the instance has rejoined replication or holds every
committed transaction, and that second part is what protects against losing writes when the MAIN
is taken down next.

The decision is re-derived from a fresh observation on every pass, and passes are polled every
`requeueWhilePending` (10 s) while a restart is pending, because `SHOW INSTANCES` and
`SHOW REPLICATION LAG` cannot be watched. The operator therefore adds between 0 and 10 s to each
step after the replica is provably caught up.

## Anatomy of one step, measured

Measured on AKS on 2026-09-23 (Memgraph 3.13.0, two data instances, a flag flipped in
`extraArgs.data`). The non-MAIN pod `data-1` was deleted at 10:03:33; the MAIN's pod `data-0` was
deleted 37 s later.

| Phase | Window | Time |
| --- | --- | --- |
| Pod terminated, recreated by the StatefulSet, container started | 10:03:33 → 10:03:44 | 11 s |
| Memgraph startup until Bolt, replication and management servers listen | 10:03:44 → 10:03:46 | 2 s |
| Coordinator leader and MAIN cannot resolve `data-1`'s hostname (`Name or service not known`) | 10:03:46 → 10:04:08 | 22 s |
| MAIN's first `SystemRecoveryReq` rejected (stale MAIN UUID), coordinator `SwapMainUUID`, second one reports `Replica instance_1 up to date` | 10:04:08 → 10:04:09 | 1 s |
| Operator's next 10 s poll sees instance_1 up and caught up, deletes `data-0` | 10:04:10 | 1 s |

The same shape appeared in an earlier step of the same session: management server up at 10:00:04,
first successful contact from the MAIN at 10:00:26. The 22 s hole is the subject of the rest of
this page.

## Why: CoreDNS negative caching

A StatefulSet pod hostname (`<sts>-<ordinal>.<headless-service>.<ns>.svc.cluster.local`) is the one
DNS name in Kubernetes that is expected to vanish and come back with a different address. During
a restart:

1. The pod is deleted. The EndpointSlice controller removes its address and CoreDNS's `kubernetes`
   plugin, which watches EndpointSlices, immediately has no record for the name.
2. The coordinator's health check and the MAIN's replication client retry the name every second.
   CoreDNS answers NXDOMAIN, which is the truth at that moment.
3. CoreDNS's `cache` plugin sits in front of the `kubernetes` plugin and stores every answer,
   negative ones included. This is standard DNS behaviour (RFC 2308): a "no such name" answer is
   cached for the zone's SOA minimum TTL so resolvers do not hammer the authoritative server for
   names that do not exist. In CoreDNS the `kubernetes` plugin's `ttl` value is used as that SOA
   minimum, and the `cache` directive caps it.
4. The replacement pod gets an IP and the `kubernetes` plugin knows the new record within a second
   or two — the headless Services set `publishNotReadyAddresses: true`, so this does not even wait
   for readiness. But the `cache` plugin has no invalidation path from the backend: it keeps serving
   the cached NXDOMAIN until the TTL runs out.

The cache is not remembering the old IP and failing to see the new one. It is remembering "this
name has no records" and will not ask the backend again until that memory expires. Had the pod
stayed alive and merely changed IP, the mirror-image problem would apply: the old address cached
for the same TTL and connections going to a dead IP. Either way the TTL is the price of caching,
and a client that resolves on every attempt — as Memgraph does — cannot get around a stale answer
from its resolver.

The TTL is a cluster setting, not the operator's:

| Distribution | `kubernetes` plugin `ttl` | Negative cache per restart |
| --- | --- | --- |
| CoreDNS upstream default (Kind, kubeadm) | 5 s | ~5 s |
| AKS (`kube-system/coredns` Corefile, `ttl 30` and `cache 30`) | 30 s | ~30 s |

Upstream defaults to 5 s precisely because pod records churn constantly; managed clusters raise
it to reduce CoreDNS load on large clusters. That trade is reasonable for stable Services and
bites per-pod hostnames. Each CoreDNS replica also has its own cache, so two clients can see
different answers for a few seconds. The e2e suite runs on Kind, which is why this phase never
stood out there.

Memgraph is behaving correctly throughout: its RPC clients re-resolve on every attempt and cache
nothing in-process, which is why the gap is exactly one CoreDNS TTL and nothing more. The
`Name or service not known` lines at TRACE level in both the coordinator's and the MAIN's logs are
the signature.

## Diagnosing a slow or apparently stuck roll

1. The `Updated` condition names the gate: `WorkloadsNotReady`, `WaitingForCatchUp`,
   `NoCaughtUpSurvivor`, or `RollingRestartInProgress` with the pod being restarted.

   ```sh
   kubectl get mgc <name> -n <ns> -o jsonpath='{range .status.conditions[*]}{.type}{"\t"}{.reason}{"\t"}{.message}{"\n"}{end}'
   ```

2. The operator's log lines `Deleted a workload pod to restart it onto the current pod template`
   and `Deferred the next pod restart` carry the decision message in their `reason` field, so the
   full sequence of the roll can be reconstructed from the manager's log alone.
3. Which pods still need a restart is the pods' `controller-revision-hash` label compared with the
   StatefulSet's `status.updateRevision`; `kubectl get controllerrevisions` shows the args each
   revision carried and when it was created.
4. Memgraph's own logs persist on the `log-storage` claim at
   `/var/log/memgraph/memgraph_<date>.log`, so they survive the pod being recreated. Look there for
   `Name or service not known` (DNS), `Replica <name> up to date` (the catch-up the roll waits
   for), and `PromoteToMainReq` / `DemoteMainToReplicaReq` (coordinator-driven failovers).
5. The TTL in force: `kubectl -n kube-system get cm coredns -o jsonpath='{.data.Corefile}'`.

### A pod that looks skipped

A spec change that lands while a roll is still in flight can make one pod appear to be skipped
by the *next* roll. Observed on 2026-09-23: the operator applied a new template and, in the same
reconcile, deleted the MAIN's pod as the last step of the *previous* roll. Under `OnDelete` the
StatefulSet recreates a pod on its current `updateRevision`, so that pod came back already on the
new template and the new roll had only the other pod left to restart. `Updated=True` was correct:
every pod ran the template the spec described. Check the revision hashes before concluding a pod
was missed.

## Options considered

Roughly from cheapest to most structural, with what other clustered systems do:

1. **Publish addresses before readiness** so the record reappears as early as possible. Done:
   `publishNotReadyAddresses: true` on both headless Services (`internal/resources/service.go`).
2. **Retry with short backoff and no client-side caching**, so the client recovers the moment the
   resolver does. Memgraph already does this. JVM-based systems (Kafka, Elasticsearch) have to add
   explicit `networkaddress.cache` settings because the JVM caches negative lookups for 10 s on its
   own; ZooKeeper had the mirror-image bug for years — peer IPs resolved once and cached forever —
   fixed in ZooKeeper itself by re-resolving on connection failure.
3. **Lower the CoreDNS `kubernetes` plugin TTL.** The cluster-level fix. On AKS the supported knob
   is the `coredns-custom` ConfigMap in `kube-system`, overriding `ttl` for the `cluster.local`
   zone; the cost is more DNS queries cluster-wide.
4. **A stable virtual address per member**: one ClusterIP Service per pod, selecting on the
   `statefulset.kubernetes.io/pod-name` label, advertised as the replication and coordinator
   address. A Service's DNS record never disappears and its ClusterIP never changes, so caching
   becomes harmless, and kube-proxy retargets the virtual IP to the new pod IP within a second or
   two of the EndpointSlice update, independent of any DNS TTL. Several operators use per-pod
   Services this way (most visibly for external access); the cost is N extra Services and one NAT
   hop. This is the operator-side change that removes the DNS dependency rather than shaving it,
   and it needs nothing from Memgraph core.
5. **Register pod IPs instead of names** in the coordination layer, the Patroni approach. Does not
   fit Memgraph: the coordinators persist advertised addresses in Raft and a pod IP changes on
   every recreation.

**Decision (2026-09-23): nothing changed in the operator.** The delay is a fixed one-TTL per pod
(~30 s on AKS, ~5 s elsewhere), it never threatens correctness, and every gate that produces it is
one we want. If a user needs faster rolls on a managed cluster where CoreDNS cannot be tuned,
option 4 is the design to reach for; shortening `requeueWhilePending` during a roll would only
shave the operator's own 0–10 s share.
