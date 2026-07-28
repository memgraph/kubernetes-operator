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
	// coreDumpsMountPath is not configurable: it is only ever written to by the
	// kernel and read by the uploader, both of which the operator points at it,
	// so a knob would only create ways for the two to disagree.
	coreDumpsMountPath = "/var/core/memgraph"

	// Volume names double as the StatefulSet volumeClaimTemplate names, so the
	// provisioned claims are <volume>-<pod>, e.g. lib-storage-example-data-0.
	libVolumeName       = "lib-storage"
	logVolumeName       = "log-storage"
	coreDumpsVolumeName = "core-dumps"
	tmpVolumeName       = "tmp"

	// Container names of the two optional containers core dumps bring along.
	corePatternContainerName = "init-core-pattern"
	uploaderContainerName    = "core-dumps-uploader"
)

// CoordinatorStatefulSet builds the single StatefulSet running all
// coordinator instances. Per-pod identity (coordinator ID, advertised FQDN)
// is derived from the pod ordinal at startup, so the pod template stays
// uniform across replicas.
//
// The replica count is an argument rather than read off the spec because the
// count to apply is a decision about the live cluster, not about the spec: it
// is the declared count while the cluster grows, and the current count while a
// lowered one is still being retired. Keeping that decision in the controller
// keeps this builder pure. DeclaredCoordinators is the count for a cluster that
// is not shrinking.
func CoordinatorStatefulSet(
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	replicas int32,
) *appsv1.StatefulSet {
	spec := normalize(cluster.Spec)
	role := spec.coordinatorRole

	container := memgraphContainer(spec, role)
	// The coordinator ID and advertised FQDN depend on the pod ordinal, which
	// only the pod itself knows; a shell wrapper derives them from the pod
	// name so all replicas share one template.
	container.Command = []string{"/bin/sh", "-ec", coordinatorStartScript(cluster, spec, role)}
	container.Env = append([]corev1.EnvVar{{
		Name: memgraphcomv1alpha1.EnvPodName,
		ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
		},
	}}, container.Env...)
	container.Ports = []corev1.ContainerPort{
		{Name: boltPortName, ContainerPort: spec.ports.bolt},
		{Name: managementPortName, ContainerPort: spec.ports.management},
		{Name: coordinatorPortName, ContainerPort: spec.ports.coordinator},
	}
	// Coordinators are probed on their Raft port: it is the one they serve
	// even before the Raft cluster has been formed.
	container.StartupProbe = tcpProbe(spec.ports.coordinator, role.startupProbe)
	container.ReadinessProbe = tcpProbe(spec.ports.coordinator, role.readinessProbe)
	container.LivenessProbe = tcpProbe(spec.ports.coordinator, role.livenessProbe)

	return statefulSet(cluster, coordinatorComponent, CoordinatorName(cluster), spec, role, replicas, container)
}

// DataStatefulSet builds the single StatefulSet running all data instances. The
// replica count is an argument for the reason CoordinatorStatefulSet documents;
// DeclaredDataInstances is the count for a cluster that is not shrinking.
func DataStatefulSet(cluster *memgraphcomv1alpha1.MemgraphCluster, replicas int32) *appsv1.StatefulSet {
	spec := normalize(cluster.Spec)
	role := spec.dataRole

	container := memgraphContainer(spec, role)
	container.Args = append(commonArgs(spec, role), role.extraArgs...)
	container.Ports = []corev1.ContainerPort{
		{Name: boltPortName, ContainerPort: spec.ports.bolt},
		{Name: managementPortName, ContainerPort: spec.ports.management},
		{Name: replicationPortName, ContainerPort: spec.ports.replication},
	}
	container.StartupProbe = tcpProbe(spec.ports.bolt, role.startupProbe)
	container.ReadinessProbe = tcpProbe(spec.ports.bolt, role.readinessProbe)
	container.LivenessProbe = tcpProbe(spec.ports.bolt, role.livenessProbe)

	return statefulSet(cluster, dataComponent, DataName(cluster), spec, role, replicas, container)
}

