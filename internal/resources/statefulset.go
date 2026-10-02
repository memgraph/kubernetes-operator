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

	// listenAddress is where every instance's Bolt server and monitoring
	// websocket listen: every interface, so the pod IP the probes, the cluster
	// and the Services reach it at is among them.
	listenAddress = "0.0.0.0"

	// Volume names double as the StatefulSet volumeClaimTemplate names, so the
	// provisioned claims are <volume>-<pod>, e.g. lib-storage-example-data-0.
	libVolumeName       = "lib-storage"
	logVolumeName       = "log-storage"
	coreDumpsVolumeName = "core-dumps"
	tmpVolumeName       = "tmp"

	// boltTLSVolumeName and boltTLSMountPath are where the Bolt certificate
	// Secret lands in every pod of both roles, mirroring the HA chart's mount.
	// The Secret is mounted whole rather than through subPath on purpose: a
	// subPath mount pins the files at pod start, while a whole-volume mount is
	// refreshed by the kubelet after an in-place update to the Secret, which
	// is what lets a renewed certificate reach a running instance without a
	// restart.
	boltTLSVolumeName = "bolt-tls"
	boltTLSMountPath  = "/etc/memgraph/ssl"

	// intraClusterTLSVolumeName and its mount path are the same for the
	// certificate the members authenticate each other with, and for the same
	// reasons.
	intraClusterTLSVolumeName = "intra-cluster-tls"
	intraClusterTLSMountPath  = "/etc/memgraph/intra_cluster_tls"

	// caKey is the key the intra-cluster Secret holds its CA under, beside
	// the two kubernetes.io/tls keys; it is the key cert-manager writes.
	caKey = "ca.crt"

	// containerName is the Memgraph container's name, and doubles as the $0 the
	// coordinator's shell wrapper is given.
	containerName = "memgraph"

	// sysctlContainerName is the init container that raises the node's
	// vm.max_map_count, named as the HA chart names it.
	sysctlContainerName = "init-sysctl"

	// Container names of the two optional containers core dumps bring along.
	corePatternContainerName = "init-core-pattern"
	uploaderContainerName    = "core-dumps-uploader"

	// fixOwnershipContainerName is the init container that chowns the volume
	// mount points to the memgraph user, named as the HA chart names it.
	fixOwnershipContainerName = "init-fix-perms"

	// terminationGracePeriod is how long a pod gets to shut down cleanly before
	// SIGKILL. Kubernetes' own default of 30 seconds was harmless while nothing
	// routinely deleted these pods; it is wrong now that the operator deletes
	// every one of them on every pod-template change, because an instance killed
	// mid-shutdown recovers from its write-ahead log on startup and lengthens
	// exactly the catch-up the rolling restart then waits on. This is a ceiling
	// and not a delay — an instance that exits in two seconds costs two seconds —
	// so it is a constant rather than a knob until someone needs a different one.
	terminationGracePeriod int64 = 300
)

// CoordinatorStatefulSet builds the single StatefulSet running all
// coordinator instances. Per-pod identity (coordinator ID, advertised FQDN)
// is derived from the zero-based pod ordinal at startup, so the pod template
// stays uniform across replicas.
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
	container.Command = shellCommand(coordinatorStartScript(cluster, spec))
	// The flags are handed to the wrapper as arguments rather than interpolated
	// into the script, so `exec ... "$@"` passes each one to Memgraph verbatim.
	// `sh -c` assigns the first operand to $0, so it is a placeholder name and
	// not a flag.
	container.Args = append([]string{containerName}, commonArgs(spec, role)...)
	container.Env = append([]corev1.EnvVar{{
		Name: memgraphcomv1alpha1.EnvPodName,
		ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
		},
	}}, container.Env...)
	container.Ports = []corev1.ContainerPort{
		{Name: boltPortName, ContainerPort: memgraphcomv1alpha1.BoltPort},
		{Name: managementPortName, ContainerPort: memgraphcomv1alpha1.ManagementPort},
		{Name: coordinatorPortName, ContainerPort: memgraphcomv1alpha1.CoordinatorPort},
		{Name: metricsPortName, ContainerPort: memgraphcomv1alpha1.MetricsPort},
	}
	// Coordinators are probed on their Raft port: it is the one they serve
	// even before the Raft cluster has been formed.
	container.ReadinessProbe = tcpProbe(memgraphcomv1alpha1.CoordinatorPort, spec.readinessProbe)

	return statefulSet(cluster, coordinatorComponent, CoordinatorName(cluster), spec, role, replicas, container)
}

