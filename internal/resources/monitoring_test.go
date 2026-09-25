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
	"testing"

	"github.com/google/go-cmp/cmp"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
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