// coordinatorStartScript derives the coordinator's identity from its pod
// ordinal (the numeric suffix of the pod name): ordinal N becomes coordinator
// ID N+1 (Memgraph treats coordinator ID 0 as unset and refuses to start, so
// IDs stay 1-based) advertised at the pod's stable DNS name within the
// headless Service.
func coordinatorStartScript(
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	spec normalizedSpec,
	role normalizedRole,
) string {
	fqdnSuffix := podFQDNSuffix(cluster, CoordinatorName(cluster), spec)
	args := append(commonArgs(spec, role), role.extraArgs...)
	return fmt.Sprintf(`ordinal="${POD_NAME##*-}"
exec %s \
  --coordinator-id="$((ordinal + 1))" \
  --coordinator-hostname="${POD_NAME}.%s" \
  --coordinator-port=%d \
  %s`, memgraphBinary, fqdnSuffix, spec.ports.coordinator, shellJoin(args))
}

// commonArgs are the Memgraph flags shared by both roles, mirroring the HA
// chart's auto-appended and default logging arguments. A role's extra args are
// appended after these, and Memgraph takes the last occurrence of a repeated
// flag, so a user-supplied flag wins.
//
// A role that opted out of log storage gets --log-file with an empty value,
// which is what turns file logging off. Leaving the flag out would not: the
// image ships /etc/memgraph/memgraph.conf with log_file set to the path below,
// Memgraph parses that file before the command line, and failing to open the
// resulting path is fatal — so an unmounted log directory on a read-only root
// filesystem would crash-loop the pod. --also-log-to-stderr keeps the logs in
// `kubectl logs` either way.
func commonArgs(spec normalizedSpec, role normalizedRole) []string {
	logDestination := logFile
	if !role.storage.createLogClaim {
		logDestination = ""
	}
	return []string{
		fmt.Sprintf("--bolt-port=%d", spec.ports.bolt),
		fmt.Sprintf("--management-port=%d", spec.ports.management),
		"--data-directory=" + dataDirectory,
		"--log-level=TRACE",
		"--also-log-to-stderr",
		"--log-file=" + logDestination,
		"--log-retention-days=35",
	}
}

func shellJoin(args []string) string {
	return strings.Join(args, " \\\n  ")
}

// memgraphContainer builds the parts of the Memgraph container shared by both
// roles: image, license env wiring, the role's extra environment and resources,
// storage mounts, and the restricted security context.
func memgraphContainer(spec normalizedSpec, role normalizedRole) corev1.Container {
	return corev1.Container{
		Name:            "memgraph",
		Image:           spec.image,
		ImagePullPolicy: spec.pullPolicy,
		Resources:       role.resources,
		// The license variables come first and the role's extra environment
		// last; admission rejects an extra variable that repeats one of the
		// names the operator owns, so the two can never collide.
		Env: append([]corev1.EnvVar{
			{
				Name: memgraphcomv1alpha1.EnvLicense,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: spec.secretName},
						Key:                  spec.licenseKey,
					},
				},
			},
			{
				Name: memgraphcomv1alpha1.EnvOrganization,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: spec.secretName},
						Key:                  spec.organizationKey,
					},
				},
			},
		}, role.env...),
		VolumeMounts:    volumeMounts(role),
		SecurityContext: restrictedSecurityContext(),
	}
}

// restrictedSecurityContext is what every container the operator builds runs
// under: no privilege escalation, no capabilities, a read-only root filesystem
// and the default seccomp profile. The core pattern init container is the one
// exception — it cannot do its job under this.
func restrictedSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		ReadOnlyRootFilesystem:   ptr.To(true),
		RunAsNonRoot:             ptr.To(true),
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

