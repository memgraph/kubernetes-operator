/*
copyright 2024 memgraph ltd.

licensed under the apache license, version 2.0 (the "license");
you may not use this file except in compliance with the license.
you may obtain a copy of the license at

    http://www.apache.org/licenses/license-2.0

unless required by applicable law or agreed to in writing, software
distributed under the license is distributed on an "as is" basis,
without warranties or conditions of any kind, either express or implied.
see the license for the specific language governing permissions and
limitations under the license.
*/

package controller

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	memgraphv1 "github.com/memgraph/kubernetes-operator/api/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
)

func (r *MemgraphHAReconciler) reconcileSetupJob(ctx context.Context, memgraphha *memgraphv1.MemgraphHA, logger *logr.Logger) (bool, error) {
	name := fmt.Sprintf("memgraph-setup")
	logger.Info("Started reconciling", "Job", name)
	setupJob := &batchv1.Job{}
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: memgraphha.Namespace}, setupJob)

	if err == nil {
		logger.Info("SetupJob already exists.", "Job", name)
		return false, nil
	}

	if errors.IsNotFound(err) {
		job := r.createSetupJob(memgraphha)
		logger.Info("Creating a new SetupJob", "SetupJob.Namespace", job.Namespace, "SetupJob.Name", job.Name)
		err := r.Create(ctx, job)
		if err != nil {
			logger.Error(err, "Failed to create new SetupJob", "SetupJob.Namespace", job.Namespace, "SetupJob.Name", job.Name)
			return true, err
		}
		logger.Info("SetupJob is created.", "Job", name)
		return true, nil
	}

	logger.Error(err, "Failed to fetch SetupJob", "Job", name)
	return true, err

}

func (r *MemgraphHAReconciler) createSetupJob(memgraphha *memgraphv1.MemgraphHA) *batchv1.Job {
	image := "memgraph/memgraph:2.18.1"
	containerName := "memgraph-setup"
	runAsUser := int64(0)
	backoffLimit := int32(4)

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      containerName,
			Namespace: memgraphha.Namespace,
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:    containerName,
							Image:   image,
							Command: []string{"/bin/bash", "-c"},
							Args: []string{`
                              	echo "Installing netcat..."
                                apt-get update && apt-get install -y netcat-openbsd
                                echo "Waiting for pods to become available for Bolt connection..."
                                until nc -z memgraph-coordinator-1.default.svc.cluster.local 7687; do sleep 1; done
                                until nc -z memgraph-coordinator-2.default.svc.cluster.local 7687; do sleep 1; done
                                until nc -z memgraph-coordinator-3.default.svc.cluster.local 7687; do sleep 1; done
                                until nc -z memgraph-data-0.default.svc.cluster.local 7687; do sleep 1; done
                                until nc -z memgraph-data-1.default.svc.cluster.local 7687; do sleep 1; done
                                echo "Pods are available for Bolt connection!"
                                sleep 5
                                echo "Running mgconsole commands..."
																echo 'ADD COORDINATOR 2 WITH CONFIG {"bolt_server": "memgraph-coordinator-2.default.svc.cluster.local:7687", "management_server":  "memgraph-coordinator-2.default.svc.cluster.local:10000", "coordinator_server":  "memgraph-coordinator-2.default.svc.cluster.local:12000"};' | mgconsole --host memgraph-coordinator-1.default.svc.cluster.local --port 7687
          											echo 'ADD COORDINATOR 3 WITH CONFIG {"bolt_server": "memgraph-coordinator-3.default.svc.cluster.local:7687", "management_server":  "memgraph-coordinator-3.default.svc.cluster.local:10000", "coordinator_server":  "memgraph-coordinator-3.default.svc.cluster.local:12000"};' | mgconsole --host memgraph-coordinator-1.default.svc.cluster.local --port 7687
          											echo 'REGISTER INSTANCE instance_1 WITH CONFIG {"bolt_server": "memgraph-data-0.default.svc.cluster.local:7687", "management_server": "memgraph-data-0.default.svc.cluster.local:10000", "replication_server": "memgraph-data-0.default.svc.cluster.local:20000"};' | mgconsole --host memgraph-coordinator-1.default.svc.cluster.local --port 7687
          											echo 'REGISTER INSTANCE instance_2 WITH CONFIG {"bolt_server": "memgraph-data-1.default.svc.cluster.local:7687", "management_server": "memgraph-data-1.default.svc.cluster.local:10000", "replication_server": "memgraph-data-1.default.svc.cluster.local:20000"};' | mgconsole --host memgraph-coordinator-1.default.svc.cluster.local --port 7687
          											echo 'SET INSTANCE instance_1 TO MAIN;' | mgconsole --host memgraph-coordinator-1.default.svc.cluster.local --port 7687
          											sleep 3
          											echo "SHOW INSTANCES on coord1"
          											echo 'SHOW INSTANCES;' | mgconsole --host memgraph-coordinator-1.default.svc.cluster.local --port 7687
          											echo "SHOW INSTANCES on coord2"
          											echo 'SHOW INSTANCES;' | mgconsole --host memgraph-coordinator-2.default.svc.cluster.local --port 7687
          											echo "SHOW INSTANCES on coord3"
          											echo 'SHOW INSTANCES;' | mgconsole --host memgraph-coordinator-3.default.svc.cluster.local --port 7687
          											echo "RETURN 0 on 1st data instance"
          											echo 'RETURN 0;' | mgconsole --host memgraph-data-0.default.svc.cluster.local --port 7687
          											echo "RETURN 0 on 2nd data instance"
          											echo 'RETURN 0;' | mgconsole --host memgraph-data-1.default.svc.cluster.local --port 7687
                            `},
							SecurityContext: &corev1.SecurityContext{
								RunAsUser: &runAsUser,
							},
						},
					},
					RestartPolicy: corev1.RestartPolicyNever,
				},
			},
			BackoffLimit: &backoffLimit,
		},
	}

	ctrl.SetControllerReference(memgraphha, job, r.Scheme)
	return job
}