// DataStatefulSet builds the single StatefulSet running all data instances. The
// replica count is an argument for the reason CoordinatorStatefulSet documents;
// DeclaredDataInstances is the count for a cluster that is not shrinking.
func DataStatefulSet(cluster *memgraphcomv1alpha1.MemgraphCluster, replicas int32) *appsv1.StatefulSet {
	spec := normalize(cluster.Spec)
	role := spec.dataRole

	container := memgraphContainer(spec, role)
	container.Args = commonArgs(spec, role)
	container.Ports = []corev1.ContainerPort{
		{Name: boltPortName, ContainerPort: memgraphcomv1alpha1.BoltPort},
		{Name: managementPortName, ContainerPort: memgraphcomv1alpha1.ManagementPort},
		{Name: replicationPortName, ContainerPort: memgraphcomv1alpha1.ReplicationPort},
		{Name: metricsPortName, ContainerPort: memgraphcomv1alpha1.MetricsPort},
	}
	container.ReadinessProbe = tcpProbe(memgraphcomv1alpha1.BoltPort, spec.readinessProbe)

	return statefulSet(cluster, dataComponent, DataName(cluster), spec, role, replicas, container)
}

// coordinatorStartScript derives the coordinator's identity from its pod
// ordinal (the numeric suffix of the pod name): ordinal N becomes coordinator
// ID N and is advertised at the pod's stable DNS name within the headless
// Service. Every other argument is forwarded by "$@" — only the derived ones
// are written into the script.
func coordinatorStartScript(
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	spec normalizedSpec,
) string {
	fqdnSuffix := podFQDNSuffix(cluster, CoordinatorName(cluster), spec)
	return fmt.Sprintf(`ordinal="${POD_NAME##*-}"
exec %s \
  --coordinator-id="$ordinal" \
  --coordinator-hostname="${POD_NAME}.%s" \
  --coordinator-port=%d \
  "$@"`, memgraphBinary, fqdnSuffix, memgraphcomv1alpha1.CoordinatorPort)
}