// corePatternInitContainer points the node's kernel at the role's core dumps
// directory. It runs the cluster's own Memgraph image — already pulled on the
// node, so core dumps need no second image to be configured or mirrored — and
// is the only container the operator builds that breaks the restricted security
// posture: /proc/sys is mounted read-only in an unprivileged container, so
// writing core_pattern needs privileged plus root. Nothing else about the pod
// is relaxed, and a namespace that forbids privileged pods can turn this off
// and have the platform manage core_pattern on the node instead.
func corePatternInitContainer(spec normalizedSpec) corev1.Container {
	// %e.%p.%t.%s expand to the crashing executable, its pid, the time and the
	// signal.
	pattern := coreDumpsMountPath + "/core.%e.%p.%t.%s"
	return corev1.Container{
		Name:            corePatternContainerName,
		Image:           spec.image,
		ImagePullPolicy: spec.pullPolicy,
		// tee rather than a plain redirect so the pattern that was set is
		// visible in the init container's logs.
		Command: []string{"/bin/sh", "-ec", fmt.Sprintf("echo '%s' | tee /proc/sys/kernel/core_pattern", pattern)},
		SecurityContext: &corev1.SecurityContext{
			Privileged: ptr.To(true),
			// Kubernetes rejects a privileged container that also forbids
			// privilege escalation, so this one cannot be false.
			AllowPrivilegeEscalation: ptr.To(true),
			ReadOnlyRootFilesystem:   ptr.To(true),
			RunAsUser:                ptr.To(int64(0)),
			RunAsNonRoot:             ptr.To(false),
			SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
	}
}

// uploaderSidecar builds the optional container that ships collected dumps off
// the volume. The operator owns the wiring a sidecar must not get wrong: the
// dumps are mounted read-only (an uploader has no business writing them), the
// mount path arrives as CORE_DUMPS_DIR so the path is stated once, and the
// pod's scratch volume is mounted at /tmp so the sidecar has somewhere to write
// without a writable root filesystem.
func uploaderSidecar(coreDumps normalizedCoreDumps) corev1.Container {
	uploader := coreDumps.uploader
	pullPolicy := uploader.PullPolicy
	if pullPolicy == "" {
		pullPolicy = memgraphcomv1alpha1.DefaultImagePullPolicy
	}
	env := append([]corev1.EnvVar{{
		Name: memgraphcomv1alpha1.EnvCoreDumpsDir, Value: coreDumpsMountPath,
	}}, normalizeEnv(uploader.Env)...)

	envFrom := make([]corev1.EnvFromSource, 0, len(uploader.EnvFromSecrets))
	for _, secret := range uploader.EnvFromSecrets {
		envFrom = append(envFrom, corev1.EnvFromSource{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: secret},
			},
		})
	}

	return corev1.Container{
		Name:            uploaderContainerName,
		Image:           uploader.Image,
		ImagePullPolicy: pullPolicy,
		Command:         uploader.Command,
		Args:            uploader.Args,
		Env:             env,
		EnvFrom:         envFrom,
		Resources:       uploader.Resources,
		VolumeMounts: []corev1.VolumeMount{
			{Name: coreDumpsVolumeName, MountPath: coreDumpsMountPath, ReadOnly: true},
			{Name: tmpVolumeName, MountPath: tmpMountPath},
		},
		SecurityContext: restrictedSecurityContext(),
	}
}

// volumeMounts are the Memgraph container's mounts: lib storage, the scratch
// directory the read-only root filesystem needs, log storage unless the role
// opted out of it, the core dumps directory when the role collects dumps, and
// last the role's own extra mounts.
func volumeMounts(role normalizedRole) []corev1.VolumeMount {
	mounts := []corev1.VolumeMount{{Name: libVolumeName, MountPath: libMountPath}}
	if role.storage.createLogClaim {
		mounts = append(mounts, corev1.VolumeMount{Name: logVolumeName, MountPath: logMountPath})
	}
	mounts = append(mounts, corev1.VolumeMount{Name: tmpVolumeName, MountPath: tmpMountPath})
	if role.coreDumps.enabled {
		mounts = append(mounts,
			corev1.VolumeMount{Name: coreDumpsVolumeName, MountPath: coreDumpsMountPath})
	}
	return append(mounts, role.extraMounts...)
}

