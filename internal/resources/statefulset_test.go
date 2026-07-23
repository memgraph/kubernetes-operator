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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
	"github.com/memgraph/kubernetes-operator/internal/resources"
)

// Shared fixture names for the golden tests in this package.
const (
	testNamespace = "memgraph-test"
	clusterName   = "example"

	coordinatorComponent = "coordinator"
	dataComponent        = "data"

	coordinatorName = clusterName + "-" + coordinatorComponent
	dataName        = clusterName + "-" + dataComponent

	memgraphName = "memgraph"

	boltPortName        = "bolt"
	managementPortName  = "management"
	replicationPortName = "replication"
)

// minimalCluster returns a MemgraphCluster as a client would minimally create
// it, deliberately without CRD schema defaults applied: builders must resolve
// defaults themselves on specs that never passed admission.
func minimalCluster() *memgraphcomv1alpha1.MemgraphCluster {
	return &memgraphcomv1alpha1.MemgraphCluster{
		ObjectMeta: metav1.ObjectMeta{Name: clusterName, Namespace: testNamespace},
	}
}

func specifiedCluster() *memgraphcomv1alpha1.MemgraphCluster {
	return &memgraphcomv1alpha1.MemgraphCluster{
		ObjectMeta: metav1.ObjectMeta{Name: clusterName, Namespace: testNamespace},
		Spec: memgraphcomv1alpha1.MemgraphClusterSpec{
			Coordinators:  ptr.To(int32(5)),
			DataInstances: ptr.To(int32(3)),
			Image: memgraphcomv1alpha1.ImageSpec{
				Repository: "registry.example.com/memgraph",
				Tag:        "3.13.0",
				PullPolicy: corev1.PullAlways,
			},
			Secrets: memgraphcomv1alpha1.SecretsSpec{
				Name:            "my-license",
				LicenseKey:      "license",
				OrganizationKey: "organization",
			},
		},
	}
}

func licenseEnv(secretName, licenseKey, organizationKey string) []corev1.EnvVar {
	return []corev1.EnvVar{
		{
			Name: "MEMGRAPH_ENTERPRISE_LICENSE",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
					Key:                  licenseKey,
				},
			},
		},
		{
			Name: "MEMGRAPH_ORGANIZATION_NAME",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
					Key:                  organizationKey,
				},
			},
		},
	}
}

func tcpProbe(port, failureThreshold int32) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(port)},
		},
		FailureThreshold: failureThreshold,
		TimeoutSeconds:   10,
		PeriodSeconds:    5,
	}
}

func expectedPodSecurityContext() *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{
		RunAsUser:      ptr.To(int64(101)),
		RunAsGroup:     ptr.To(int64(103)),
		FSGroup:        ptr.To(int64(103)),
		RunAsNonRoot:   ptr.To(true),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

func expectedContainerSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		ReadOnlyRootFilesystem:   ptr.To(true),
		RunAsNonRoot:             ptr.To(true),
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

func expectedVolumeMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{Name: "lib-storage", MountPath: "/var/lib/memgraph"},
		{Name: "log-storage", MountPath: "/var/log/memgraph"},
		{Name: "tmp", MountPath: "/tmp"},
	}
}

func expectedVolumes() []corev1.Volume {
	return []corev1.Volume{
		{Name: "lib-storage", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "log-storage", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
}

func expectedLabels(component string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       memgraphName,
		"app.kubernetes.io/instance":   clusterName,
		"app.kubernetes.io/component":  component,
		"app.kubernetes.io/managed-by": "memgraph-operator",
	}
}

func expectedSelectorLabels(component string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":      memgraphName,
		"app.kubernetes.io/instance":  clusterName,
		"app.kubernetes.io/component": component,
	}
}

const expectedCoordinatorScript = `ordinal="${POD_NAME##*-}"
exec /usr/lib/memgraph/memgraph \
  --coordinator-id="$((ordinal + 1))" \
  --coordinator-hostname="${POD_NAME}.example-coordinator.memgraph-test.svc.cluster.local" \
  --coordinator-port=12000 \
  --bolt-port=7687 \
  --management-port=10000 \
  --data-directory=/var/lib/memgraph/mg_data \
  --log-level=TRACE \
  --also-log-to-stderr \
  --log-file=/var/log/memgraph/memgraph.log \
  --log-retention-days=35`

