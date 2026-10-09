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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
	"github.com/memgraph/kubernetes-operator/internal/resources"
)

const (
	appsAPIVersion    = "apps/v1"
	deploymentKind    = "Deployment"
	vmagentComponent  = "vmagent"
	vmagentDataVolume = "data"
	vmagentName       = clusterName + "-vmagent"
	vmagentConfigName = vmagentName + "-config"
	vmagentImage      = "registry.example.com/victoriametrics/vmagent:v1.140.0"
	remoteWriteURL    = "http://vmsingle.monitoring.svc.cluster.local:8428/api/v1/write"
	basicAuthSecret   = "monitoring-basic-auth"
)

// vmagentCluster asks for a vmagent with every knob set, so the golden tests
// pin each one.
func vmagentCluster() *memgraphcomv1alpha1.MemgraphCluster {
	cluster := minimalCluster()
	cluster.Spec.Monitoring = &memgraphcomv1alpha1.MonitoringSpec{
		VMAgentRemote: &memgraphcomv1alpha1.VMAgentRemoteSpec{
			Image: memgraphcomv1alpha1.VMAgentImageSpec{
				Repository: "registry.example.com/victoriametrics/vmagent",
				Tag:        "v1.140.0",
				PullPolicy: corev1.PullAlways,
			},
			RemoteWrite: memgraphcomv1alpha1.RemoteWriteSpec{
				URL:       remoteWriteURL,
				BasicAuth: &memgraphcomv1alpha1.BasicAuthSecretSpec{SecretName: basicAuthSecret},
			},
			ScrapeInterval: "30s",
			ExternalLabels: map[string]string{"cluster": "production", "team": "platform"},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")},
			},
		},
	}
	return cluster
}

// expectedVMAgentLabels is the label set of both vmagent objects: the identity
// labels of a component of their own plus the marker the controller prunes by.
func expectedVMAgentLabels() map[string]string {
	labels := expectedLabels(vmagentComponent)
	labels[monitoringLabel] = monitoringMarker
	return labels
}

// expectedScrapeConfig is the scrape config of vmagentCluster at three
// coordinators and two data instances: one job over every pod by DNS on the
// metrics port, the interval and external labels under global.
const expectedScrapeConfig = `global:
  external_labels:
    cluster: production
    team: platform
  scrape_interval: 30s
scrape_configs:
- job_name: memgraph
  metrics_path: /metrics
  static_configs:
  - targets:
    - example-coordinator-0.example-coordinator.memgraph-test.svc.cluster.local:9091
    - example-coordinator-1.example-coordinator.memgraph-test.svc.cluster.local:9091
    - example-coordinator-2.example-coordinator.memgraph-test.svc.cluster.local:9091
    - example-data-0.example-data.memgraph-test.svc.cluster.local:9091
    - example-data-1.example-data.memgraph-test.svc.cluster.local:9091
`

func expectedVMAgentConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmagentConfigName,
			Namespace: testNamespace,
			Labels:    expectedVMAgentLabels(),
		},
		Data: map[string]string{"scrape.yml": expectedScrapeConfig},
	}
}

func TestVMAgentConfigMap(t *testing.T) {
	got := resources.VMAgentConfigMap(vmagentCluster(), 3, 2)
	if diff := cmp.Diff(expectedVMAgentConfigMap(), got); diff != "" {
		t.Errorf("VMAgentConfigMap() mismatch (-want +got):\n%s", diff)
	}
}

