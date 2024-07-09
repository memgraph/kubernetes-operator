# Memgraph Kubernetes Operator

## Prerequisites

We use Go version 1.22.5. Check out here how to [install Go](https://go.dev/doc/install). Helm version is v3.14.4.


## Installation

```bash
git clone git@github.com:memgraph/kubernetes-operator.git
git checkout helm-operator (until merged)
git submodule init
git submodule update
```

Change andidocker8888 to your domain name to download latest kubernetes operator.
(This is just for developers, users will be able to download this from our DockerHub.)
```bash
make docker-build docker-push
make deploy
```

After following steps above you should be able to see `kubernetes-operator-controller-manager` deployment in `kubernetes-operator-system` namespace:
![image](https://github.com/memgraph/kubernetes-operator/assets/53269502/a4fc70fe-ef5b-4541-afd8-3ad3ee43a070)

This also causes creating `kubernetes-operator-controller-manager-768d9db99b-xs6hk` pod with 2 containers in `kubernetes-operator-system` namespace:
![image](https://github.com/memgraph/kubernetes-operator/assets/53269502/7220c1bd-588c-4662-b696-d43b3085eac3)


If you position yourself into operator-controller-manager pod with:
`kubectl exec -it -n kubernetes-operator-system kubernetes-operator-controller-manager-768d9db99b-xs6hk bash` and run ls, you should be able to see two items: `helm-charts` directory and watches.yaml.

Go to `config/samples/memgraph_v1_memghraphha.yaml` and provide your license information by setting `MEMGRAPH_ORGANIZATION_NAME` and `MEMGRAPH_ENTERPRISE_LICENSE`.

After approx. 60s, you should be able to see your cluster running with `kubectl get pods -A`:
![image](https://github.com/memgraph/kubernetes-operator/assets/53269502/069e2079-03f2-4827-83c1-b06a338b63e4)

Find URL of any coordinator instances:
![Screenshot from 2024-07-09 13-10-42](https://github.com/memgraph/kubernetes-operator/assets/53269502/fbc2d487-e258-4613-bc85-0484bcf2e0dd)

and connect to see the state of the cluster:
![image](https://github.com/memgraph/kubernetes-operator/assets/53269502/c68d52e2-19f7-4e45-8ff0-fc2ee662c64b)

Let's say I want to change `--log-level` flag for coordinators with id 1 and 2. I can do that in the following way:
![image](https://github.com/memgraph/kubernetes-operator/assets/53269502/87ff43e7-f5b1-4764-9fed-4d87758b0f77)

Only pods corresponding to these 2 coordinators will get restarted:
![image](https://github.com/memgraph/kubernetes-operator/assets/53269502/930ac553-31ad-4230-9e2e-f67858f3fe25)

## Clear resources

```
kubectl delete -f config/samples/memgraph_v1_memgraphha.yaml
make undeploy
```














