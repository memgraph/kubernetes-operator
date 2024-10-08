# Install Memgraph Kubernetes Operator

All described installation options will run the Operator inside the cluster.

Make sure to clone this repository with its submodule (helm-charts).

```bash
git clone --recurse-submodules git@github.com:memgraph/kubernetes-operator.git
```

## Install K8 Resources

```bash
make deploy
```

This command will use operator's image from Memgraph's DockerHub and create all necessary Kubernetes resources for running an operator.

## Verify Installation

Installation using any of the options described above will create a Kubernetes ServiceAccount, RoleBinding, Role, Deployment, and Pods all in the newly created namespace `memgraph-operator-system`. You can check your resources with:

```bash
kubectl get serviceaccounts -n memgraph-operator-system
kubectl get clusterrolebindings -n memgraph-operator-system
kubectl get clusterroles -n memgraph-operator-system
kubectl get deployments -n memgraph-operator-system
kubectl get pods -n memgraph-operator-system
kubectl get services -n memgraph-operator-system
```

CustomResourceDefinition `memgraphhas.memgraph.com`, whose job is to monitor CustomResource `MemgraphHA`, will also get created and you can verify
this with:

```bash
kubectl get crds -A
```

## Start Memgraph High Availability Cluster

We already provide a sample cluster in `config/samples/memgraph_v1_ha.yaml`. You only need to set your license information by
creating a Kubernetes Secret containing licensing info. You can do this in a following way:

```bash
 kubectl create secret generic memgraph-secrets \
--from-literal=MEMGRAPH_ENTERPRISE_LICENSE="<YOUR_LICENSE_INFO>" \
--from-literal=MEMGRAPH_ORGANIZATION_NAME="<YOUR_ORGANIZATION_NAME>"
```

Start Memgraph HA cluster with `kubectl apply -f config/samples/memgraph_v1_ha.yaml`.

After approximately 60 seconds, you should be able to see instances in the output of `kubectl get pods -A`.

You can now find the URL of any coordinator instances by running e.g `minikube service list` and connect to see the state of the cluster by running
`show instances;`:
![image](https://github.com/memgraph/kubernetes-operator/assets/53269502/c68d52e2-19f7-4e45-8ff0-fc2ee662c64b)

## Clear Resources

```bash
kubectl delete -f config/samples/memgraph_v1_ha.yaml # For deleting cluster
kubectl delete pvc --all  # Or leave them if you want to use persistent storage
kubectl delete secret memgraph-secrets
make undeploy
```
