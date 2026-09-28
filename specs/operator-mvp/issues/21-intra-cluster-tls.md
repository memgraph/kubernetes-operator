# Intra-cluster TLS

**Type**: AFK

## Parent

`specs/operator-mvp/PRD.md` — TLS (bolt and intra-cluster) was listed there as a later version; `20-bolt-tls.md` and this issue are that version, scoped in a design interview on 2026-09-25.

## What to build

Let the members of a cluster talk to each other over mutual TLS: replication, the coordinator-to-instance management RPC, and Raft between coordinators. `spec.tls.intraCluster` is an optional struct beside `spec.tls.bolt`, independent of it, with one field, `secretName`, naming a Secret holding `tls.crt`, `tls.key` and `ca.crt` — exactly what a cert-manager Certificate issued by a private CA writes. Present, every pod of both roles mounts the Secret read-only at `/etc/memgraph/intra_cluster_tls` with the three keys projected by name and runs with `--cluster-cert-file`, `--cluster-key-file` and `--cluster-ca-file`; Memgraph validates at boot that the three arrive together, so the operator always passes all three.

One shared certificate is the shape, for both roles, and the survey of thirteen database operators is the argument: nine take one certificate verified against the CA only (CloudNativePG, TiDB, cass-operator, both Percona operators, Zalando, Redpanda, MongoDB Community, ScyllaDB); the per-pod designs (Strimzi, ECK) exist where the engine verifies hostnames or the operator runs its own CA and minting per pod is free; CockroachDB verifies hostnames and still shares one certificate through a wildcard SAN. Memgraph verifies no member hostname anywhere on this path — `cluster_tls.cpp` checks the chain and, server-side, demands a client certificate; NuRaft's `verify_sn_` is not set — so per-pod identities would give it nothing to check. A wildcard pod-DNS SAN per role is documented as recommended practice regardless, so a later hostname check in the core costs users nothing. The operator runs no CA of its own; that is the larger scope this deliberately avoids.

The mode cannot be added to or removed from a live cluster, and admission says so. Intra-cluster TLS is all-or-nothing per process, and a member with it cannot talk to a member without it; the operator's roll replaces one pod at a time, data instances first, and gates every step on replication lag. Enabling on a live cluster therefore deadlocks at the first step: the restarted replica comes back speaking TLS, the still-plain MAIN can no longer replicate to it, lag never converges, and the next pod is never deleted. Disabling deadlocks the same way, and coordinators partition the same way under Raft. A CEL transition rule on the `tls` block pins the presence of `intraCluster`, with the delete-and-recreate-on-retained-volumes message the claim-template rules use. The alternatives — a full-stop restart mode in the operator, which contradicts the ordered-roll invariant, or a permissive mixed mode in the core — are both larger than the problem. Changing `secretName` inside the block stays allowed and rolls normally: every member speaks TLS throughout, and the certificates on both sides of the roll must chain to a CA the other side trusts, which is the user's job. A CA cut-over therefore needs `ca.crt` in both Secrets to be a bundle holding the old and the new CA, documented rather than detected; a roll stalled by a mismatch is recovered by fixing the bundle and, until the core reloads on its own, running `RELOAD INTRA_CLUSTER TLS` on the not-yet-rolled pods.

Rotation is as for Bolt: the mount has no `subPath`, the kubelet refreshes the files, NuRaft already picks them up per handshake by mtime, and the replication and management contexts do so on `RELOAD INTRA_CLUSTER TLS` until the core extends the mtime check to them. The operator watches no Secret and drives no reload.

Nothing the operator itself does changes: it never speaks on these ports, probes stay TCP, and the planner and the roll are unaffected.

Testing follows the house split. Builder tests pin the volume, the three projected keys, the read-only mount and the three flags on both roles, and that a cluster without the block carries none of it. Envtest covers the pin: adding `intraCluster` to a live cluster and removing it are both rejected with the message, changing its `secretName` is accepted. The e2e suite boots a cluster with both `tls.bolt` and `tls.intraCluster` from a CA minted in the test process, converges it, writes and reads through a `neo4j+ssc` driver, and proves the roll still works under intra-cluster TLS by changing an unrelated pod-template field and watching the ordered roll complete — which is what a `secretName` change would exercise too.

## Acceptance criteria

- [ ] `spec.tls.intraCluster` is an optional struct with a required `secretName`, independent of `spec.tls.bolt`
- [ ] Present, both roles mount the Secret read-only at `/etc/memgraph/intra_cluster_tls` projecting `tls.crt`, `tls.key` and `ca.crt`, and carry the three `--cluster-*-file` flags; absent, none of it; builder tests cover both roles and both cases
- [ ] A CEL transition rule refuses adding or removing `intraCluster` on a live cluster with the delete-and-recreate message; `secretName` changes within it are accepted; envtest covers all three
- [ ] The field's doc comment states the Secret shape, the wildcard SAN recommendation, the CA-bundle rule for a cut-over and the pin
- [ ] `make manifests generate chart-sync` regenerated and `make chart-verify` green
- [ ] E2E: a cluster with both blocks bootstraps and converges, serves a `neo4j+ssc` driver, and completes an ordered roll on an unrelated template change
- [ ] `docs/tls.md` gains the intra-cluster section: the Secret shape, mTLS and what Memgraph verifies, why one shared certificate, the pin and its reason, the CA cut-over procedure and the manual `RELOAD INTRA_CLUSTER TLS` step until the core reloads on its own

## Blocked by

- `20-bolt-tls.md`: the `spec.tls` container, the certificate minting in the e2e suite and `docs/tls.md` come from there
