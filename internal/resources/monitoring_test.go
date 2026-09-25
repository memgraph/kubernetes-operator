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

package resources_test

import (
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
	"github.com/memgraph/kubernetes-operator/internal/resources"
)

const (
	monitoringComponent = "monitoring"
	monitoringLabel     = "memgraph.com/monitoring"
	monitoringMarker    = "true"
	releaseLabel        = "release"
	releaseName         = "kube-prometheus-stack"
	scrapeAnnotation    = "example.com/scrape-owner"
	scrapeOwner         = "platform"
	scrapeInterval      = "15s"
)

// monitoredCluster asks for a ServiceMonitor with every knob set, so the golden
// test pins each one.
func monitoredCluster() *memgraphcomv1alpha1.MemgraphCluster {
	cluster := minimalCluster()
	cluster.Spec.Monitoring = &memgraphcomv1alpha1.MonitoringSpec{
		ServiceMonitor: &memgraphcomv1alpha1.ServiceMonitorSpec{
			Labels:      map[string]string{releaseLabel: releaseName},
			Annotations: map[string]string{scrapeAnnotation: scrapeOwner},
			Interval:    scrapeInterval,
		},
	}
	return cluster
}

// expectedServiceMonitor is the ServiceMonitor of monitoredCluster: one
// endpoint on the metrics port, selecting the cluster's Services by identity
// labels, carrying the custom labels under the operator's own plus the marker
// the controller prunes by.
func expectedServiceMonitor() *monitoringv1.ServiceMonitor {
	labels := expectedLabelsWith(monitoringComponent, map[string]string{releaseLabel: releaseName})
	labels[monitoringLabel] = monitoringMarker
	return &monitoringv1.ServiceMonitor{
		TypeMeta: metav1.TypeMeta{APIVersion: "monitoring.coreos.com/v1", Kind: "ServiceMonitor"},
		ObjectMeta: metav1.ObjectMeta{
			Name:        clusterName,
			Namespace:   testNamespace,
			Labels:      labels,
			Annotations: map[string]string{scrapeAnnotation: scrapeOwner},
		},
		Spec: monitoringv1.ServiceMonitorSpec{
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{nameLabel: memgraphName, instanceLabel: clusterName},
			},
			Endpoints: []monitoringv1.Endpoint{{
				Port:     metricsPortName,
				Path:     "/metrics",
				Scheme:   ptr.To(monitoringv1.SchemeHTTP),
				Interval: scrapeInterval,
			}},
		},
	}
}

func TestServiceMonitor(t *testing.T) {
	got := resources.ServiceMonitor(monitoredCluster())
	if diff := cmp.Diff(expectedServiceMonitor(), got); diff != "" {
		t.Errorf("ServiceMonitor() mismatch (-want +got):\n%s", diff)
	}
}

// TestServiceMonitorEmptyBlock pins what an empty block gets: no interval, so
// Prometheus's global default applies; no annotations claimed; the operator's
// own labels alone.
func TestServiceMonitorEmptyBlock(t *testing.T) {
	cluster := minimalCluster()
	cluster.Spec.Monitoring = &memgraphcomv1alpha1.MonitoringSpec{
		ServiceMonitor: &memgraphcomv1alpha1.ServiceMonitorSpec{},
	}

	want := expectedServiceMonitor()
	want.Labels = expectedLabels(monitoringComponent)
	want.Labels[monitoringLabel] = monitoringMarker
	want.Annotations = nil
	want.Spec.Endpoints[0].Interval = ""

	got := resources.ServiceMonitor(cluster)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ServiceMonitor() mismatch (-want +got):\n%s", diff)
	}
}

// TestServiceMonitorIdentityLabelsWin: a custom label can never detach the
// ServiceMonitor from its cluster or from the marker the controller prunes by.
func TestServiceMonitorIdentityLabelsWin(t *testing.T) {
	cluster := monitoredCluster()
	cluster.Spec.Monitoring.ServiceMonitor.Labels = map[string]string{
		instanceLabel:   "someone-else",
		monitoringLabel: "false",
	}

	got := resources.ServiceMonitor(cluster)
	if got.Labels[instanceLabel] != clusterName {
		t.Errorf("instance label = %q, want %q", got.Labels[instanceLabel], clusterName)
	}
	if got.Labels[monitoringLabel] != monitoringMarker {
		t.Errorf("monitoring marker = %q, want %q", got.Labels[monitoringLabel], monitoringMarker)
	}
}

func TestUsesServiceMonitor(t *testing.T) {
	if resources.UsesServiceMonitor(minimalCluster()) {
		t.Error("UsesServiceMonitor() = true for a cluster without the block")
	}
	withEmptyMonitoring := minimalCluster()
	withEmptyMonitoring.Spec.Monitoring = &memgraphcomv1alpha1.MonitoringSpec{}
	if resources.UsesServiceMonitor(withEmptyMonitoring) {
		t.Error("UsesServiceMonitor() = true for a monitoring block without serviceMonitor")
	}
	if !resources.UsesServiceMonitor(monitoredCluster()) {
		t.Error("UsesServiceMonitor() = false for a cluster with the block")
	}
}