// commonArgs are the command-line arguments both roles' Memgraph containers
// start with: the role's flag file, then the flags the operator pins. The
// command line carries exactly the flags spec.flags may not set, and nothing
// else; every other flag, the operator's own logging defaults included, lives
// in the flag file (see flags.go). gflags processes --flag-file where it
// stands in the argument list and takes the last occurrence of a repeated
// flag, so a pinned flag after the file wins even if a spelling of it slipped
// past admission.
//
// The pinned set is what the rest of the cluster depends on: the ports and
// listen addresses the Services, probes, registrations, the metrics scrapers
// and the Vector sidecar reach the instance at, the metrics format, the data
// directory on the lib claim, and the log file.
//
// A role that opted out of log storage gets --log-file with an empty value,
// which is what turns file logging off. Leaving the flag out would not: the
// image ships /etc/memgraph/memgraph.conf with log_file set to the path below,
// Memgraph parses that file before the command line, and failing to open the
// resulting path is fatal — so an unmounted log directory on a read-only root
// filesystem would crash-loop the pod. also_log_to_stderr in the flag file
// keeps the logs in `kubectl logs` either way.
//
// A cluster with Bolt TLS gets the certificate and key flags last. Memgraph
// serves the metrics endpoint from the same server context, so the two flags
// turn 9091 to https as well; nothing else about the args changes. A cluster
// with intra-cluster TLS gets the three cluster flags after those: Memgraph
// refuses to start with only some of them, so they always travel together.
func commonArgs(spec normalizedSpec, role normalizedRole) []string {
	logDestination := logFile
	if !role.storage.createLogClaim {
		logDestination = ""
	}
	args := []string{
		"--flag-file=" + flagFilePath,
		fmt.Sprintf("--bolt-port=%d", memgraphcomv1alpha1.BoltPort),
		"--bolt-address=" + listenAddress,
		fmt.Sprintf("--management-port=%d", memgraphcomv1alpha1.ManagementPort),
		// The metrics endpoint is served either way; the port is pinned so it
		// agrees with the declared container and Service ports, and the format
		// so that it is what the operator says rather than what the image's
		// default happens to be (JSON before 3.13, deprecated since).
		fmt.Sprintf("--metrics-port=%d", memgraphcomv1alpha1.MetricsPort),
		"--metrics-format=OpenMetrics",
		// The monitoring websocket is what the Vector sidecar dials on the
		// loopback address, so where it listens is pinned like the ports.
		"--monitoring-address=" + listenAddress,
		fmt.Sprintf("--monitoring-port=%d", memgraphcomv1alpha1.MonitoringPort),
		"--data-directory=" + dataDirectory,
		"--log-file=" + logDestination,
	}
	if spec.boltTLSSecret != "" {
		args = append(args,
			"--bolt-cert-file="+boltTLSMountPath+"/"+corev1.TLSCertKey,
			"--bolt-key-file="+boltTLSMountPath+"/"+corev1.TLSPrivateKeyKey,
		)
	}
	if spec.intraClusterTLSSecret != "" {
		args = append(args,
			"--cluster-cert-file="+intraClusterTLSMountPath+"/"+corev1.TLSCertKey,
			"--cluster-key-file="+intraClusterTLSMountPath+"/"+corev1.TLSPrivateKeyKey,
			"--cluster-ca-file="+intraClusterTLSMountPath+"/"+caKey,
		)
	}
	return args
}

// memgraphContainer builds the parts of the Memgraph container shared by both
// roles: image, license env wiring, the role's extra environment and resources,
// storage mounts, and the restricted security context.
func memgraphContainer(spec normalizedSpec, role normalizedRole) corev1.Container {
	return corev1.Container{
		Name:            containerName,
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
		VolumeMounts:    volumeMounts(spec, role),
		SecurityContext: restrictedSecurityContext(),
	}
}

// shellCommand runs a script under the image's POSIX shell, failing on the
// first command that fails.
func shellCommand(script string) []string {
	return []string{"/bin/sh", "-ec", script}
}

// restrictedSecurityContext is what every container the operator builds runs
// under: no privilege escalation, no capabilities, a read-only root filesystem
// and the default seccomp profile. The init containers are the exception: the
// two that write under /proc/sys and the one that chowns the volumes cannot do
// their job under this.
func restrictedSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		ReadOnlyRootFilesystem:   ptr.To(true),
		RunAsNonRoot:             ptr.To(true),
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

// sysctlInitContainer raises the node's vm.max_map_count to the cluster's
// floor. It runs first, before the core pattern container, as in the HA chart.
// The value is a property of the node and not one of the namespaced sysctls a
// pod may set for itself, so like the core pattern this takes a privileged
// root container; it runs the cluster's own Memgraph image and writes
// /proc/sys directly, so no sysctl binary and no second image are involved.
// It only ever raises: a node already at or above the floor is left as the
// administrator set it.
func sysctlInitContainer(spec normalizedSpec) corev1.Container {
	// tee rather than a plain redirect so the value that was set is visible in
	// the init container's logs, and the kept value is logged for the same
	// reason.
	script := fmt.Sprintf(`want=%d; have=$(cat /proc/sys/vm/max_map_count)
if [ "$have" -lt "$want" ]; then echo "$want" | tee /proc/sys/vm/max_map_count; else echo "vm.max_map_count is $have, keeping it"; fi`,
		spec.maxMapCount)
	return corev1.Container{
		Name:            sysctlContainerName,
		Image:           spec.image,
		ImagePullPolicy: spec.pullPolicy,
		Command:         shellCommand(script),
		SecurityContext: privilegedRootSecurityContext(),
	}
}

