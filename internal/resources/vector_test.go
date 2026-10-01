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
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
	"github.com/memgraph/kubernetes-operator/internal/resources"
)

const (
	vectorComponent  = "vector"
	vectorConfigName = clusterName + "-vector-config"
	vectorImage      = "registry.example.com/timberio/vector:0.50.0-alpine"
	logsEndpoint     = "http://victoria-logs.monitoring.svc.cluster.local:9428/insert"
	logsAuthSecret   = "logs-basic-auth"

	configMapKind   = "ConfigMap"
	debuggerName    = "my-debugger"
	podNameEnv      = "POD_NAME"
	podNameField    = "metadata.name"
	basicAuthPasswd = "password"
)

// vectorCluster asks for the Vector sidecar with every knob set, so the golden
// tests pin each one.
func vectorCluster() *memgraphcomv1alpha1.MemgraphCluster {
	cluster := minimalCluster()
	cluster.Spec.Monitoring = &memgraphcomv1alpha1.MonitoringSpec{
		VectorRemote: &memgraphcomv1alpha1.VectorRemoteSpec{
			Image: memgraphcomv1alpha1.VectorImageSpec{
				Repository: "registry.example.com/timberio/vector",
				Tag:        "0.50.0-alpine",
				PullPolicy: corev1.PullAlways,
			},
			LogsEndpoint: logsEndpoint,
			Auth:         &memgraphcomv1alpha1.BasicAuthSecretSpec{SecretName: logsAuthSecret},
			ExtraLabels:  map[string]string{"cluster_id": "production", "role": "overridden"},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")},
			},
		},
	}
	return cluster
}

// expectedVectorRemap is the VRL program every configuration carries, as the
// YAML block scalar renders it.
const expectedVectorRemap = `    source: |
      level = "unknown"
      parsed, err = parse_json(.message)
      if err == null && is_object(parsed) {
        level = downcase(to_string(parsed.level) ?? "unknown")
        .message = to_string(parsed.message) ?? .message
      }
      if level == "warning" {
        level = "warn"
      } else if level == "critical" {
        level = "fatal"
      }
      .level = level
`

// expectedVectorConfig is the configuration of vectorCluster: the websocket
// inside the pod, the remap, and a Loki sink at the endpoint with the chart's
// labels, the pod's own from the environment, the extra label, the operator's
// role label winning the collision, and the credentials from the environment.
const expectedVectorConfig = `data_dir: /vector-data
sinks:
  loki:
    auth:
      password: ${LOGS_PASSWORD}
      strategy: basic
      user: ${LOGS_USERNAME}
    encoding:
      codec: text
    endpoint: http://victoria-logs.monitoring.svc.cluster.local:9428/insert
    healthcheck:
      enabled: false
    inputs:
    - logs
    labels:
      app: memgraph
      cluster_id: production
      job: memgraph
      level: '{{ level }}'
      namespace: ${POD_NAMESPACE}
      pod: ${POD_NAME}
      role: ${ROLE}
    remove_label_fields: true
    type: loki
sources:
  memgraph:
    type: websocket
    uri: ws://127.0.0.1:7444
transforms:
  logs:
    inputs:
    - memgraph
` + expectedVectorRemap + `    type: remap
`

func expectedVectorConfigMap() *corev1.ConfigMap {
	labels := expectedLabels(vectorComponent)
	labels[monitoringLabel] = monitoringMarker
	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: configMapKind},
		ObjectMeta: metav1.ObjectMeta{
			Name:      vectorConfigName,
			Namespace: testNamespace,
			Labels:    labels,
		},
		Data: map[string]string{"vector.yaml": expectedVectorConfig},
	}
}

func TestVectorConfigMap(t *testing.T) {
	got := resources.VectorConfigMap(vectorCluster())
	if diff := cmp.Diff(expectedVectorConfigMap(), got); diff != "" {
		t.Errorf("VectorConfigMap() mismatch (-want +got):\n%s", diff)
	}
}

// TestVectorConfigMapUnauthenticated pins what a block naming no Secret gets:
// a sink with no auth block, and nothing else different.
func TestVectorConfigMapUnauthenticated(t *testing.T) {
	cluster := vectorCluster()
	cluster.Spec.Monitoring.VectorRemote.Auth = nil

	want := expectedVectorConfigMap()
	want.Data["vector.yaml"] = strings.Replace(expectedVectorConfig, `    auth:
      password: ${LOGS_PASSWORD}
      strategy: basic
      user: ${LOGS_USERNAME}
`, "", 1)

	got := resources.VectorConfigMap(cluster)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("VectorConfigMap() mismatch (-want +got):\n%s", diff)
	}
}

// TestVectorConfigMapBoltTLS pins what spec.tls.bolt does to the source: the
// websocket shares the Bolt TLS context, so the sidecar dials wss, told to
// do TLS explicitly because the scheme alone does not make Vector do it, and
// does not verify a certificate that names no loopback address.
func TestVectorConfigMapBoltTLS(t *testing.T) {
	cluster := vectorCluster()
	cluster.Spec.TLS = &memgraphcomv1alpha1.TLSSpec{
		Bolt: &memgraphcomv1alpha1.BoltTLSSpec{SecretName: boltTLSSecretName},
	}

	want := expectedVectorConfigMap()
	want.Data["vector.yaml"] = strings.Replace(expectedVectorConfig, `    type: websocket
    uri: ws://127.0.0.1:7444
`, `    tls:
      enabled: true
      verify_certificate: false
      verify_hostname: false
    type: websocket
    uri: wss://127.0.0.1:7444
`, 1)

	got := resources.VectorConfigMap(cluster)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("VectorConfigMap() mismatch (-want +got):\n%s", diff)
	}
}

// expectedVectorSidecar is the sidecar of vectorCluster on the given role:
// Vector on the mounted configuration, told its role, pod and namespace and
// handed the credentials through the environment, its state on an emptyDir,
// under the restricted security context.
func expectedVectorSidecar(role string) corev1.Container {
	return corev1.Container{
		Name:            vectorComponent,
		Image:           vectorImage,
		ImagePullPolicy: corev1.PullAlways,
		Args:            []string{"--config", "/etc/vector/config/vector.yaml"},
		Env: []corev1.EnvVar{
			{Name: "ROLE", Value: role},
			{Name: podNameEnv, ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: podNameField},
			}},
			{Name: "POD_NAMESPACE", ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
			}},
			{Name: "LOGS_USERNAME", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: logsAuthSecret},
					Key:                  "username",
				},
			}},
			{Name: "LOGS_PASSWORD", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: logsAuthSecret},
					Key:                  basicAuthPasswd,
				},
			}},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "vector-config", MountPath: "/etc/vector/config", ReadOnly: true},
			{Name: "vector-data", MountPath: "/vector-data"},
		},
		SecurityContext: expectedContainerSecurityContext(),
	}
}

func expectedVectorVolumes() []corev1.Volume {
	return []corev1.Volume{
		{
			Name: "vector-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: vectorConfigName},
				},
			},
		},
		{Name: "vector-data", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
}

// TestStatefulSetVectorSidecar pins the sidecar landing on both roles, after
// Memgraph and before any user container, each told its own role, with the two
// volumes it mounts on the pod.
func TestStatefulSetVectorSidecar(t *testing.T) {
	cluster := vectorCluster()
	cluster.Spec.UserContainers.Data = []corev1.Container{{Name: debuggerName, Image: "busybox"}}

	for role, podSpec := range map[string]corev1.PodSpec{
		coordinatorComponent: coordinatorStatefulSet(cluster).Spec.Template.Spec,
		dataComponent:        dataStatefulSet(cluster).Spec.Template.Spec,
	} {
		names := make([]string, 0, len(podSpec.Containers))
		for _, container := range podSpec.Containers {
			names = append(names, container.Name)
		}
		wantNames := []string{memgraphName, vectorComponent}
		if role == dataComponent {
			wantNames = append(wantNames, debuggerName)
		}
		if diff := cmp.Diff(wantNames, names); diff != "" {
			t.Errorf("%s containers mismatch (-want +got):\n%s", role, diff)
		}
		if diff := cmp.Diff(expectedVectorSidecar(role), podSpec.Containers[1]); diff != "" {
			t.Errorf("%s vector sidecar mismatch (-want +got):\n%s", role, diff)
		}
		for _, want := range expectedVectorVolumes() {
			found := false
			for _, volume := range podSpec.Volumes {
				if volume.Name == want.Name {
					found = true
					if diff := cmp.Diff(want, volume); diff != "" {
						t.Errorf("%s volume %s mismatch (-want +got):\n%s", role, want.Name, diff)
					}
				}
			}
			if !found {
				t.Errorf("%s pod has no %s volume", role, want.Name)
			}
		}
	}
}

// TestStatefulSetVectorSidecarDefaults pins what a block naming only the
// endpoint gets: the chart's image at the tag the operator pins, no
// credentials in the environment, no resources.
func TestStatefulSetVectorSidecarDefaults(t *testing.T) {
	cluster := minimalCluster()
	cluster.Spec.Monitoring = &memgraphcomv1alpha1.MonitoringSpec{
		VectorRemote: &memgraphcomv1alpha1.VectorRemoteSpec{LogsEndpoint: logsEndpoint},
	}

	want := expectedVectorSidecar(dataComponent)
	want.Image = "docker.io/timberio/vector:0.49.0-debian"
	want.ImagePullPolicy = corev1.PullIfNotPresent
	want.Env = want.Env[:3]
	want.Resources = corev1.ResourceRequirements{}

	containers := dataStatefulSet(cluster).Spec.Template.Spec.Containers
	if len(containers) != 2 {
		t.Fatalf("containers = %d, want Memgraph plus the sidecar", len(containers))
	}
	if diff := cmp.Diff(want, containers[1]); diff != "" {
		t.Errorf("vector sidecar mismatch (-want +got):\n%s", diff)
	}
}

// TestStatefulSetWithoutVector: a cluster that did not ask for the sidecar
// carries neither it nor its volumes.
func TestStatefulSetWithoutVector(t *testing.T) {
	podSpec := dataStatefulSet(monitoredCluster()).Spec.Template.Spec
	if len(podSpec.Containers) != 1 {
		t.Errorf("containers = %d, want only Memgraph's", len(podSpec.Containers))
	}
	for _, volume := range podSpec.Volumes {
		if strings.HasPrefix(volume.Name, "vector-") {
			t.Errorf("unexpected volume %s on a cluster without the block", volume.Name)
		}
	}
}

func TestUsesVector(t *testing.T) {
	if resources.UsesVector(minimalCluster()) {
		t.Error("UsesVector() = true for a cluster without the block")
	}
	if resources.UsesVector(monitoredCluster()) {
		t.Error("UsesVector() = true for a monitoring block without vectorRemote")
	}
	if !resources.UsesVector(vectorCluster()) {
		t.Error("UsesVector() = false for a cluster with the block")
	}
}
