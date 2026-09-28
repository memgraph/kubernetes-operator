# TLS: serving Bolt over a certificate you supply

`spec.tls` turns on the cluster's TLS modes from certificates in Secrets you supply. This document is the contract for `spec.tls.bolt`: the Secret shape, what the certificate has to cover, what the operator derives from the block, how a certificate is renewed, and what happens when the block is added to or removed from a running cluster. Intra-cluster TLS (`spec.tls.intraCluster`) is a separate mode and arrives separately.

## `spec.tls.bolt`

```yaml
spec:
  tls:
    bolt:
      secretName: bolt-tls   # a kubernetes.io/tls Secret in the cluster's namespace
```

The block is presence-based, like `externalAccess` and `monitoring`: present, both roles serve Bolt on 7687 over TLS; absent, in plaintext. There is no `enabled` field and no key-name knobs.

The Secret holds the certificate under `tls.crt` and the private key under `tls.key`, the shape of a `kubernetes.io/tls` Secret. That is what both of these produce, unchanged:

```sh
kubectl create secret tls bolt-tls --cert=tls.crt --key=tls.key -n memgraph
```

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: memgraph-bolt
  namespace: memgraph
spec:
  secretName: bolt-tls
  issuerRef: {name: my-issuer, kind: ClusterIssuer}
  dnsNames:
    - memgraph.example.com
    - "*.memgraph-coordinator.memgraph.svc.cluster.local"
    - "*.memgraph-data.memgraph.svc.cluster.local"
```

Every pod of both roles mounts the Secret read-only at `/etc/memgraph/ssl`, with the two keys projected by name, and runs with `--bolt-cert-file=/etc/memgraph/ssl/tls.crt --bolt-key-file=/etc/memgraph/ssl/tls.key`. A Secret carrying more keys mounts fine; one missing either key, or a Secret that does not exist, keeps the pod from starting, which is reported the way a missing license Secret is: the pod stays pending and the cluster unconverged.

One Secret serves every pod of both roles. A StatefulSet has one pod template, so the HA chart's per-instance Secrets cannot be expressed, and per-pod certificates would buy nothing: a Bolt certificate carries as many names as its clients dial, and no client tells one member from another by certificate.

## What Bolt TLS is, and what the certificate has to cover

Bolt TLS is one-way, server-authenticated TLS. Memgraph presents the certificate and never asks a client for one. A client that connects gets confidentiality of every Bolt byte on the wire and, if it verifies, proof it reached the real cluster; who the client is remains Bolt auth's business, never the certificate's.

Verification is the client's job, and the CA lives with whoever verifies, never with Memgraph. A verifying client checks two things: that the chain ends at a CA it trusts, and that the certificate's subject alternative names contain the hostname it dialed. So the names on the certificate must cover every address a verifying client uses:

- **The external address** of an exposed cluster, which is what `status.externalAccess` reports and what the operator announces as `bolt_server` in the routing table. With external-dns that is your hostname annotation; without, a LoadBalancer's IP, which a certificate can carry as an IP SAN.
- **The pod DNS names** for clients inside Kubernetes, which one wildcard per role covers: `*.<cluster>-coordinator.<namespace>.svc.<clusterDomain>` and `*.<cluster>-data.<namespace>.svc.<clusterDomain>`. A routing driver follows the announced addresses, so an in-cluster client of an unexposed cluster dials exactly these.

None of this is validated at admission. A certificate that covers the wrong names still serves; verifying clients refuse it, and non-verifying ones do not notice.

A client's dial then looks like this with the Neo4j drivers: `neo4j+s://` verifies against the driver's trust store (or a custom one), `neo4j+ssc://` encrypts and accepts any certificate. `mgconsole` takes `--use-ssl=true`.

## What the operator derives from the block

Three things follow from the block. None is a knob.

- **The operator's own dials.** The controller speaks to the coordinators over pod DNS. With the block set it dials TLS and does not verify the certificate. Verifying would force every Bolt certificate to carry a CA and the pod-DNS wildcards, ruling out the ordinary case of a public-CA certificate issued for the external hostname alone, and it would buy nothing while Bolt is unauthenticated: anyone positioned to impersonate a coordinator to the operator can send the same commands to the real one. The trigger for revisiting this is Bolt auth in the operator, when an impostor could harvest credentials.
- **The ServiceMonitor.** Memgraph serves the metrics endpoint from the Bolt server context, so the moment the block is set, 9091 speaks TLS with the same certificate. A `spec.monitoring.serviceMonitor` therefore switches its endpoint to `scheme: https` with `tlsConfig.insecureSkipVerify: true`. Prometheus scrapes pod IPs from the headless Services' endpoints, which no user-facing certificate names, so a verified scrape would need a `serverName` in the SANs on top of a CA the ServiceMonitor can reference; that is the same burden the operator's dials decline, for the same reason. A ServiceMonitor or scrape config of your own must make the same switch.
- **Probes are untouched.** The only probe either role carries is a TCP-socket readiness check, which completes the TCP handshake and closes without speaking TLS.

## Renewing the certificate

The operator does not rotate certificates and never watches Secrets. What it does is mount the Secret without `subPath`, which is what makes an in-place update reach running pods: after any update to the Secret object, from cert-manager, External Secrets, Vault or `kubectl apply` alike, the kubelet swaps the mounted files on every pod within its sync period, about a minute.

Memgraph then has to pick the new files up. Today it does so on request, per instance:

```cypher
RELOAD BOLT SERVER TLS;
```

issued on every coordinator and every data instance. Until Memgraph reloads the files on its own, the way its Raft layer already does, that query is the manual step of a renewal. Nothing restarts.

Pointing the spec at a differently named Secret is different: it is a pod-template change and rolls the cluster, one pod at a time in the operator's order. That is the path for a deliberate cut-over, not for routine renewal.

A certificate nobody renews expires, and the cluster stops serving verifying clients when it does. That is true of every operator that takes a certificate rather than running its own CA.

## Adding or removing the block on a running cluster

Both are allowed and both are an ordinary roll: the block changes the pod template, so `Updated` goes False and the operator restarts the pods in its usual order, data instances first, the MAIN last of its role, then coordinators, the Raft leader last. Members never speak Bolt to each other, so the cluster itself does not care which pods have rolled.

The one party the roll affects is the operator. Coordinators roll last, so after the block is added every coordinator still speaks plaintext for the whole data-instance phase while the spec says TLS, and the reverse when the block is removed. The operator dials the mode the spec asks for and, if that fails, retries once in the other mode, so the roll proceeds; the cost is one failed connection per not-yet-rolled coordinator per pass, and it self-corrects when the roll completes. Clients see the change pod by pod: a client pinned to one address must switch modes when that pod rolls, and a routing driver reconnects as the routing table moves.

## Not in scope

- **Client certificates on Bolt** (mutual TLS towards applications). Memgraph's Bolt server does not request one.
- **Verification by the operator or the ServiceMonitor.** See above; the trigger is Bolt auth.
- **Certificate issuance and rotation.** Bring cert-manager or your own tooling; the operator consumes the Secret and lets the kubelet deliver updates.
- **The HA chart's per-instance Secrets and key-name overrides.** One Secret with the standard keys serves the whole cluster.
