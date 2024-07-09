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

After following steps above you should be able to get the following output:







