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
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
)

const (
	memgraphBinary = "/usr/lib/memgraph/memgraph"
	dataDirectory  = "/var/lib/memgraph/mg_data"
	logFile        = "/var/log/memgraph/memgraph.log"

	libMountPath = "/var/lib/memgraph"
	logMountPath = "/var/log/memgraph"
	tmpMountPath = "/tmp"
)

// CoordinatorStatefulSet builds the single StatefulSet running all
// coordinator instances. Per-pod identity (coordinator ID, advertised FQDN)
// is derived from the pod ordinal at startup, so the pod template stays
// uniform across replicas.
func CoordinatorStatefulSet(cluster *memgraphcomv1alpha1.MemgraphCluster) *appsv1.StatefulSet {
	spec := normalize(cluster.Spec)

	container := memgraphContainer(spec)
	// The coordinator ID and advertised FQDN depend on the pod ordinal, which
	// only the pod itself knows; a shell wrapper derives them from the pod
	// name so all replicas share one template.
	container.Command = []string{"/bin/sh", "-ec", coordinatorStartScript(cluster)}
	container.Env = append([]corev1.EnvVar{{
		Name: "POD_NAME",
		ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
		},
	}}, container.Env...)
	container.Ports = []corev1.ContainerPort{
		{Name: boltPortName, ContainerPort: BoltPort},
		{Name: managementPortName, ContainerPort: ManagementPort},
		{Name: coordinatorPortName, ContainerPort: CoordinatorPort},
	}
	container.StartupProbe = tcpProbe(CoordinatorPort, 20)
	container.ReadinessProbe = tcpProbe(CoordinatorPort, 20)
	container.LivenessProbe = tcpProbe(CoordinatorPort, 20)

	return statefulSet(cluster, coordinatorComponent, CoordinatorName(cluster), spec.coordinators, container)
}

// DataStatefulSet builds the single StatefulSet running all data instances.
func DataStatefulSet(cluster *memgraphcomv1alpha1.MemgraphCluster) *appsv1.StatefulSet {
	spec := normalize(cluster.Spec)

	container := memgraphContainer(spec)
	container.Args = dataArgs()
	container.Ports = []corev1.ContainerPort{
		{Name: boltPortName, ContainerPort: BoltPort},
		{Name: managementPortName, ContainerPort: ManagementPort},
		{Name: replicationPortName, ContainerPort: ReplicationPort},
	}
	// A generous startup budget so large snapshot restores are not killed
	// mid-load (mirrors the HA chart's default of 1440 * 5s = 2h).
	container.StartupProbe = tcpProbe(BoltPort, 1440)
	container.ReadinessProbe = tcpProbe(BoltPort, 20)
	container.LivenessProbe = tcpProbe(BoltPort, 20)

	return statefulSet(cluster, dataComponent, DataName(cluster), spec.dataInstances, container)
}

// coordinatorStartScript derives the coordinator's identity from its pod
// ordinal (the numeric suffix of the pod name): ordinal N becomes coordinator
// ID N+1 (Raft IDs start at 1) advertised at the pod's stable DNS name within
// the headless Service.
func coordinatorStartScript(cluster *memgraphcomv1alpha1.MemgraphCluster) string {
	fqdnSuffix := fmt.Sprintf("%s.%s.svc.%s", CoordinatorName(cluster), cluster.Namespace, clusterDomain)
	return fmt.Sprintf(`ordinal="${POD_NAME##*-}"
exec %s \
  --coordinator-id="$((ordinal + 1))" \
  --coordinator-hostname="${POD_NAME}.%s" \
  --coordinator-port=%d \
  %s`, memgraphBinary, fqdnSuffix, CoordinatorPort, shellJoin(commonArgs()))
}

func dataArgs() []string {
	return commonArgs()
}

// commonArgs are the Memgraph flags shared by both roles, mirroring the HA
// chart's auto-appended and default logging arguments.
func commonArgs() []string {
	return []string{
		fmt.Sprintf("--bolt-port=%d", BoltPort),
		fmt.Sprintf("--management-port=%d", ManagementPort),
		"--data-directory=" + dataDirectory,
		"--log-level=TRACE",
		"--also-log-to-stderr",
		"--log-file=" + logFile,
		"--log-retention-days=35",
	}
}

func shellJoin(args []string) string {
	return strings.Join(args, " \\\n  ")
}

// memgraphContainer builds the parts of the Memgraph container shared by both
// roles: image, license env wiring, storage mounts, and the restricted
// security context.
func memgraphContainer(spec normalizedSpec) corev1.Container {
	return corev1.Container{
		Name:            "memgraph",
		Image:           spec.image,
		ImagePullPolicy: spec.pullPolicy,
		Env: []corev1.EnvVar{
			{
				Name: "MEMGRAPH_ENTERPRISE_LICENSE",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: spec.secretName},
						Key:                  spec.licenseKey,
					},
				},
			},
			{
				Name: "MEMGRAPH_ORGANIZATION_NAME",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: spec.secretName},
						Key:                  spec.organizationKey,
					},
				},
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "lib-storage", MountPath: libMountPath},
			{Name: "log-storage", MountPath: logMountPath},
			{Name: "tmp", MountPath: tmpMountPath},
		},
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			ReadOnlyRootFilesystem:   ptr.To(true),
			RunAsNonRoot:             ptr.To(true),
			SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
	}
}

func statefulSet(
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	component, name string,
	replicas int32,
	container corev1.Container,
) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		// TypeMeta is set explicitly because the controller server-side
		// applies builder output, and apply patches must carry the GVK.
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "StatefulSet"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cluster.Namespace,
			Labels:    labels(cluster, component),
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:            ptr.To(replicas),
			ServiceName:         name,
			PodManagementPolicy: appsv1.ParallelPodManagement,
			Selector:            &metav1.LabelSelector{MatchLabels: selectorLabels(cluster, component)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels(cluster, component),
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{container},
					SecurityContext: &corev1.PodSecurityContext{
						RunAsUser:      ptr.To(memgraphUserID),
						RunAsGroup:     ptr.To(memgraphGroupID),
						FSGroup:        ptr.To(memgraphGroupID),
						RunAsNonRoot:   ptr.To(true),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					// Storage is ephemeral in this slice; PVC templates and
					// retention policy land with the storage-configuration
					// slice (specs/operator-mvp/issues/08).
					Volumes: []corev1.Volume{
						{Name: "lib-storage", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
						{Name: "log-storage", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
						{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
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
