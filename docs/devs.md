# Development of the Memgraph Kubernetes Operator

```
kubebuilder create api --group memgraph --version v1 --kind <OperatorName>
# Implement the reconcile loop under internal/controller/operatorname_controller.go
# Make sure the k8s cluster is running.
make install
make run
kubectl apply -f config/samples/memgraph_v1_operatorname.yaml # To create the custom resource.
kubectl get operatornames                                     # To get the list of all custom resources.
kubectl edit operatornames.memgraph.com operatorname-sample   # To change the custom resource.
```

NOTE: After code changes `make install` and `make run` have to be run again.
For the on the fly code changes, something like
[air](https://github.com/air-verse/air) is required.
