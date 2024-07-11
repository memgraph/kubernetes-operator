# Install Memgraph Kubernetes Operator

## Option I: Install using Makefile

Make sure to clone this repository with its submodule (helm-charts).

```bash
git clone --recurse-submodules git@github.com:memgraph/kubernetes-operator.git
```

After this you can start your operator with a single command:

```bash
make deploy
```

This command will use operator's image from Memgraph's DockerHub and create all necessary Kubernetes resources for running an operator.

## Option II: Install using kubectl

```bash
kubectl apply -k config/default
```

## Verify installation

Installation using any of options described above will cause creating Kubernetes ServiceAccount, RoleBinding, Role, Deployment and Pods all in newly created all in newly created all in newly created
namespace `memgraph-operator-system`. You can check your resources with:

```bash
kubectl get serviceaccounts -A
kubectl get rolebindings -A
kubectl get roles -A
kubectl get deployments -A
kubectl get pods -A
```

CustomResourceDefinition `memgraphhas.memgraph.com`, whose job is to monitor CustomResource `MemgraphHA`, will also get created and you can verify
this with:

```bash
kubectl get crds -A
```

## Start Memgraph High Availability Cluster

We already provide sample cluster in `config/samples/memgraph_v1_ha.yaml`. You only need to set your license information by setting 
`MEMGRAPH_ORGANIZATION_NAME` and `MEMGRAPH_ENTERPRISE_LICENSE` environment variables in the sample file.

Start Memgraph HA cluster with `kubectl apply -f config/samples/memgraph_v1_ha.yaml`.

After approx. 60s, you should be able to see your cluster running with `kubectl get pods -A`:
![image](https://github.com/memgraph/kubernetes-operator/assets/53269502/069e2079-03f2-4827-83c1-b06a338b63e4)

Find URL of any coordinator instances:
![Screenshot from 2024-07-09 13-10-42](https://github.com/memgraph/kubernetes-operator/assets/53269502/fbc2d487-e258-4613-bc85-0484bcf2e0dd)

and connect to see the state of the cluster:
![image](https://github.com/memgraph/kubernetes-operator/assets/53269502/c68d52e2-19f7-4e45-8ff0-fc2ee662c64b)


## Clear resources

```bash
kubectl delete -f config/samples/memgraph_v1_ha.yaml
make undeploy / or kubectl delete -k config/default
```