func TestCoordinatorStatefulSetDefaults(t *testing.T) {
	want := &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "StatefulSet"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      coordinatorName,
			Namespace: testNamespace,
			Labels:    expectedLabels(coordinatorComponent),
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:            ptr.To(int32(3)),
			ServiceName:         coordinatorName,
			PodManagementPolicy: appsv1.ParallelPodManagement,
			Selector:            &metav1.LabelSelector{MatchLabels: expectedSelectorLabels(coordinatorComponent)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: expectedLabels(coordinatorComponent)},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:            memgraphName,
						Image:           "docker.io/memgraph/memgraph:3.12.0-relwithdebinfo",
						ImagePullPolicy: corev1.PullIfNotPresent,
						Command:         []string{"/bin/sh", "-ec", expectedCoordinatorScript},
						Env: append([]corev1.EnvVar{{
							Name: "POD_NAME",
							ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
							},
						}}, licenseEnv("memgraph-secrets", "MEMGRAPH_ENTERPRISE_LICENSE", "MEMGRAPH_ORGANIZATION_NAME")...),
						Ports: []corev1.ContainerPort{
							{Name: boltPortName, ContainerPort: 7687},
							{Name: managementPortName, ContainerPort: 10000},
							{Name: coordinatorComponent, ContainerPort: 12000},
						},
						StartupProbe:    tcpProbe(12000, 20),
						ReadinessProbe:  tcpProbe(12000, 20),
						LivenessProbe:   tcpProbe(12000, 20),
						VolumeMounts:    expectedVolumeMounts(),
						SecurityContext: expectedContainerSecurityContext(),
					}},
					SecurityContext: expectedPodSecurityContext(),
					Volumes:         expectedVolumes(),
				},
			},
		},
	}

	got := resources.CoordinatorStatefulSet(minimalCluster())
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("CoordinatorStatefulSet() mismatch (-want +got):\n%s", diff)
	}
}

func TestDataStatefulSetDefaults(t *testing.T) {
	want := &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "StatefulSet"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      dataName,
			Namespace: testNamespace,
			Labels:    expectedLabels(dataComponent),
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:            ptr.To(int32(2)),
			ServiceName:         dataName,
			PodManagementPolicy: appsv1.ParallelPodManagement,
			Selector:            &metav1.LabelSelector{MatchLabels: expectedSelectorLabels(dataComponent)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: expectedLabels(dataComponent)},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:            memgraphName,
						Image:           "docker.io/memgraph/memgraph:3.12.0-relwithdebinfo",
						ImagePullPolicy: corev1.PullIfNotPresent,
						Args: []string{
							"--bolt-port=7687",
							"--management-port=10000",
							"--data-directory=/var/lib/memgraph/mg_data",
							"--log-level=TRACE",
							"--also-log-to-stderr",
							"--log-file=/var/log/memgraph/memgraph.log",
							"--log-retention-days=35",
						},
						Env: licenseEnv("memgraph-secrets", "MEMGRAPH_ENTERPRISE_LICENSE", "MEMGRAPH_ORGANIZATION_NAME"),
						Ports: []corev1.ContainerPort{
							{Name: boltPortName, ContainerPort: 7687},
							{Name: managementPortName, ContainerPort: 10000},
							{Name: replicationPortName, ContainerPort: 20000},
						},
						StartupProbe:    tcpProbe(7687, 1440),
						ReadinessProbe:  tcpProbe(7687, 20),
						LivenessProbe:   tcpProbe(7687, 20),
						VolumeMounts:    expectedVolumeMounts(),
						SecurityContext: expectedContainerSecurityContext(),
					}},
					SecurityContext: expectedPodSecurityContext(),
					Volumes:         expectedVolumes(),
				},
			},
		},
	}

	got := resources.DataStatefulSet(minimalCluster())
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("DataStatefulSet() mismatch (-want +got):\n%s", diff)
	}
}

func TestStatefulSetSpecOverrides(t *testing.T) {
	cluster := specifiedCluster()

	tests := []struct {
		name     string
		sts      *appsv1.StatefulSet
		replicas int32
	}{
		{name: coordinatorComponent, sts: resources.CoordinatorStatefulSet(cluster), replicas: 5},
		{name: dataComponent, sts: resources.DataStatefulSet(cluster), replicas: 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := *tc.sts.Spec.Replicas; got != tc.replicas {
				t.Errorf("replicas = %d, want %d", got, tc.replicas)
			}
			container := tc.sts.Spec.Template.Spec.Containers[0]
			if container.Image != "registry.example.com/memgraph:3.13.0" {
				t.Errorf("image = %q, want %q", container.Image, "registry.example.com/memgraph:3.13.0")
			}
			if container.ImagePullPolicy != corev1.PullAlways {
				t.Errorf("pull policy = %q, want %q", container.ImagePullPolicy, corev1.PullAlways)
			}
			wantEnv := licenseEnv("my-license", "license", "organization")
			gotEnv := container.Env[len(container.Env)-2:]
			if diff := cmp.Diff(wantEnv, gotEnv); diff != "" {
				t.Errorf("license env mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
