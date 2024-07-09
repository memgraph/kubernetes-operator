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