// corePatternInitContainer points the node's kernel at the role's core dumps
// directory. It runs the cluster's own Memgraph image — already pulled on the
// node, so core dumps need no second image to be configured or mirrored — and
// like the sysctl container breaks the restricted security posture: /proc/sys
// is mounted read-only in an unprivileged container, so writing core_pattern
// needs privileged plus root. Nothing else about the pod is relaxed, and a
// namespace that forbids privileged pods can turn this off and have the
// platform manage core_pattern on the node instead.
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
		Command:         []string{"/bin/sh", "-ec", fmt.Sprintf("echo '%s' | tee /proc/sys/kernel/core_pattern", pattern)},
		SecurityContext: privilegedRootSecurityContext(),
	}
}

// privilegedRootSecurityContext is what the two init containers that write
// under /proc/sys run with: privileged root, but still on a read-only root
// filesystem and the default seccomp profile. It and chownSecurityContext are
// the only places the operator relaxes the restricted posture.
func privilegedRootSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		Privileged: ptr.To(true),
		// Kubernetes rejects a privileged container that also forbids
		// privilege escalation, so this one cannot be false.
		AllowPrivilegeEscalation: ptr.To(true),
		ReadOnlyRootFilesystem:   ptr.To(true),
		RunAsUser:                ptr.To(int64(0)),
		RunAsNonRoot:             ptr.To(false),
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

// fixOwnershipInitContainer chowns the role's volume mount points to the
// memgraph user, recursively, for storage drivers that do not honor the pod's
// fsGroup and hand over a volume root owned by root. It runs after the
// node-tuning containers, as in the HA chart, and mounts exactly what the
// Memgraph container will use from the pod's claims: the lib volume, the log
// volume when the role has a log claim, and the core dumps volume when the
// role collects dumps. The uid and gid are the ones the pods run as, from the
// securityContext block or the images' defaults; the chart's separate knobs
// for them could only disagree with the pod. Like the other init containers
// it runs the cluster's own Memgraph image.
func fixOwnershipInitContainer(spec normalizedSpec, role normalizedRole, uid, gid int64) corev1.Container {
	mounts := []corev1.VolumeMount{{Name: libVolumeName, MountPath: libMountPath}}
	if role.storage.createLogClaim {
		mounts = append(mounts, corev1.VolumeMount{Name: logVolumeName, MountPath: logMountPath})
	}
	if role.coreDumps.enabled {
		mounts = append(mounts, corev1.VolumeMount{Name: coreDumpsVolumeName, MountPath: coreDumpsMountPath})
	}
	lines := make([]string, 0, len(mounts))
	for _, mount := range mounts {
		lines = append(lines, fmt.Sprintf("chown -R %d:%d %s", uid, gid, mount.MountPath))
	}
	script := strings.Join(lines, "\n")
	return corev1.Container{
		Name:            fixOwnershipContainerName,
		Image:           spec.image,
		ImagePullPolicy: spec.pullPolicy,
		Command:         shellCommand(script),
		SecurityContext: chownSecurityContext(),
		VolumeMounts:    mounts,
	}
}

// chownSecurityContext is what the ownership-fixing init container runs with:
// root, because the volume root it fixes is owned by root, but not privileged
// and holding CAP_CHOWN alone, the one capability the job takes. Everything
// else stays as restricted as the Memgraph container; it is what the HA
// chart's container runs with.
func chownSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
			Add:  []corev1.Capability{"CHOWN"},
		},
		ReadOnlyRootFilesystem: ptr.To(true),
		RunAsUser:              ptr.To(int64(0)),
		RunAsNonRoot:           ptr.To(false),
		SeccompProfile:         &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
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

