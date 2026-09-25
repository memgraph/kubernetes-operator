/*
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
*/

package resources

import (
	_ "embed"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
)

// MonitoringLabel marks every monitoring object the operator builds for a
// cluster, with MonitoringValue as its value. Like ExternalAccessLabel, it is
// what the controller lists by to find the objects a spec no longer describes
// and delete them; the two markers are distinct so that a change to one block
// never considers the other's objects.
const (
	MonitoringLabel = "memgraph.com/monitoring"
	MonitoringValue = "true"
)

// monitoringComponent labels the monitoring objects, which belong to neither
// role: one ServiceMonitor covers the whole cluster.
const monitoringComponent = "monitoring"

// metricsPath is where Memgraph serves OpenMetrics on the metrics port.
const metricsPath = "/metrics"

// MonitoringSelector matches every monitoring object of this cluster and
// nothing else: the objects the operator has to consider deleting when the
// spec stops describing them.
func MonitoringSelector(cluster *memgraphcomv1alpha1.MemgraphCluster) map[string]string {
	return map[string]string{
		instanceLabel:   cluster.Name,
		MonitoringLabel: MonitoringValue,
	}
}

// ServiceMonitorName is the name of the one ServiceMonitor the operator
// creates for a cluster that asks for it.
func ServiceMonitorName(cluster *memgraphcomv1alpha1.MemgraphCluster) string {
	return cluster.Name
}

// UsesServiceMonitor reports whether the cluster asked for a ServiceMonitor.
func UsesServiceMonitor(cluster *memgraphcomv1alpha1.MemgraphCluster) bool {
	return cluster.Spec.Monitoring != nil && cluster.Spec.Monitoring.ServiceMonitor != nil
}

// ServiceMonitor builds the ServiceMonitor scraping every instance of the
// cluster. It selects the cluster's Services by its identity labels, which
// both headless Services carry and the external Services carry too; only the
// headless ones publish a port named metrics, so only they yield targets. The
// headless Services publish not-ready addresses, so a recovering instance is
// scraped and fails, which is the up=0 a monitoring stack wants to see rather
// than a target that vanishes. The endpoint is plain HTTP on the metrics port,
// and names an interval only when the spec does, so Prometheus's own default
// applies otherwise.
//
// The builder is only called for a cluster whose spec carries the block: it
// reads the block's knobs and has nothing to build without them.
func ServiceMonitor(cluster *memgraphcomv1alpha1.MemgraphCluster) *monitoringv1.ServiceMonitor {
	spec := normalize(cluster.Spec)
	block := spec.monitoring.serviceMonitor

	labels := labels(cluster, monitoringComponent, block.labels)
	labels[MonitoringLabel] = MonitoringValue

	return &monitoringv1.ServiceMonitor{
		TypeMeta: metav1.TypeMeta{
			APIVersion: monitoringv1.SchemeGroupVersion.String(),
			Kind:       monitoringv1.ServiceMonitorsKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        ServiceMonitorName(cluster),
			Namespace:   cluster.Namespace,
			Labels:      labels,
			Annotations: emptyToNil(block.annotations),
		},
		Spec: monitoringv1.ServiceMonitorSpec{
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/name": "memgraph",
					instanceLabel:            cluster.Name,
				},
			},
			Endpoints: []monitoringv1.Endpoint{{
				Port:     metricsPortName,
				Path:     metricsPath,
				Scheme:   ptr.To(monitoringv1.SchemeHTTP),
				Interval: monitoringv1.Duration(block.interval),
			}},
		},
	}
}

// grafanaDashboardJSON is the "Memgraph OpenMetrics" Grafana dashboard, copied
// from charts/memgraph-high-availability/dashboards/memgraph_openmetrics.json
// in github.com/memgraph/helm-charts. It is a copy on purpose: fetching it at
// reconcile time would make the operator depend on the network, and the file
// changes a few times a year. Update it by copying the chart's file over this
// one when the chart's dashboard changes, as part of a release.
//
//go:embed dashboards/memgraph_openmetrics.json
var grafanaDashboardJSON string

// GrafanaDashboardKey is the key the dashboard JSON sits under in the
// ConfigMap, which is also the file name the Grafana sidecar writes it as.
const GrafanaDashboardKey = "memgraph_openmetrics.json"

// grafanaDashboardComponent labels the dashboard ConfigMap.
const grafanaDashboardComponent = "grafana-dashboard"

// GrafanaDashboardName is the name of the one ConfigMap the operator creates
// for a cluster that asks for the dashboard.
func GrafanaDashboardName(cluster *memgraphcomv1alpha1.MemgraphCluster) string {
	return cluster.Name + "-" + grafanaDashboardComponent
}

// UsesGrafanaDashboard reports whether the cluster asked for the dashboard.
func UsesGrafanaDashboard(cluster *memgraphcomv1alpha1.MemgraphCluster) bool {
	return cluster.Spec.Monitoring != nil && cluster.Spec.Monitoring.GrafanaDashboard != nil
}

// GrafanaDashboard builds the ConfigMap holding the dashboard JSON, labelled
// for the Grafana sidecar to find it: the block's labels, which default to the
// kube-prometheus-stack convention when it names none, under the operator's
// own identity labels and the marker the controller prunes by.
//
// The builder is only called for a cluster whose spec carries the block.
func GrafanaDashboard(cluster *memgraphcomv1alpha1.MemgraphCluster) *corev1.ConfigMap {
	spec := normalize(cluster.Spec)
	block := spec.monitoring.grafanaDashboard

	labels := labels(cluster, grafanaDashboardComponent, block.labels)
	labels[MonitoringLabel] = MonitoringValue

	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{
			Name:        GrafanaDashboardName(cluster),
			Namespace:   cluster.Namespace,
			Labels:      labels,
			Annotations: emptyToNil(block.annotations),
		},
		Data: map[string]string{GrafanaDashboardKey: grafanaDashboardJSON},
	}
}
