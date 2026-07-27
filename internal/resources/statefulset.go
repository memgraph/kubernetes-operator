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
	"k8s.io/apimachinery/pkg/api/resource"
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

	// Volume names double as the StatefulSet volumeClaimTemplate names, so the
	// provisioned claims are <volume>-<pod>, e.g. lib-storage-example-data-0.
	libVolumeName = "lib-storage"
	logVolumeName = "log-storage"
	tmpVolumeName = "tmp"
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

	return statefulSet(cluster, coordinatorComponent, CoordinatorName(cluster), spec, spec.coordinators, container)
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

	return statefulSet(cluster, dataComponent, DataName(cluster), spec, spec.dataInstances, container)
}

// coordinatorStartScript derives the coordinator's identity from its pod
// ordinal (the numeric suffix of the pod name): ordinal N becomes coordinator
// ID N+1 (Memgraph treats coordinator ID 0 as unset and refuses to start, so
// IDs stay 1-based) advertised at the pod's stable DNS name within the
// headless Service.
func coordinatorStartScript(cluster *memgraphcomv1alpha1.MemgraphCluster) string {
	fqdnSuffix := podFQDNSuffix(cluster, CoordinatorName(cluster))
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
			{Name: libVolumeName, MountPath: libMountPath},
			{Name: logVolumeName, MountPath: logMountPath},
			{Name: tmpVolumeName, MountPath: tmpMountPath},
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
	spec normalizedSpec,
	replicas int32,
	container corev1.Container,
) *appsv1.StatefulSet {
	storage := spec.dataStorage
	if component == coordinatorComponent {
		storage = spec.coordinatorStorage
	}

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
			// The StatefulSet controller is the only thing that ever deletes
			// this cluster's storage; the operator owns no finalizer and runs
			// no cleanup of its own. whenScaled is always Retain because both
			// replica counts are immutable in v1alpha1 — nothing scales down,
			// so no claim is ever orphaned by scaling.
			PersistentVolumeClaimRetentionPolicy: &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{
				WhenDeleted: retentionType(spec.retentionPolicy),
				WhenScaled:  appsv1.RetainPersistentVolumeClaimRetentionPolicyType,
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				volumeClaimTemplate(libVolumeName, storage.libSize, storage.libAccessMode, storage.libClass),
				volumeClaimTemplate(logVolumeName, storage.logSize, storage.logAccessMode, storage.logClass),
			},
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
					// Lib and log storage come from the volumeClaimTemplates
					// above; only the scratch directory the read-only root
					// filesystem still needs is ephemeral.
					Volumes: []corev1.Volume{
						{Name: tmpVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
			},
		},
	}
}

// volumeClaimTemplate builds one StatefulSet volumeClaimTemplate. A nil class
// is left unset so the cluster's default StorageClass applies; the empty
// string is passed through as-is, which disables dynamic provisioning.
func volumeClaimTemplate(
	name string,
	size resource.Quantity,
	accessMode corev1.PersistentVolumeAccessMode,
	class *string,
) corev1.PersistentVolumeClaim {
	return corev1.PersistentVolumeClaim{
		// TypeMeta is set explicitly for the same reason the StatefulSet sets
		// it: the controller server-side applies the builder output.
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{accessMode},
			StorageClassName: class,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: size},
			},
		},
	}
}

// retentionType maps the spec's retention policy onto the StatefulSet's
// whenDeleted policy. The two vocabularies coincide, so the mapping is a
// rename rather than a decision.
func retentionType(policy memgraphcomv1alpha1.StorageRetentionPolicy) appsv1.PersistentVolumeClaimRetentionPolicyType {
	if policy == memgraphcomv1alpha1.RetentionPolicyDelete {
		return appsv1.DeletePersistentVolumeClaimRetentionPolicyType
	}
	return appsv1.RetainPersistentVolumeClaimRetentionPolicyType
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
