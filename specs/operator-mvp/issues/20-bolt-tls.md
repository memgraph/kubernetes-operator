# Bolt TLS

**Type**: AFK

## Parent

`specs/operator-mvp/PRD.md` — TLS (bolt and intra-cluster) was listed there as a later version; this issue and `21-intra-cluster-tls.md` are that version, scoped in a design interview on 2026-09-25.

## What to build

Let a cluster serve Bolt, and with it the metrics endpoint, over TLS from a certificate the user supplies. `spec.tls.bolt` is an optional struct with one field, `secretName`, naming a Secret in the cluster's namespace holding `tls.crt` and `tls.key` — the keys `kubectl create secret tls` and a cert-manager Certificate write, so the ordinary way of getting a certificate into Kubernetes produces a working Secret with nothing renamed. The block is presence-based like `externalAccess` and `monitoring`: present, both roles run with TLS on 7687; absent, they do not. There is no `enabled`, and no key-name overrides: the HA chart's `certSecretPath` / `keySecretPath` exist because the chart validates nothing, and a Secret is re-keyed in one command.

One Secret serves every pod of both roles. A StatefulSet has one pod template, so per-pod Secrets like the chart's cannot be expressed at all, and per-pod keys inside one Secret would buy nothing: a Bolt certificate carries as many SANs as its clients dial, and no client distinguishes one member from another by certificate. Every pod mounts the Secret read-only at `/etc/memgraph/ssl` with the two keys projected by name, and both roles get `--bolt-cert-file=/etc/memgraph/ssl/tls.crt --bolt-key-file=/etc/memgraph/ssl/tls.key` in their common args. A missing Secret or a missing key is handled as a missing license Secret is today — the pod cannot start and the cluster stays unconverged — with nothing new pre-checked.

Bolt TLS is one-way, server-authenticated TLS: Memgraph presents the certificate and never asks the client for one. What a client gets is confidentiality of every Bolt byte and, if it verifies, proof it reached the real cluster. Verification is the client's business — the CA lives with whoever verifies, never with Memgraph — and it needs the certificate's SANs to cover the address the client dialed. The address a client outside Kubernetes dials is the one the operator discovers and announces as `bolt_server`, so the SANs must cover the external hostname of an exposed cluster; a client inside the cluster dials pod DNS, which a wildcard per role covers (`*.<cluster>-coordinator.<namespace>.svc.<clusterDomain>` and the data equivalent). That requirement is documented on the field, not validated.

Three things inside the operator follow from the block, all derived, none a knob:

- **The operator's own dials.** The controller speaks to coordinators over pod DNS, so with the block set it dials TLS. It does not verify the certificate: verification would force every Bolt certificate to carry a CA plus pod-DNS SANs, ruling out the ordinary case of a public-CA certificate issued for the external hostname alone, and it would buy nothing today, because Bolt is unauthenticated and anyone positioned to impersonate a coordinator to the operator can send the same admin commands to the real one. The trigger for revisiting this is Bolt auth in the operator: then an impostor could harvest credentials, and a CA plus SAN requirement for the operator's dials becomes worth its cost. The Connector interface takes the intent alongside the address; the Bolt implementation dials `bolt+ssc` for TLS and `bolt` otherwise.
- **Enabling or disabling on a live cluster is an ordinary roll**, because members never speak Bolt to each other. The one party the roll affects is the operator itself: coordinators roll last, so for the whole data-instance phase after the block is added every coordinator still speaks plain Bolt while the spec says TLS, and dialing per spec alone would stall the roll at its first step. The connector therefore dials the mode the spec asks for and, if that fails, retries once in the other mode; the cost is one failed connection per not-yet-rolled coordinator, and it self-corrects when the roll ends.
- **The ServiceMonitor switches to `scheme: https` with `insecureSkipVerify: true`.** `memgraph.cpp` hands the metrics server the Bolt server context, so 9091 speaks TLS with the same certificate the moment the block is set, and a ServiceMonitor still saying `http` would fail every scrape silently. Prometheus scrapes pod IPs from the headless Services' endpoints, so a verified scrape needs a `serverName` and that name in the SANs on top of a CA the ServiceMonitor can reference — the same pod-DNS burden on a user-facing certificate the operator's own dials decline — and the answer follows. No `tlsConfig` passthrough is exposed: nothing verifiable can be put in it without the SAN work, so it would be a knob with one working value.