const (
	grafanaDashboardComponent = "grafana-dashboard"
	grafanaDashboardLabel     = "grafana_dashboard"
	grafanaDashboardName      = clusterName + "-grafana-dashboard"
	grafanaFolderAnnotation   = "grafana_folder"
	grafanaFolder             = "Memgraph"
	sidecarLabel              = "my_sidecar"
)

// dashboardCluster asks for the dashboard with every knob set, so the golden
// test pins each one — including that a named label set replaces the default
// rather than adding to it.
func dashboardCluster() *memgraphcomv1alpha1.MemgraphCluster {
	cluster := minimalCluster()
	cluster.Spec.Monitoring = &memgraphcomv1alpha1.MonitoringSpec{
		GrafanaDashboard: &memgraphcomv1alpha1.GrafanaDashboardSpec{
			Labels:      map[string]string{sidecarLabel: "yes"},
			Annotations: map[string]string{grafanaFolderAnnotation: grafanaFolder},
		},
	}
	return cluster
}

// expectedDashboardMeta is the ConfigMap of dashboardCluster without its data,
// which the JSON test below checks on its own: the dashboard is far too large
// to sit in a golden.
func expectedDashboardMeta() *corev1.ConfigMap {
	labels := expectedLabelsWith(grafanaDashboardComponent, map[string]string{sidecarLabel: "yes"})
	labels[monitoringLabel] = monitoringMarker
	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{
			Name:        grafanaDashboardName,
			Namespace:   testNamespace,
			Labels:      labels,
			Annotations: map[string]string{grafanaFolderAnnotation: grafanaFolder},
		},
	}
}

func TestGrafanaDashboard(t *testing.T) {
	got := resources.GrafanaDashboard(dashboardCluster())

	data := got.Data
	got.Data = nil
	if diff := cmp.Diff(expectedDashboardMeta(), got); diff != "" {
		t.Errorf("GrafanaDashboard() mismatch (-want +got):\n%s", diff)
	}

	// The data is the chart's dashboard, verbatim: one key, valid JSON, the
	// dashboard's own title, and the datasource template variable that lets it
	// bind to whichever Prometheus Grafana has.
	if len(data) != 1 {
		t.Fatalf("ConfigMap data has %d keys, want 1", len(data))
	}
	var dashboard struct {
		Title      string `json:"title"`
		Templating struct {
			List []struct {
				Name string `json:"name"`
			} `json:"list"`
		} `json:"templating"`
	}
	if err := json.Unmarshal([]byte(data[resources.GrafanaDashboardKey]), &dashboard); err != nil {
		t.Fatalf("dashboard JSON does not parse: %v", err)
	}
	if dashboard.Title != "Memgraph OpenMetrics" {
		t.Errorf("dashboard title = %q, want %q", dashboard.Title, "Memgraph OpenMetrics")
	}
	hasDatasource := false
	for _, variable := range dashboard.Templating.List {
		hasDatasource = hasDatasource || variable.Name == "datasource"
	}
	if !hasDatasource {
		t.Error("dashboard has no datasource template variable, so it could not bind to Grafana's Prometheus")
	}
}

// TestGrafanaDashboardDefaultLabel pins what an empty block gets: the label
// the kube-prometheus-stack sidecar selects on, and no annotations claimed.
func TestGrafanaDashboardDefaultLabel(t *testing.T) {
	cluster := minimalCluster()
	cluster.Spec.Monitoring = &memgraphcomv1alpha1.MonitoringSpec{
		GrafanaDashboard: &memgraphcomv1alpha1.GrafanaDashboardSpec{},
	}

	want := expectedDashboardMeta()
	want.Labels = expectedLabelsWith(grafanaDashboardComponent, map[string]string{grafanaDashboardLabel: "1"})
	want.Labels[monitoringLabel] = monitoringMarker
	want.Annotations = nil

	got := resources.GrafanaDashboard(cluster)
	got.Data = nil
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("GrafanaDashboard() mismatch (-want +got):\n%s", diff)
	}
}

func TestUsesGrafanaDashboard(t *testing.T) {
	if resources.UsesGrafanaDashboard(minimalCluster()) {
		t.Error("UsesGrafanaDashboard() = true for a cluster without the block")
	}
	if resources.UsesGrafanaDashboard(monitoredCluster()) {
		t.Error("UsesGrafanaDashboard() = true for a monitoring block without grafanaDashboard")
	}
	if !resources.UsesGrafanaDashboard(dashboardCluster()) {
		t.Error("UsesGrafanaDashboard() = false for a cluster with the block")
	}
}
