# Memgraph Kubernetes Operator

A Kubernetes operator for running [Memgraph](https://memgraph.com) high-availability clusters. It exposes a `MemgraphCluster` custom resource (API group `memgraph.com/v1alpha1`, short name `mgc`): declare the cluster topology in a single resource and the operator provisions the workloads, bootstraps HA registration, and continuously reconciles registration state.

> **Status: early development.** This repository was reset for a fresh operator effort; the previous attempt is preserved on the `archive/pre-operator-mvp` branch. The product requirements and issue slices driving the current work live in [`specs/operator-mvp/`](specs/operator-mvp/PRD.md).

## Description

The operator replaces the `memgraph-high-availability` Helm chart's fire-and-forget registration Job with a controller that continuously drives the cluster toward its declared topology: one StatefulSet per role (coordinators, data instances), automatic bootstrap and MAIN promotion, and automatic re-registration of instances that lose their registration state. See the [PRD](specs/operator-mvp/PRD.md) for the full design.

## Getting Started

### Prerequisites
- go version v1.24.6+
- docker version 17.03+.
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.

### To install the operator

The install chart is published to the Memgraph helm repository — the same one the Memgraph charts
come from. It ships the `MemgraphCluster` CRD, a least-privilege RBAC set, and the controller
Deployment:

```sh
helm repo add memgraph https://memgraph.github.io/helm-charts
helm repo update
helm install memgraph-operator memgraph/memgraph-operator \
  --namespace memgraph-operator-system --create-namespace --wait
```

The chart source lives in this repository, in
[`charts/memgraph-operator`](charts/memgraph-operator/README.md), and installs from there too:

```sh
helm install memgraph-operator ./charts/memgraph-operator \
  --namespace memgraph-operator-system --create-namespace --wait
```

Uninstall with `helm uninstall memgraph-operator --namespace memgraph-operator-system`. Helm never
deletes CRDs it installed, so remove the CRD explicitly (`kubectl delete crd
memgraphclusters.memgraph.com`) once no cluster needs it — see the
[chart README](charts/memgraph-operator/README.md) for the values and the upgrade caveat.

### To deploy a development build on the cluster
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/kubernetes-operator:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/kubernetes-operator:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

>**NOTE**: Ensure that the samples has default values to test it out.

### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Project Distribution

Following the options to release and provide this solution to the users.

### By providing a bundle with all YAML files

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=<some-registry>/kubernetes-operator:tag
```

**NOTE:** The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without its
dependencies.

2. Using the installer

Users can just run 'kubectl apply -f <URL for YAML BUNDLE>' to install
the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/kubernetes-operator/<tag or branch>/dist/install.yaml
```

### By providing a Helm chart

The install chart is maintained in this repository under
[`charts/memgraph-operator`](charts/memgraph-operator/README.md), next to the manifests it ships:
its CRDs and the manager's RBAC rules are generated from the Go types and the
`+kubebuilder:rbac` markers, so the chart can never drift from the controller version it
installs.

```sh
make chart-sync      # regenerate the chart's CRDs and RBAC rules after changing the API or markers
make helm-lint       # lint the chart and render it with defaults and with the toggles flipped
make test-chart      # install/uninstall the chart on a throwaway Kind cluster
```

Pushing a version tag cross-publishes the packaged chart into the existing
[`memgraph.github.io/helm-charts`](https://memgraph.github.io/helm-charts) index, so users install
it from the helm repository they already have configured. The chart version and the operator
version move independently — `v0.2.0` releases the operator, `chart-0.4.2` releases the chart
alone. See [`docs/releasing.md`](docs/releasing.md) for the procedure, the dry-run and prerelease
paths, and the credentials involved.

## Contributing

Development is sliced into PR-gated issues under [`specs/operator-mvp/issues/`](specs/operator-mvp/issues). Every pull request runs lint, unit, and envtest suites.

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