Probes are untouched: the only probe either role carries is a TCP-socket readiness check, which completes the TCP handshake and closes without speaking TLS.

Rotation is not the operator's job. The Secret is mounted without `subPath`, so the kubelet refreshes the files on every pod within its sync period after any in-place update to the Secret, from cert-manager or `kubectl apply` alike. Memgraph today reloads the Bolt server context only on `RELOAD BOLT SERVER TLS`, which the docs name as the manual step per pod until the core picks the files up on its own the way NuRaft already does (an mtime check per handshake, to be extended in Memgraph). The operator never watches Secrets — that would widen its RBAC to reading every Secret's contents — and never restarts pods for an in-place update. Pointing the spec at a differently named Secret is a pod-template change and rolls normally.

Testing follows the house split. Builder tests pin the volume, the projected keys, the read-only mount and the two flags on both roles, and that a cluster without the block carries none of it. The ServiceMonitor golden test gains the https-plus-skip-verify shape. A unit test pins the connector's dial order for both intents. Envtest proves the controller dials coordinators with the TLS intent when the block is set, through the fake connector. The e2e suite boots a cluster exposed through LoadBalancers and asking for a ServiceMonitor, converges it plain, then adds the block and watches the ordered roll complete under the two-mode dial; afterwards a `neo4j+ssc` routing driver writes and reads through the coordinators' external address, a plain `neo4j` driver is refused, the ServiceMonitor endpoint reads `https` with skip-verify, and 9091 answers over TLS. The certificate is minted in the test process with `crypto/x509` — a throwaway CA and one leaf with the pod-DNS wildcards — and written as a `kubernetes.io/tls` Secret; no cert-manager and no fixture files that expire.

## Acceptance criteria

- [ ] `spec.tls.bolt` is an optional struct with a required `secretName`; no `enabled`, no key-name knobs
- [ ] Present, both roles mount the Secret read-only at `/etc/memgraph/ssl` projecting `tls.crt` and `tls.key`, and carry `--bolt-cert-file` and `--bolt-key-file` in their args; absent, none of it; builder tests cover both roles and both cases
- [ ] The field's doc comment states the SAN requirement (the announced external address, pod-DNS wildcards for in-cluster clients) and the rotation contract
- [ ] `memgraph.Connector.Connect` takes the TLS intent; the Bolt connector dials `bolt+ssc` for TLS and `bolt` otherwise, retrying once in the other mode on failure; a unit test pins the order
- [ ] The controller passes the intent from the spec on every coordinator dial; envtest covers a cluster with the block dialing TLS
- [ ] The ServiceMonitor endpoint is `https` with `tlsConfig.insecureSkipVerify: true` when the block is set and unchanged otherwise; golden test covers it
- [ ] `make manifests generate chart-sync` regenerated and `make chart-verify` green
- [ ] E2E: a plain cluster that gets the block rolls in order and converges; a `neo4j+ssc` driver writes and reads through the coordinators' LoadBalancer; a plain driver is refused; the ServiceMonitor reads https with skip-verify; 9091 answers over TLS
- [ ] `docs/tls.md` states the contract for Bolt: the Secret shape, the SAN rules, what the operator derives, the manual `RELOAD BOLT SERVER TLS` step until the core reloads on its own, and the toggle-on-live-cluster behaviour; the README links to it and `docs/monitoring.md` no longer lists `scheme`/`tlsConfig` as out of scope

## Blocked by

- Nothing: builds on `17-sequenced-rolling-restart.md` and `18-openmetrics-servicemonitor.md`, both on `main`