// volumeMounts are the Memgraph container's mounts: lib storage, the role's
// flag file, the scratch directory the read-only root filesystem needs, log
// storage unless the role opted out of it, the core dumps directory when the
// role collects dumps, the Bolt and intra-cluster certificates when the
// cluster has those modes, and last the role's own extra mounts.
func volumeMounts(spec normalizedSpec, role normalizedRole) []corev1.VolumeMount {
	mounts := []corev1.VolumeMount{
		{Name: libVolumeName, MountPath: libMountPath},
		{Name: flagsVolumeName, MountPath: flagsMountPath, ReadOnly: true},
	}
	if role.storage.createLogClaim {
		mounts = append(mounts, corev1.VolumeMount{Name: logVolumeName, MountPath: logMountPath})
	}
	mounts = append(mounts, corev1.VolumeMount{Name: tmpVolumeName, MountPath: tmpMountPath})
	if role.coreDumps.enabled {
		mounts = append(mounts,
			corev1.VolumeMount{Name: coreDumpsVolumeName, MountPath: coreDumpsMountPath})
	}
	if spec.boltTLSSecret != "" {
		mounts = append(mounts,
			corev1.VolumeMount{Name: boltTLSVolumeName, MountPath: boltTLSMountPath, ReadOnly: true})
	}
	if spec.intraClusterTLSSecret != "" {
		mounts = append(mounts,
			corev1.VolumeMount{Name: intraClusterTLSVolumeName, MountPath: intraClusterTLSMountPath, ReadOnly: true})
	}
	return append(mounts, role.extraMounts...)
}

// podVolumes is the role's flag file, the scratch directory the read-only root
// filesystem needs, the certificate Secrets of the TLS modes the cluster has,
// plus the role's extra volumes. Everything persistent comes from
// volumeClaimTemplates instead.
//
// The certificate Secrets are projected by key name: tls.crt and tls.key, the
// shape of a kubernetes.io/tls Secret, plus ca.crt for the intra-cluster one.
// A Secret carrying more keys mounts fine, and one missing a key keeps the pod
// from starting, which is how a missing license Secret is reported too.
func podVolumes(
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	spec normalizedSpec,
	role normalizedRole,
	flagsConfigMap string,
) []corev1.Volume {
	volumes := make([]corev1.Volume, 0, 4+len(role.extraVolumes))
	volumes = append(volumes, flagsVolume(flagsConfigMap), corev1.Volume{
		Name: tmpVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	})
	if spec.boltTLSSecret != "" {
		volumes = append(volumes, corev1.Volume{
			Name: boltTLSVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: spec.boltTLSSecret,
					Items: []corev1.KeyToPath{
						{Key: corev1.TLSCertKey, Path: corev1.TLSCertKey},
						{Key: corev1.TLSPrivateKeyKey, Path: corev1.TLSPrivateKeyKey},
					},
				},
			},
		})
	}
	if spec.intraClusterTLSSecret != "" {
		volumes = append(volumes, corev1.Volume{
			Name: intraClusterTLSVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: spec.intraClusterTLSSecret,
					Items: []corev1.KeyToPath{
						{Key: corev1.TLSCertKey, Path: corev1.TLSCertKey},
						{Key: corev1.TLSPrivateKeyKey, Path: corev1.TLSPrivateKeyKey},
						{Key: caKey, Path: caKey},
					},
				},
			},
		})
	}
	if spec.monitoring.vector != nil {
		volumes = append(volumes, vectorVolumes(cluster)...)
	}
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

// podContainers is the Memgraph container, the uploader sidecar when the role
// has one, the Vector sidecar when the cluster ships its logs, then the role's
// user containers. Memgraph stays first, so `kubectl logs` without -c keeps
// showing the database.
func podContainers(
	component string,
	spec normalizedSpec,
	role normalizedRole,
	memgraph corev1.Container,
) []corev1.Container {
	containers := []corev1.Container{memgraph}
	if role.coreDumps.enabled && role.coreDumps.uploader != nil {
		containers = append(containers, uploaderSidecar(role.coreDumps))
	}
	if spec.monitoring.vector != nil {
		containers = append(containers, vectorSidecar(component, spec))
	}
	for _, container := range role.userContainers {
		containers = append(containers, userContainer(container))
	}
	return containers
}

