# Install Memgraph Kubernetes Operator

## Install

Make sure to clone this repository with its submodule (helm-charts).

```bash
git clone --recurse-submodules git@github.com:memgraph/kubernetes-operator.git
```

After this you can start your operator with a single command:

```bash
make deploy
```

This command will use operator's image from Memgraph's DockerHub and create all necessary Kubernetes resources for running an operator.

## Verify installation

To verify that deployment ran successfully please run:

```bash
kubectl get deployments -A

```

and you should be able to see `kubernetes-operator-controller-manager` deployment in `kubernetes-operator-system` namespace:
![image](https://github.com/memgraph/kubernetes-operator/assets/53269502/a4fc70fe-ef5b-4541-afd8-3ad3ee43a070)

Together with the deployment, `kubernetes-operator-controller-manager-768d9db99b-xs6hk` pod with 2 containers in
`kubernetes-operator-system` namespace should also get created and you can verify this with:
![image](https://github.com/memgraph/kubernetes-operator/assets/53269502/7220c1bd-588c-4662-b696-d43b3085eac3)


If you position yourself into operator-controller-manager pod with:
`kubectl exec -it -n kubernetes-operator-system kubernetes-operator-controller-manager-768d9db99b-xs6hk bash` and run ls, 
you should be able to see two items: `helm-charts` directory and `watches.yaml` file.

## Start Memgraph High Availability Cluster

We already provide sample cluster in `config/samples/memgraph_v1_ha.yaml`. You only need to set your license information by setting 
`MEMGRAPH_ORGANIZATION_NAME` and `MEMGRAPH_ENTERPRISE_LICENSE` environment variables.

Start Memgraph HA cluster with `kubectl apply -f config/samples/memgraph_v1_ha.yaml`.

After approx. 60s, you should be able to see your cluster running with `kubectl get pods -A`:
![image](https://github.com/memgraph/kubernetes-operator/assets/53269502/069e2079-03f2-4827-83c1-b06a338b63e4)

Find URL of any coordinator instances:
![Screenshot from 2024-07-09 13-10-42](https://github.com/memgraph/kubernetes-operator/assets/53269502/fbc2d487-e258-4613-bc85-0484bcf2e0dd)

and connect to see the state of the cluster:
![image](https://github.com/memgraph/kubernetes-operator/assets/53269502/c68d52e2-19f7-4e45-8ff0-fc2ee662c64b)

## Clear resources

```
kubectl delete -f config/samples/memgraph_v1_ha.yaml
make undeploy
```