// podVolumes is the scratch directory the read-only root filesystem needs plus
// the role's extra volumes. Everything persistent comes from
// volumeClaimTemplates instead.
func podVolumes(role normalizedRole) []corev1.Volume {
	volumes := make([]corev1.Volume, 0, 1+len(role.extraVolumes))
	volumes = append(volumes, corev1.Volume{
		Name: tmpVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	})
	return append(volumes, role.extraVolumes...)
}

// volumeClaimTemplates are the per-pod claims of the role: lib storage always,
// log storage unless the role opted out of it, and core dumps when enabled. All
// three follow the cluster's single retention policy.
func volumeClaimTemplates(role normalizedRole) []corev1.PersistentVolumeClaim {
	storage := role.storage
	claims := []corev1.PersistentVolumeClaim{
		volumeClaimTemplate(libVolumeName, storage.libSize, storage.libAccessMode, storage.libClass),
	}
	if storage.createLogClaim {
		claims = append(claims,
			volumeClaimTemplate(logVolumeName, storage.logSize, storage.logAccessMode, storage.logClass))
	}
	if role.coreDumps.enabled {
		// Dumps are written by one node's kernel into one pod's directory, so
		// the access mode is not a knob: ReadWriteOnce is the only one that
		// describes it.
		claims = append(claims, volumeClaimTemplate(coreDumpsVolumeName,
			role.coreDumps.size, corev1.ReadWriteOnce, role.coreDumps.class))
	}
	return claims
}

// podContainers is the Memgraph container plus the uploader sidecar when the
// role has one. Memgraph stays first, so `kubectl logs` without -c keeps
// showing the database.
func podContainers(memgraph corev1.Container, role normalizedRole) []corev1.Container {
	containers := []corev1.Container{memgraph}
	if role.coreDumps.enabled && role.coreDumps.uploader != nil {
		containers = append(containers, uploaderSidecar(role.coreDumps))
	}
	return containers
}

// podInitContainers is empty unless the role asked the operator to configure
// the node's core pattern.
func podInitContainers(spec normalizedSpec, role normalizedRole) []corev1.Container {
	if !role.coreDumps.enabled || !role.coreDumps.configurePattern {
		return nil
	}
	return []corev1.Container{corePatternInitContainer(spec)}
}

func statefulSet(
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	component, name string,
	spec normalizedSpec,
	role normalizedRole,
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
			Labels:    labels(cluster, component, role.statefulSetLabels),
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:            ptr.To(replicas),
			ServiceName:         name,
			PodManagementPolicy: appsv1.ParallelPodManagement,
			Selector:            &metav1.LabelSelector{MatchLabels: selectorLabels(cluster, component)},
			// The StatefulSet controller is the only thing that ever deletes
			// this cluster's storage; the operator owns no finalizer and runs
			// no cleanup of its own. Both halves of the policy follow the one
			// retention knob: whether a claim is orphaned by deleting the
			// cluster or by scaling a role down, the user asked the same
			// question — keep this cluster's data, or do not.
			PersistentVolumeClaimRetentionPolicy: &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{
				WhenDeleted: retentionType(spec.retentionPolicy),
				WhenScaled:  retentionType(spec.retentionPolicy),
			},
			VolumeClaimTemplates: volumeClaimTemplates(role),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels(cluster, component, role.podLabels),
				},
				Spec: corev1.PodSpec{
					InitContainers: podInitContainers(spec, role),
					Containers:     podContainers(container, role),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsUser:      ptr.To(memgraphUserID),
						RunAsGroup:     ptr.To(memgraphGroupID),
						FSGroup:        ptr.To(memgraphGroupID),
						RunAsNonRoot:   ptr.To(true),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Volumes: podVolumes(role),
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

// tcpProbe builds one probe. The handler is always a TCP-socket check against
// the role's own port — the probe type is deliberately not configurable — so
// only the timings come from the spec.
func tcpProbe(port int32, timings normalizedProbe) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(port)},
		},
		FailureThreshold: timings.failureThreshold,
		TimeoutSeconds:   timings.timeoutSeconds,
		PeriodSeconds:    timings.periodSeconds,
	}
}
