/*
Copyright 2024 Memgraph Ltd.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"os"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	memgraphv1 "github.com/memgraph/kubernetes-operator/api/v1"
)

// MemgraphMoveReconciler reconciles a MemgraphMove object
type MemgraphMoveReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=memgraph.com,resources=memgraphmoves,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=memgraph.com,resources=memgraphmoves/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=memgraph.com,resources=memgraphmoves/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the MemgraphMove object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.4/pkg/reconcile
func (r *MemgraphMoveReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var move memgraphv1.MemgraphMove
	if err := r.Get(ctx, req.NamespacedName, &move); err != nil {
		log.Error(err, "unable to fetch MemgraphMove")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.Info("Reading values", "foo", move.Spec.Foo, "databases", move.Spec.Databases)

	// If just testing, use port foward on the host.
	dbUri := "bolt://localhost:7687"
	// If running under cluster, ClusterIP service could be used.
	// dbUri := "bolt://mydb-memgraph.default.svc.cluster.local:7687"
	dbUser := ""
	dbPassword := ""
	query := "SHOW VERSION;"
	driver, err := neo4j.NewDriverWithContext(
		dbUri,
		neo4j.BasicAuth(dbUser, dbPassword, ""))
	if err != nil {
		log.Error(err, "failed to create the database driver")
		os.Exit(1)
	}
	defer driver.Close(ctx)
	err = driver.VerifyConnectivity(ctx)
	if err != nil {
		log.Error(err, "unable to connect to the database")
		os.Exit(1)
	}
	log.Info("connected to the database")
	session := driver.NewSession(ctx, neo4j.SessionConfig{})
	defer session.Close(ctx)
	result, err := session.Run(ctx, query, nil)
	if err != nil {
		log.Error(err, "unable to run the query")
		os.Exit(1)
	}
	for result.Next(ctx) {
		record := result.Record()
		version, _ := record.Get("version")
		log.Info("database version", "vesion", version)
	}
	_, err = result.Consume(ctx)
	if err != nil {
		log.Error(err, "unable to consume all the query results")
		os.Exit(1)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MemgraphMoveReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&memgraphv1.MemgraphMove{}).
		Named("memgraphmove").
		Complete(r)
}
