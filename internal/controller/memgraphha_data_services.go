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
	"fmt"

	"github.com/go-logr/logr"
	memgraphv1 "github.com/memgraph/kubernetes-operator/api/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
)

func (r *MemgraphHAReconciler) reconcileDataInstanceNodePortService(ctx context.Context, memgraphha *memgraphv1.MemgraphHA, logger *logr.Logger, dataInstanceId int) (bool, error) {
	serviceName := fmt.Sprintf("memgraph-data-%d-external", dataInstanceId)
	logger.Info("Started reconciling NodePort service", "NodePort", serviceName)

	dataInstanceNodePortService := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: serviceName, Namespace: memgraphha.Namespace}, dataInstanceNodePortService)

	if err == nil {
		logger.Info("NodePort already exists.", "NodePort", serviceName)
		return false, nil
	}

	if errors.IsNotFound(err) {
		nodePort := r.createDataInstanceNodePort(memgraphha, dataInstanceId)
		logger.Info("Creating a new NodePort", "NodePort.Namespace", nodePort.Namespace, "NodePort.Name", nodePort.Name)
		err := r.Create(ctx, nodePort)
		if err != nil {
			logger.Error(err, "Failed to create new NodePort", "NodePort.Namespace", nodePort.Namespace, "NodePort.Name", nodePort.Name)
			return true, err
		}
		logger.Info("NodePort is created.", "NodePort", serviceName)
		return true, nil
	}

	logger.Error(err, "Failed to fetch NodePort", "NodePort", serviceName)
	return true, err

}

func (r *MemgraphHAReconciler) createDataInstanceNodePort(memgraphha *memgraphv1.MemgraphHA, dataInstanceId int) *corev1.Service {
	serviceName := fmt.Sprintf("memgraph-data-%d-external", dataInstanceId)
	dataInstanceName := fmt.Sprintf("memgraph-data-%d", dataInstanceId)
	// TODO: (andi) Extract somehow configuration and move into separate files.
	boltPort := 7687

	dataInstanceNodePort := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: memgraphha.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeNodePort,
			Selector: createDataInstanceLabels(dataInstanceName),
			Ports: []corev1.ServicePort{
				{
					Name:       "bolt",
					Protocol:   corev1.ProtocolTCP,
					Port:       int32(boltPort),
					TargetPort: intstr.FromInt(boltPort),
				},
			},
		},
	}

	ctrl.SetControllerReference(memgraphha, dataInstanceNodePort, r.Scheme)
	return dataInstanceNodePort
}

func (r *MemgraphHAReconciler) reconcileDataInstanceClusterIPService(ctx context.Context, memgraphha *memgraphv1.MemgraphHA, logger *logr.Logger, dataInstanceId int) (bool, error) {
	serviceName := fmt.Sprintf("memgraph-data-%d", dataInstanceId)
	logger.Info("Started reconciling ClusterIP service", "ClusterIP", serviceName)

	dataInstanceClusterIPService := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: serviceName, Namespace: memgraphha.Namespace}, dataInstanceClusterIPService)

	if err == nil {
		logger.Info("ClusterIP already exists.", "ClusterIP", serviceName)
		return false, nil
	}

	if errors.IsNotFound(err) {
		clusterIP := r.createDataInstanceClusterIP(memgraphha, dataInstanceId)
		logger.Info("Creating a new ClusterIP", "ClusterIP.Namespace", clusterIP.Namespace, "ClusterIP.Name", clusterIP.Name)
		err := r.Create(ctx, clusterIP)
		if err != nil {
			logger.Error(err, "Failed to create new ClusterIP", "ClusterIP.Namespace", clusterIP.Namespace, "ClusterIP.Name", clusterIP.Name)
			return true, err
		}
		logger.Info("ClusterIP is created.", "ClusterIP", serviceName)
		return true, nil
	}

	logger.Error(err, "Failed to fetch ClusterIP", "ClusterIP", serviceName)
	return true, err

}

func (r *MemgraphHAReconciler) createDataInstanceClusterIP(memgraphha *memgraphv1.MemgraphHA, dataInstanceId int) *corev1.Service {
	serviceName := fmt.Sprintf("memgraph-data-%d", dataInstanceId)
	dataInstanceName := serviceName
	// TODO: (andi) Extract somehow configuration and move into separate files.
	boltPort := 7687
	replicationPort := 20000
	mgmtPort := 10000

	dataInstanceClusterIP := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: memgraphha.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: createDataInstanceLabels(dataInstanceName),
			Ports: []corev1.ServicePort{
				{
					Name:       "bolt",
					Protocol:   corev1.ProtocolTCP,
					Port:       int32(boltPort),
					TargetPort: intstr.FromInt(boltPort),
				},
				{
					Name:       "replication",
					Protocol:   corev1.ProtocolTCP,
					Port:       int32(replicationPort),
					TargetPort: intstr.FromInt(replicationPort),
				},
				{
					Name:       "management",
					Protocol:   corev1.ProtocolTCP,
					Port:       int32(mgmtPort),
					TargetPort: intstr.FromInt(mgmtPort),
				},
			},
		},
	}

	ctrl.SetControllerReference(memgraphha, dataInstanceClusterIP, r.Scheme)
	return dataInstanceClusterIP
}