// userContainer passes a user container, sidecar or init, through as
// written, filling in the one thing the pod's security posture needs from it:
// a container naming no securityContext gets the same restricted one as
// Memgraph, so the chart's busybox example runs under the restricted Pod
// Security Standard. A container that names one has said what it needs and
// keeps it.
func userContainer(container corev1.Container) corev1.Container {
	if container.SecurityContext == nil {
		container.SecurityContext = restrictedSecurityContext()
	}
	return container
}

// podInitContainers are the containers a role's pods run before Memgraph, in
// the HA chart's order: the sysctl one if the cluster asked for it, then the
// core pattern one if the role collects dumps and asked the operator to
// configure it, then the ownership one if the cluster asked for it, then the
// role's own init containers. The first two are privileged and the third runs
// as root, so a restricted namespace can have none of them; the user's are
// passed through like the user containers, filling the restricted security
// context on any naming none.
func podInitContainers(spec normalizedSpec, role normalizedRole) []corev1.Container {
	var containers []corev1.Container
	if spec.maxMapCount > 0 {
		containers = append(containers, sysctlInitContainer(spec))
	}
	if role.coreDumps.enabled && role.coreDumps.configurePattern {
		containers = append(containers, corePatternInitContainer(spec))
	}
	// The CRD rejects the block beside a securityContext that leaves the uid
	// or both groups to the platform, so a spec that passed admission always
	// has a target; one that did not gets no container rather than a chown to
	// an identity nobody named.
	if uid, gid, ok := spec.securityContext.chownTarget(); spec.fixOwnership && ok {
		containers = append(containers, fixOwnershipInitContainer(spec, role, uid, gid))
	}
	for _, container := range role.initContainers {
		containers = append(containers, userContainer(container))
	}
	return containers
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
			// The operator replaces pods itself, one at a time and in an order
			// Kubernetes cannot express: data instances before coordinators, the
			// MAIN last, the Raft leader last. RollingUpdate sweeps highest ordinal
			// to lowest and `partition` is a descending cutoff rather than a set, so
			// a MAIN on any ordinal but 0 would be restarted mid-sweep and every
			// such restart costs another coordinator-driven failover.
			//
			// The cost of this is real and permanent: nothing but the operator will
			// ever restart one of these pods again, so a pod-template change no
			// reconcile acts on takes effect never. That is what the Updated
			// condition is for.
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.OnDeleteStatefulSetStrategyType,
			},
			Selector: &metav1.LabelSelector{MatchLabels: selectorLabels(cluster, component)},
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
					// The startup-only flags are part of the template through
					// their digests, so a change to one is a new revision and
					// the controller can see which keys a pod lacks; the
					// run-time flags are deliberately not, so a change to one
					// changes nothing here.
					Annotations: map[string]string{
						FlagsAnnotation: flagsDigests(roleFlags(role)),
					},
				},
				Spec: corev1.PodSpec{
					TerminationGracePeriodSeconds: ptr.To(terminationGracePeriod),
					InitContainers:                podInitContainers(spec, role),
					Containers:                    podContainers(component, spec, role, container),
					// The identity comes from the securityContext block or the
					// images' defaults; the two fields every policy requires
					// are not the block's to change.
					SecurityContext: &corev1.PodSecurityContext{
						RunAsUser:      spec.securityContext.runAsUser,
						RunAsGroup:     spec.securityContext.runAsGroup,
						FSGroup:        spec.securityContext.fsGroup,
						RunAsNonRoot:   ptr.To(true),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Volumes:                   podVolumes(cluster, spec, role, name+"-"+flagsComponent),
					NodeSelector:              role.scheduling.NodeSelector,
					Tolerations:               role.scheduling.Tolerations,
					TopologySpreadConstraints: spreadConstraints(cluster, component, role),
					Affinity:                  podAffinity(cluster, component, spec, role),
					PriorityClassName:         role.scheduling.PriorityClassName,
				},
			},
		},
	}
}