// TestVMAgentConfigMapDefaults pins what a block naming only the endpoint
// gets: the chart's interval, no external labels, and the targets following
// the counts the controller applied rather than any the spec declares.
func TestVMAgentConfigMapDefaults(t *testing.T) {
	cluster := minimalCluster()
	cluster.Spec.DataInstances = ptr.To(int32(5))
	cluster.Spec.Monitoring = &memgraphcomv1alpha1.MonitoringSpec{
		VMAgentRemote: &memgraphcomv1alpha1.VMAgentRemoteSpec{
			RemoteWrite: memgraphcomv1alpha1.RemoteWriteSpec{URL: remoteWriteURL},
		},
	}

	want := expectedVMAgentConfigMap()
	want.Data["scrape.yml"] = `global:
  scrape_interval: 15s
scrape_configs:
- job_name: memgraph
  metrics_path: /metrics
  static_configs:
  - targets:
    - example-coordinator-0.example-coordinator.memgraph-test.svc.cluster.local:9091
    - example-coordinator-1.example-coordinator.memgraph-test.svc.cluster.local:9091
    - example-coordinator-2.example-coordinator.memgraph-test.svc.cluster.local:9091
    - example-data-0.example-data.memgraph-test.svc.cluster.local:9091
`

	got := resources.VMAgentConfigMap(cluster, 3, 1)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("VMAgentConfigMap() mismatch (-want +got):\n%s", diff)
	}
}

// TestVMAgentConfigMapBoltTLS pins what spec.tls.bolt does to the scrape:
// Memgraph serves metrics from the Bolt server context, so the job goes over
// https the moment the block is set, unverified for the reason the
// ServiceMonitor's endpoint is.
func TestVMAgentConfigMapBoltTLS(t *testing.T) {
	cluster := vmagentCluster()
	cluster.Spec.TLS = &memgraphcomv1alpha1.TLSSpec{
		Bolt: &memgraphcomv1alpha1.BoltTLSSpec{SecretName: boltTLSSecretName},
	}

	want := expectedVMAgentConfigMap()
	want.Data["scrape.yml"] = `global:
  external_labels:
    cluster: production
    team: platform
  scrape_interval: 30s
scrape_configs:
- job_name: memgraph
  metrics_path: /metrics
  scheme: https
  static_configs:
  - targets:
    - example-coordinator-0.example-coordinator.memgraph-test.svc.cluster.local:9091
    - example-coordinator-1.example-coordinator.memgraph-test.svc.cluster.local:9091
    - example-coordinator-2.example-coordinator.memgraph-test.svc.cluster.local:9091
    - example-data-0.example-data.memgraph-test.svc.cluster.local:9091
    - example-data-1.example-data.memgraph-test.svc.cluster.local:9091
  tls_config:
    insecure_skip_verify: true
`

	got := resources.VMAgentConfigMap(cluster, 3, 2)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("VMAgentConfigMap() mismatch (-want +got):\n%s", diff)
	}
}

// expectedVMAgentDeployment is the Deployment of vmagentCluster: one replica
// of the named image under the restricted security context and a fixed
// non-root identity, the scrape config mounted read-only and re-read on an
// interval, a buffer on an emptyDir, the basic-auth username taken from the
// Secret through the environment and the password mounted as the file vmagent
// reads it from.
func expectedVMAgentDeployment() *appsv1.Deployment {
	labels := expectedVMAgentLabels()
	uid := ptr.To(int64(65534))
	return &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{APIVersion: appsAPIVersion, Kind: deploymentKind},
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmagentName,
			Namespace: testNamespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: expectedSelectorLabels(vmagentComponent)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: ptr.To(false),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsUser:      uid,
						RunAsGroup:     uid,
						FSGroup:        uid,
						RunAsNonRoot:   ptr.To(true),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{{
						Name:            vmagentComponent,
						Image:           vmagentImage,
						ImagePullPolicy: corev1.PullAlways,
						Args: []string{
							"-promscrape.config=/etc/vmagent/config/scrape.yml",
							"-promscrape.configCheckInterval=1m",
							"-remoteWrite.url=" + remoteWriteURL,
							"-remoteWrite.tmpDataPath=/tmpData",
							"-httpListenAddr=:8429",
							"-remoteWrite.basicAuth.username=$(VMAGENT_BASIC_AUTH_USERNAME)",
							"-remoteWrite.basicAuth.passwordFile=/etc/vmagent/basic-auth/password",
						},
						Env: []corev1.EnvVar{{
							Name: "VMAGENT_BASIC_AUTH_USERNAME",
							ValueFrom: &corev1.EnvVarSource{
								SecretKeyRef: &corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{Name: basicAuthSecret},
									Key:                  "username",
								},
							},
						}},
						Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8429}},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{Path: "/health", Port: intstr.FromString("http")},
							},
						},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")},
						},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "config", MountPath: "/etc/vmagent/config", ReadOnly: true},
							{Name: vmagentDataVolume, MountPath: "/tmpData"},
							{Name: "basic-auth", MountPath: "/etc/vmagent/basic-auth", ReadOnly: true},
						},
						SecurityContext: expectedContainerSecurityContext(),
					}},
					Volumes: []corev1.Volume{
						{
							Name: "config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: vmagentConfigName},
								},
							},
						},
						{Name: vmagentDataVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
						{
							Name: "basic-auth",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: basicAuthSecret,
									Items:      []corev1.KeyToPath{{Key: "password", Path: "password"}},
								},
							},
						},
					},
				},
			},
		},
	}
}

