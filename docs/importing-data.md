# Importing data from CSV files

`LOAD CSV` reads a file from the filesystem of the instance that runs it, and an import writes, so it runs on the MAIN. Which data instance is the MAIN is not fixed: the Raft coordinators move it on failover, a rolling restart deletes the MAIN's pod and lets them elect another, and a scale-down hands it over before retiring an instance. A file copied into one pod is therefore only usable while that pod happens to be the MAIN, and copying it there has costs of its own:

- `/tmp` is an `emptyDir`, so the file is gone after the pod's next restart;
- `/var/lib/memgraph` is the instance's data volume, so a large file there takes space that snapshots and the WAL need;
- `kubectl cp` streams a tar through the API server, which is slow and fragile for files of many gigabytes.

The shape that works is the opposite: put the files once on one shared volume, and mount that volume read-only into **every** data instance. Whichever instance is the MAIN sees the same files at the same path, before and after a failover. The operator needs no feature for this; `spec.extraVolumes` and `spec.extraVolumeMounts` carry it.

## 1. Create a shared claim

The claim must be one that many pods on different nodes can mount at once: access mode `ReadWriteMany`. An ordinary block disk (Azure Disk, EBS, GCE PD, and the default storage class of most clouds) is `ReadWriteOnce` and attaches to one node; with the data instances spread across nodes, the first pod mounts it and the others stay in `ContainerCreating` with a multi-attach error. What offers `ReadWriteMany` is a network file share:

| Platform | Storage class |
| --- | --- |
| AKS | Azure Files (`azurefile-csi`, or `azurefile-csi-premium` for large files) |
| EKS | EFS (the `efs.csi.aws.com` driver) |
| GKE | Filestore (`standard-rwx`, `premium-rwx`) |
| Elsewhere | an NFS provisioner, CephFS, or any CSI driver offering `ReadWriteMany` |

Create the claim in the cluster's namespace, sized for the files:

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: csv-import
  namespace: memgraph
spec:
  accessModes: [ReadWriteMany]
  storageClassName: azurefile-csi
  resources:
    requests:
      storage: 500Gi
```

The share holds only the source files. The imported graph is written by the MAIN to its own `lib-storage` claim and replicated to each replica's, as always.

## 2. Put the files on it

Fill the share from outside Memgraph, so the copy has nothing to do with which instance is the MAIN and survives any restart of the cluster. Two ways:

- **Through the storage service.** A dynamically provisioned share is an ordinary share in your cloud account (on AKS, a file share in the storage account the driver created; the claim's PersistentVolume names it in `spec.csi.volumeHandle`), so `azcopy`, AWS DataSync, or a plain NFS mount on a workstation copies the files without going through Kubernetes at all. For files of many gigabytes this is the fast path.
- **Through a loader pod.** A throwaway pod mounting the claim read-write, and `kubectl cp` into it. Slower, but it needs nothing beyond `kubectl`, and the pod can be deleted as soon as the copy is done:

  ```yaml
  apiVersion: v1
  kind: Pod
  metadata:
    name: csv-loader
    namespace: memgraph
  spec:
    securityContext:
      runAsNonRoot: true
      runAsUser: 101
      runAsGroup: 103
      seccompProfile:
        type: RuntimeDefault
    containers:
      - name: loader
        image: busybox:1.37
        command: ["sleep", "infinity"]
        securityContext:
          allowPrivilegeEscalation: false
          capabilities:
            drop: [ALL]
        volumeMounts:
          - name: import
            mountPath: /import
    volumes:
      - name: import
        persistentVolumeClaim:
          claimName: csv-import
  ```

  ```sh
  kubectl cp -n memgraph ./nodes.csv csv-loader:/import/nodes.csv
  kubectl delete pod -n memgraph csv-loader
  ```

  The security context is there because a namespace enforcing the `restricted` Pod Security Standard rejects a pod without one.

Whichever way, the files must be readable by the identity Memgraph runs as (uid 101 and gid 103 unless `spec.securityContext` says otherwise). Azure Files mounts with permissive modes by default; on NFS, check the export's permissions.

## 3. Mount it into the data instances

```yaml
spec:
  extraVolumes:
    data:
      - name: csv-import
        persistentVolumeClaim:
          claimName: csv-import
          readOnly: true
  extraVolumeMounts:
    data:
      - name: csv-import
        mountPath: /import
        readOnly: true
```

Only the data instances need it; coordinators run no imports. Adding the mount changes the data instances' pod template, so it **rolls the data instances**: one pod at a time, the MAIN last, with the coordinators failing over to a replica when the MAIN's pod is deleted. Wait for the roll to finish before importing (give the operator a moment to observe the edit first, or `Updated` may still be the `True` from before it):

```sh
kubectl wait --namespace memgraph --for=condition=Updated memgraphcluster/memgraph --timeout=30m
```

## 4. Run the import on the MAIN

Find the MAIN and run `LOAD CSV` there, with paths under the mount:

```sh
kubectl get mgc memgraph -n memgraph -o jsonpath='{.status.main}'   # e.g. instance_1 → memgraph-data-1

kubectl exec -i -n memgraph memgraph-data-1 -c memgraph -- mgconsole <<'EOF'
LOAD CSV FROM "/import/nodes.csv" WITH HEADER AS row
CREATE (:Person {id: row.id, name: row.name});
EOF
```

The writes replicate to the other data instances like any other transaction. If a failover interrupts the import, the transaction that was running on the old MAIN is lost; the new MAIN sees the same files at the same path, so the import is re-run against it rather than started over from a copy. For speeding up large imports (indexes before loading, batching, the CSV options), see Memgraph's [import best practices](https://memgraph.com/docs/data-migration/best-practices) and [`LOAD CSV` reference](https://memgraph.com/docs/data-migration/csv).

## 5. Clean up

Once the import is done, the share is no longer needed. Remove the two `extraVolumes` and `extraVolumeMounts` entries (another roll of the data instances), then delete the claim:

```sh
kubectl delete pvc -n memgraph csv-import
```

Keep the mount instead if you import regularly; a share mounted read-only costs the instances nothing while it is idle.
