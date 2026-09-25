# External access: reaching a cluster from outside Kubernetes

`spec.externalAccess` exposes a `MemgraphCluster` to clients outside the Kubernetes cluster. This document is the contract behind the [README's walkthrough](../README.md#external-access): what the operator creates, how it decides which address to announce, what each condition means while that happens, and what the two modes need from the cluster they run on.

## Why the operator owns it

A Memgraph HA client does not connect to a data instance by address. It connects to a coordinator, asks for the routing table, and follows the addresses in it: the MAIN for writes, a replica for reads. Those addresses are the `bolt_server` each member was registered with, and the operator is what registers them. So a `Service` of type `LoadBalancer` somebody creates by hand is not external access: clients reach the coordinator and are handed pod DNS names they cannot resolve.

External access is therefore two things done together, and the operator does both:

1. **The way in.** The Services, or the Gateway and its routes, that carry traffic from outside to each member.
2. **The routing table.** Every exposed member is registered at the address clients reach it through, and re-registered with `UPDATE CONFIG` whenever that address appears, changes or goes away.

Only the bolt address ever leaves the cluster network. The management, replication and coordinator addresses stay on pod DNS, because the cluster uses them to reach its own members and `UPDATE CONFIG` cannot change them anyway. The operator itself always dials coordinators at their pod addresses too, never through the LoadBalancer or Gateway.

## The two modes

Both modes expose the same two things: one address shared by all coordinators, because any coordinator answers a routing request, and one address per data instance, because the routing table names each one individually. Both build the same Services, one shared by the coordinators and one per data instance selecting its pod by the StatefulSet pod-name label, each publishing the bolt port and nothing else.

### `type: LoadBalancer`

The Services are of type `LoadBalancer`. The cloud gives each one an address, and that address is announced. A cluster of three coordinators and two data instances costs three cloud load balancers; every added data instance costs one more.

```yaml
spec:
  externalAccess:
    type: LoadBalancer
    coordinators:
      labels: {}
      annotations: {}      # on the coordinators' shared Service
    data:
      labels: {}
      annotations: {}      # on each data Service, {ordinal} substituted
```

### `type: Gateway`

The Services are ClusterIPs behind one Gateway API `Gateway` the operator creates: a TCP listener shared by all coordinators on the bolt port 7687, and one TCP listener per data instance on `gateway.dataPortBase + ordinal`, each fed by a `TCPRoute` to the instance's Service. One address, one cloud load balancer, a port per instance. The Gateway is operator-owned rather than attached to one you run because TCPRoute has no host matching, so the listener list is a function of `dataInstances`: raising the count adds a listener, lowering it removes one.

```yaml
spec:
  externalAccess:
    type: Gateway
    gateway:
      gatewayClassName: eg   # required: the class whose controller programs the Gateway
      dataPortBase: 9000     # data instance N listens on 9000 + N
      labels: {}
      annotations: {}        # on the Gateway object
    coordinators:
      annotations: {}        # on the coordinators' TCPRoute
    data:
      annotations: {}        # on each data TCPRoute, {ordinal} substituted
```

The port base is a knob because these are the ports you open on a firewall, and it defaults so a fresh cluster needs nothing. Admission rejects a base at or below 7687, where a data listener would land on the coordinators', and a range that runs past the end of the port space for the declared `dataInstances`.

#### What the cluster must have first

The operator never bundles the Gateway API CRDs. They belong to whoever installs a Gateway controller, and every controller ships them. Two things have to be true before `type: Gateway` works:

- **Gateway API v1.6 or newer.** The operator builds `TCPRoute` at `gateway.networking.k8s.io/v1`. TCPRoute graduated to `v1` in Gateway API v1.6; older releases serve it as `v1alpha2` alone, and v1.6 stops serving `v1alpha2`, so there is no single version that works on both sides of that line. Envoy Gateway v1.9 is the first release to bundle v1.6.
- **A GatewayClass** whose controller is running, named in `gateway.gatewayClassName`.

The operator checks once, when it starts, whether `Gateway` and `TCPRoute` are served at `v1`. If they are not, a cluster asking for `type: Gateway` reports `Converged=False` with reason `ApplyFailed` and a message naming what is missing, for example `TCPRoute is served only as gateway.networking.k8s.io/v1alpha2`. The cluster keeps running and serving in-cluster at pod addresses. Install what is missing, then **restart the operator**: the check is not repeated while it runs.

**Helm does not upgrade CRDs.** This is the trap to know about. Upgrading a Gateway controller with `helm upgrade`, or installing a new version over old CRDs, leaves the Gateway API CRDs exactly as they were, because Helm installs the files in a chart's `crds/` directory only when they are absent. Moving a cluster from an older Gateway API to v1.6 is always an explicit step:

```sh
kubectl apply --server-side --force-conflicts \
  -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.6.1/experimental-install.yaml
kubectl get crd tcproutes.gateway.networking.k8s.io -o jsonpath='{.spec.versions[*].name}'   # v1 v1alpha2
kubectl -n memgraph-operator-system rollout restart deploy/memgraph-operator-controller-manager
```

Server-side apply is needed because the CRDs exceed the client-side annotation size, and `--force-conflicts` takes field ownership from whatever manager created them. Use the channel your Gateway controller installs; Envoy Gateway installs the experimental one. Both channels serve TCPRoute at `v1`.

## Which address is announced

The address is discovered from the objects the operator creates, never declared in the spec. For each exposed member it is taken in this order, and the first one that exists wins:

| Priority | LoadBalancer mode | Gateway mode |
| --- | --- | --- |
| 1 | `external-dns.alpha.kubernetes.io/hostname` annotation on the member's Service | the same annotation on the member's TCPRoute, then on the Gateway |
| 2 | hostname the LoadBalancer reports in the Service status | hostname the Gateway reports in its status |
| 3 | IP the LoadBalancer reports | IP the Gateway reports |
| 4 | the pod's own DNS name, until any of the above exists | the same |

The port is 7687 in LoadBalancer mode and for the coordinators in Gateway mode, and `dataPortBase + ordinal` for a data instance behind a Gateway.

The external-dns annotation comes first because external-dns writes the DNS record at the provider and never back into the Service or Gateway, so the annotation is the only place the name you want clients to use can be found. The operator knows exactly this one third-party annotation. If the value is a comma-separated list, the first name is announced.

Nothing is remembered between passes. The desired address is recomputed from the current objects on every reconcile, and the planner issues `UPDATE CONFIG FOR INSTANCE` or `UPDATE CONFIG FOR COORDINATOR` for every member whose observed `bolt_server` differs from it. That is what makes the three situations below need no special handling:

- **Bootstrap raced the cloud.** Pods became ready before the LoadBalancer had an address, so the member was registered at its pod address. One `UPDATE CONFIG` moves it when the address appears. The common case is the reverse: a LoadBalancer is provisioned in seconds and a Memgraph pod takes longer to become ready, so most members are registered at their external address in one step.
- **The address changed.** A LoadBalancer was recreated with a new IP, or you added an external-dns annotation to a running cluster.
- **The block was removed.** Every member is moved back to its pod address.

Members a lowered count is retiring are left at whatever address they had; they are on their way out.

## Per-instance hostnames: `{ordinal}`

In LoadBalancer mode every data instance has its own LoadBalancer and needs its own hostname, and one annotation value copied onto every data Service would register the same DNS name for every instance. So every annotation value on a per-instance object has `{ordinal}` replaced with the pod ordinal:

```yaml
externalAccess:
  type: LoadBalancer
  coordinators:
    annotations:
      external-dns.alpha.kubernetes.io/hostname: memgraph.example.com
  data:
    annotations:
      external-dns.alpha.kubernetes.io/hostname: data-{ordinal}.memgraph.example.com
```

registers the coordinators at `memgraph.example.com:7687`, `instance_0` at `data-0.memgraph.example.com:7687`, `instance_1` at `data-1.memgraph.example.com:7687`, and a third instance added later at `data-2.memgraph.example.com:7687` with nothing further from you. Admission rejects a data hostname annotation without `{ordinal}`, because it can only produce duplicate routing entries, and a coordinators hostname with it, because all coordinators share one address. The same rule holds in Gateway mode, where the annotations land on the TCPRoutes: the ports already tell instances apart there, but a hostname per instance is what external-dns's TCPRoute source publishes, so it is the same shape.

Labels are never substituted; `{` is not valid in a label value anyway.

## What the status and conditions say

```sh
kubectl get mgc memgraph -n memgraph -o jsonpath='{.status.externalAccess}' | jq
```

```json
{
  "coordinators": "203.0.113.10:7687",
  "data": [
    {"name": "instance_0", "address": "203.0.113.11:7687"},
    {"name": "instance_1", "address": "203.0.113.12:7687"}
  ]
}
```

`status.externalAccess` is what the operator drives the registered addresses toward. An entry whose `address` is absent is a member still waiting for its LoadBalancer or the Gateway. The block is absent on an unexposed cluster.

`Converged` is what tells you whether the routing table has caught up:

| `Converged` | Reason | Meaning |
| --- | --- | --- |
| `False` | `RegistrationInProgress` | `UPDATE CONFIG` commands are being issued to move members onto their external addresses. |
| `False` | `ExternalAddressPending` | Every member is registered, but a LoadBalancer or the Gateway has no address yet; the message names it. Members behind it are announced at their pod addresses for now. If it persists, nothing is provisioning the address: no cloud controller or address pool answers the Service, or no controller programs the GatewayClass. |
| `False` | `ApplyFailed` | The exposure cannot be served on this cluster at all; the message says what to install. Waiting does not clear it. |
| `True` | `AllInstancesRegistered` | Every member is registered at the address in `status.externalAccess`. |

So `kubectl wait --for=condition=Converged` on an exposed cluster means "reachable at the addresses in status", which is what a client outside the cluster wants to wait for. `Ready` is untouched by any of this: a MAIN is serving and in-cluster clients work throughout.

## Lifecycle

- **Growing.** A data instance added by raising `dataInstances` gets its Service, or its listener and route, in the same pass that widens the StatefulSet, ahead of its pod. By the time the pod is ready the address usually exists, and the instance is registered at it in one step.
- **Shrinking.** A retiring data instance keeps its way in for as long as it keeps its pod; it may still be serving clients as MAIN while its handover waits for a caught-up survivor. The pass that sheds the pod is the pass that drops its Service, listener and route.
- **Switching type.** Change `type` and, when leaving Gateway mode, remove the `gateway` block in the same edit; admission rejects a `gateway` block with `type: LoadBalancer`. The Services flip type in place, the objects the other mode owned are deleted, and every member passes through its pod address until the new way in has an address. Clients briefly see in-cluster addresses in their routing table during that window.
- **Removing the block.** The Services, Gateway and routes are deleted, and every member is moved back to its pod address. Nothing is left behind, and nothing has to be undone by hand.

The external objects carry an owner reference to the `MemgraphCluster`, so deleting the cluster removes them through garbage collection like everything else. Removing them while the cluster stays is the one thing garbage collection cannot do, which is why the operator's ClusterRole has `delete` on Services, Gateways and TCPRoutes. It never deletes a headless Service or a StatefulSet.

## Connecting from outside

Use the routing scheme and the coordinators' address alone; the driver fetches the routing table from there and follows it:

```
neo4j://203.0.113.10:7687
```

A `bolt://` connection straight to a data instance's address also works, but then it is your client, not the routing table, that has to know which instance is MAIN.

TLS is not part of external access yet: the addresses announced are plain Bolt, so treat the network path accordingly. Bolt authentication is likewise still to come.

## Not in scope

- **A declared hostname without external-dns.** The address is always discovered. If you run your own DNS and want the name in the routing table rather than the IP, the external-dns annotation is currently the only hook, and it works whether or not external-dns is actually running: the operator reads the annotation, not the DNS record.
- **Attaching to a Gateway you run**, rather than the operator creating one. The listener list changes with `dataInstances`, so the operator has to own it.
- **NodePort.** It ends in "run `UPDATE CONFIG` by hand", which is the gap the operator exists to close.
- **`externalTrafficPolicy`, `loadBalancerSourceRanges`, `loadBalancerClass`** and the rest of the Service spec beyond labels and annotations.