func TestVMAgentDeployment(t *testing.T) {
	got := resources.VMAgentDeployment(vmagentCluster())
	if diff := cmp.Diff(expectedVMAgentDeployment(), got); diff != "" {
		t.Errorf("VMAgentDeployment() mismatch (-want +got):\n%s", diff)
	}
}

// TestVMAgentDeploymentDefaults pins what a block naming only the endpoint
// gets: the chart's image at the tag the operator pins, no credentials
// mounted or named, no resources.
func TestVMAgentDeploymentDefaults(t *testing.T) {
	cluster := minimalCluster()
	cluster.Spec.Monitoring = &memgraphcomv1alpha1.MonitoringSpec{
		VMAgentRemote: &memgraphcomv1alpha1.VMAgentRemoteSpec{
			RemoteWrite: memgraphcomv1alpha1.RemoteWriteSpec{URL: remoteWriteURL},
		},
	}

	want := expectedVMAgentDeployment()
	container := &want.Spec.Template.Spec.Containers[0]
	container.Image = "docker.io/victoriametrics/vmagent:v1.139.0"
	container.ImagePullPolicy = corev1.PullIfNotPresent
	container.Args = container.Args[:5]
	container.Env = nil
	container.Resources = corev1.ResourceRequirements{}
	container.VolumeMounts = container.VolumeMounts[:2]
	want.Spec.Template.Spec.Volumes = want.Spec.Template.Spec.Volumes[:2]

	got := resources.VMAgentDeployment(cluster)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("VMAgentDeployment() mismatch (-want +got):\n%s", diff)
	}
}

func TestUsesVMAgent(t *testing.T) {
	if resources.UsesVMAgent(minimalCluster()) {
		t.Error("UsesVMAgent() = true for a cluster without the block")
	}
	if resources.UsesVMAgent(monitoredCluster()) {
		t.Error("UsesVMAgent() = true for a monitoring block without vmagentRemote")
	}
	if !resources.UsesVMAgent(vmagentCluster()) {
		t.Error("UsesVMAgent() = false for a cluster with the block")
	}
}

// TestClusterNameOfMemgraphPod pins that the pods of both StatefulSets map to
// their cluster and the vmagent's, which carries the same identity labels
// under a role of its own, maps to none.
func TestClusterNameOfMemgraphPod(t *testing.T) {
	for name, tc := range map[string]struct {
		labels map[string]string
		want   string
	}{
		"coordinator": {coordinatorStatefulSet(minimalCluster()).Spec.Template.Labels, clusterName},
		"data":        {dataStatefulSet(minimalCluster()).Spec.Template.Labels, clusterName},
		"vmagent":     {resources.VMAgentDeployment(vmagentCluster()).Spec.Template.Labels, ""},
		"unlabelled":  {nil, ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := resources.ClusterNameOfMemgraphPod(tc.labels); got != tc.want {
				t.Errorf("ClusterNameOfMemgraphPod() = %q, want %q", got, tc.want)
			}
		})
	}
}
