# Backup and restore: volume snapshots and the claims they come back to

The operator ships no backup feature, and needs none for volume-level backup and restore to work: the pieces that make it work are its storage model. This document is the contract a backup tool relies on — how the claims are named, what the operator will and will not do to them — and the restore sequence that follows from it. It applies to any CSI driver with snapshot support (Portworx, EBS, Ceph, and the rest) and to the products built on the `VolumeSnapshot` API (PX-Backup, Velero, Kasten); nothing here is vendor-specific.

## What the operator does with storage

Each pod of a `MemgraphCluster` owns its PersistentVolumeClaims through the StatefulSet's `volumeClaimTemplates`, and the StatefulSet controller names them `<template>-<pod>`:

| Claim | Holds | Pod |
| --- | --- | --- |
| `lib-storage-<cluster>-coordinator-<n>` | the coordinator's Raft state | `<cluster>-coordinator-<n>` |
| `lib-storage-<cluster>-data-<n>` | the data instance's snapshots, WAL and durable state | `<cluster>-data-<n>` |
| `log-storage-<cluster>-<role>-<n>` | Memgraph's log files (unless `createLogStorageClaim: false`) | the same pod |
| `core-dumps-<cluster>-<role>-<n>` | core dumps, when the role collects them | the same pod |

For a cluster named `memgraph` with three coordinators and two data instances, the claims that matter for a backup are therefore `lib-storage-memgraph-coordinator-0`, `-1`, `-2` and `lib-storage-memgraph-data-0`, `-1`. Every claim carries the cluster's identity labels (`app.kubernetes.io/instance: <cluster>`, `app.kubernetes.io/component: coordinator | data`), which is what a group snapshot selects on.

Three properties of the operator make restore possible, and all three are deliberate:

- **It never deletes a claim.** It holds no finalizer and runs no cleanup of its own; the only thing that ever removes a PVC is the StatefulSet's own retention policy, which follows `spec.storage.retentionPolicy` and defaults to `Retain`. Deleting the `MemgraphCluster` takes the StatefulSets and pods away through garbage collection and leaves every claim where it is.
- **The StatefulSet adopts a claim that already exists under the right name** rather than creating a new one. This is the entire restore mechanism: put the right data behind the right name before the pod comes up, and the pod starts on it.
- **Re-registration is idempotent.** When the pods are back, the operator compares `SHOW INSTANCES` on the coordinator leader with the declared topology and issues only what is missing. A cluster restored with its coordinators' Raft state intact usually needs nothing.

## Taking a backup

Back up the `lib-storage` claims; the log and core-dump claims hold nothing a restore needs. Two things distinguish a database cluster from a stateless workload:

- **Consistency across members.** Snapshot all `lib-storage` claims of the cluster as one group, at one instant, so the coordinators' view of the cluster and the data instances' storage agree. With Stork that is a `GroupVolumeSnapshot` selecting `app.kubernetes.io/instance=<cluster>`; Velero and Kasten have their own grouping. Snapshots taken one claim at a time, seconds apart, can disagree about which instance was MAIN and how far each replica had caught up.
- **Application consistency.** A snapshot is crash-consistent: Memgraph recovers from it as it would from a power loss, replaying the WAL since its last own snapshot. Running `CREATE SNAPSHOT;` on the MAIN just before the volume snapshot (a Stork pre-exec `Rule`, a Velero pre-hook) shortens that replay to seconds. It is an optimization, not a requirement.

## Restoring in place

The sequence, in this order:

1. **Delete the `MemgraphCluster`.** Keep its manifest. The pods go; the claims stay (`Retain`).
2. **Replace the claims from the snapshots, under the same names.** Either let the backup tool restore the volumes over the existing claims, or delete each claim and create it again with a `dataSource` pointing at its snapshot. A claim created by hand has to match what the StatefulSet template will ask for, or the pod never binds:

   ```yaml
   apiVersion: v1
   kind: PersistentVolumeClaim
   metadata:
     name: lib-storage-memgraph-data-0      # the template name, the cluster, the role, the ordinal
     namespace: memgraph                    # the cluster's namespace
   spec:
     storageClassName: <spec.storage.data.libStorageClassName>
     accessModes: [<spec.storage.data.libStorageAccessMode>]   # ReadWriteOnce by default
     resources:
       requests:
         storage: <spec.storage.data.libPVCSize>               # at least; a larger claim is fine
     dataSource:
       apiGroup: snapshot.storage.k8s.io
       kind: VolumeSnapshot
       name: <the snapshot of the old data-0>
   ```

   The mapping has to be ordinal-correct: the old `data-0`'s snapshot goes behind the new `data-0`, and each coordinator's snapshot behind its own ordinal, because a coordinator's Raft state is specific to its identity in the cluster.
3. **Re-create the `MemgraphCluster`** from the kept manifest. The StatefulSets adopt the claims, the pods start on the restored data, and the operator re-registers whatever is missing.

Partial restores work the same way. To restore the data instances alone, replace only their claims; a coordinator that comes back on its own state re-registers data instances that have changed under it. To rebuild a replica from the MAIN rather than from a snapshot, delete its claim and let the StatefulSet create an empty one: the operator registers it and Memgraph replicates the MAIN's data to it.

## Restoring somewhere else

Restoring the namespace into another Kubernetes cluster — the disaster-recovery case — works with one constraint that is not the operator's to lift. The coordinators' Raft state records every member's address as pod DNS, `<pod>.<cluster>-<role>.<namespace>.svc.<clusterDomain>`, which embeds the **namespace name and the cluster domain**. Restore into a namespace of the same name on a cluster with the same `clusterDomain`, and the coordinators find their members. Restore into a differently named namespace, and they try to reach members that do not exist. The `memgraph-high-availability` chart has the same limitation; lifting it would need a core-side way to re-address a restored cluster.

The operator and the `MemgraphCluster` CRD must be installed on the target cluster before the resource is restored there. Secrets the resource references (the license, any TLS or basic-auth Secret) are not stored in the resource and have to be restored or re-created alongside it.

## What the chart's restore knobs became

The chart's per-instance `restoreDataFromSnapshot: true` / `volumeSnapshotName: <snapshot>` put a `dataSource` on that instance's `volumeClaimTemplate`, which the chart can express only because it runs one StatefulSet per instance. The operator runs one StatefulSet per role, so one claim template per role, and Kubernetes has no per-ordinal `dataSource`: the same field here would restore every pod of a role from one snapshot, which is wrong for coordinators and pointless for replicas. The knob's job — a claim provisioned from a named snapshot before the pod first starts — is step 2 above, done by hand. A per-role list from which the operator pre-creates the claims is the operator-shaped version of the knob; it is not built, and comes when someone asks.

## Not in scope

- **A backup feature of its own.** Scheduling snapshots, shipping them off-cluster and cataloguing them is what PX-Backup, Velero and Kasten do; the operator exposes the claims and stays out of the way.
- **Pausing reconciliation.** Restoring in place deletes and re-creates the resource because the operator would otherwise re-apply the StatefulSets while the claims are being swapped. An annotation that pauses it would let the resource stay; it is not built.
- **Memgraph's own `CREATE SNAPSHOT` and snapshot-file restore**, which work inside a volume rather than across them and are documented with Memgraph, not here.
