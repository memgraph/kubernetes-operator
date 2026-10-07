# Growing storage on a live cluster

Raise a size and the operator grows every claim behind it, under the running pods:

```sh
kubectl patch mgc memgraph -n memgraph --type=merge -p '{"spec":{"storage":{"data":{"libPVCSize":"50Gi"}}}}'
kubectl wait --namespace memgraph --for=condition=Converged memgraphcluster/memgraph --timeout=10m
```

The sizes that grow are `storage.<role>.libPVCSize` and `coreDumps.<role>.size` (while the role collects dumps), in any combination and in one edit. A size never shrinks: Kubernetes cannot shrink a volume, so admission refuses it. Everything else that backs a claim — the storage classes, the access modes, whether a role collects core dumps — stays fixed at creation, as before. With file logging on, the lib claim holds the log files too, so a claim filling up with logs is grown the same way, or relieved by lowering `log-retention-days` or `log-level` in `spec.flags`.

## Requirements

- **The StorageClass allows expansion** (`allowVolumeExpansion: true`). When it does not, the API server refuses the claim patch, the resource reports `Converged=False` with reason `VolumeExpansionRefused` naming the claim and quoting the refusal, and nothing else changes. Allowing expansion on the class is all it takes to continue; the patch is retried on every pass.
- **The storage driver grows a volume in use.** Every current CSI driver of a major cloud or storage system does — AWS EBS, GCE Persistent Disk, Azure Disk, Ceph RBD and CephFS, Longhorn 1.4 and later, Portworx, TopoLVM, NetApp Trident. See [drivers that cannot](#drivers-that-cannot-grow-a-volume-in-use) for the exceptions.

## What happens

1. **Every claim of the role is patched to the new size**, a claim retained from an earlier scale-down included, so a later re-grow does not reattach a pod to an undersized volume.
2. **The role's StatefulSet is deleted with its pods and claims orphaned and recreated** with the new claim templates, because Kubernetes forbids changing a live StatefulSet's `volumeClaimTemplates`. No pod is restarted: the StatefulSet controller adopts a running pod whose volumes name the claims its ordinal gets, which a size leaves as it was, and the pod template — what the restart revision hashes — is unchanged. From here on a claim the StatefulSet creates, on a scale-up, starts at the new size. While the StatefulSet is gone its replica count is read off the pods it left, so a count lowered in that moment is still carried out by retiring the members first.
3. **The storage provider grows each volume, and the kubelet its filesystem**, under the running pod; on most drivers the filesystem follows within a minute. The operator restarts nothing for it.

`Converged` stays `False` with reason `VolumeExpansionInProgress` until every claim a pod mounts holds the new size, naming each claim still growing and its pod. On a driver that grows volumes in use that takes about a minute. When the storage driver reports an error while it retries — the claim's `ControllerResizeError` or `NodeResizeError` condition — the message carries the first one, which is usually the reason it will not finish:

```
Waiting for lib-storage-memgraph-data-0 (pod memgraph-data-0), ... to reach the declared size. The storage driver
reports lib-storage-memgraph-data-0: ... "Change in disk property of VM of size 'Standard_A2_v2' is not supported." ...
``` A claim nothing mounts — retained from a scale-down — counts as grown once its volume has; its filesystem grows when a pod next mounts it. A resize the provider accepted and then gave up on, which the claim reports as `ControllerResizeInfeasible` or `NodeResizeInfeasible`, is `VolumeExpansionFailed` with the claim's error; retrying it is the provider's business.

## Drivers that cannot grow a volume in use

A driver that only expands offline refuses to grow a volume while any pod uses its claim, and the resizer retries once none does. A restart does not give it that moment — the StatefulSet recreates the pod at once — so such a resize needs the pod gone for its whole length. That is an outage, and choosing it is yours: the operator reports the resize as `VolumeExpansionInProgress` naming the pod, and waits. Known cases:

- vSphere CSI before v2.2, or on vCenter / ESXi before 7.0 Update 2;
- HPE SimpliVity CSI;
- Longhorn before v1.4, and its V2 data engine before v1.10 or on the UBLK frontend;
- Azure Disk on node VM sizes that cannot change an attached disk, such as the Av2 series: the resize fails with `OperationNotAllowed: Change in disk property of VM of size 'Standard_A2_v2' is not supported` and is retried forever. Move the data pods to a node pool whose VM size supports it, and later resizes run online;
- Azure Disk shared disks, and Standard HDD, Standard SSD and Premium SSD disks grown past 4 TiB;
- Cinder on an OpenStack cloud older than Cinder API microversion 3.42.

On one of those, Kubernetes has no way to keep a single StatefulSet pod down, so the resize needs the cluster stopped: with the size already raised, delete the MemgraphCluster (under the default `Retain` policy every claim stays), wait for the claims to report their new capacity, and apply the same resource again. The pods come back on the grown volumes and the cluster re-registers as after any restart.