// podAffinity is the pod anti-affinity of one role: the operator's own rule
// first, when the cluster asked for one, then the role's own terms appended
// to it. The two are never merged into one term and the role's never replace
// the operator's, so a user adding a rule of their own cannot silently lose
// the spread the cluster was created with. A role with neither carries no
// affinity at all.
func podAffinity(
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	component string,
	spec normalizedSpec,
	role normalizedRole,
) *corev1.Affinity {
	var anti corev1.PodAntiAffinity
	if rule := spec.podAntiAffinity; rule != nil {
		// Scope role keeps this role's pods apart; scope cluster keeps every
		// pod of the cluster apart, so its selector drops the component label.
		selector := selectorLabels(cluster, component)
		if rule.scope == memgraphcomv1alpha1.PodAntiAffinityScopeCluster {
			delete(selector, componentLabel)
		}
		term := corev1.PodAffinityTerm{
			LabelSelector: &metav1.LabelSelector{MatchLabels: selector},
			TopologyKey:   rule.topologyKey,
		}
		if rule.typ == memgraphcomv1alpha1.PodAntiAffinityRequired {
			anti.RequiredDuringSchedulingIgnoredDuringExecution = []corev1.PodAffinityTerm{term}
		} else {
			anti.PreferredDuringSchedulingIgnoredDuringExecution = []corev1.WeightedPodAffinityTerm{{
				Weight:          memgraphcomv1alpha1.PodAntiAffinityWeight,
				PodAffinityTerm: term,
			}}
		}
	}
	if own := role.scheduling.PodAntiAffinity; own != nil {
		anti.RequiredDuringSchedulingIgnoredDuringExecution = append(
			anti.RequiredDuringSchedulingIgnoredDuringExecution,
			own.RequiredDuringSchedulingIgnoredDuringExecution...)
		anti.PreferredDuringSchedulingIgnoredDuringExecution = append(
			anti.PreferredDuringSchedulingIgnoredDuringExecution,
			own.PreferredDuringSchedulingIgnoredDuringExecution...)
	}
	if anti.RequiredDuringSchedulingIgnoredDuringExecution == nil &&
		anti.PreferredDuringSchedulingIgnoredDuringExecution == nil {
		return nil
	}
	return &corev1.Affinity{PodAntiAffinity: &anti}
}

// spreadConstraints passes the role's topology spread constraints through,
// giving a constraint that names no labelSelector the role's own pod selector:
// the labels are the operator's, so asking the user to repeat them would only
// invite a typo that spreads nothing.
func spreadConstraints(
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	component string,
	role normalizedRole,
) []corev1.TopologySpreadConstraint {
	if len(role.scheduling.TopologySpreadConstraints) == 0 {
		return nil
	}
	constraints := make([]corev1.TopologySpreadConstraint, 0, len(role.scheduling.TopologySpreadConstraints))
	for _, c := range role.scheduling.TopologySpreadConstraints {
		if c.LabelSelector == nil {
			c.LabelSelector = &metav1.LabelSelector{MatchLabels: selectorLabels(cluster, component)}
		}
		constraints = append(constraints, c)
	}
	return constraints
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

// tcpProbe builds the one probe a role's container carries, its readiness
// probe. The handler is always a TCP-socket check against the role's own port —
// the probe type is deliberately not configurable — so only the timings come
// from the spec. No liveness or startup probe is built, on purpose: a data
// instance opens no port until every database is recovered, so a liveness
// check could only ever kill a recovery that outlived a guessed budget, and a
// Bolt listener that goes away after startup means the process is gone, which
// ends the container without any probe. ReadinessProbeSpec carries the argument.
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
