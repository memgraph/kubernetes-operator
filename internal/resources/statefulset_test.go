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
	"fmt"
	"maps"
	"slices"
	"strings"
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
	metricsPortName     = "metrics"

	statefulSetKind = "StatefulSet"
	serviceKind     = "Service"

	tmpVolume = "tmp"

	// The Bolt TLS Secret a fixture names, and the volume the builder mounts
	// it as.
	boltTLSSecretName = "bolt-tls"
	boltTLSVolume     = "bolt-tls"

	// The intra-cluster TLS Secret a fixture names, and its volume.
	intraTLSSecretName = "intra-cluster-tls"
	intraTLSVolume     = "intra-cluster-tls"

	// The keys the TLS Secrets are projected by, and where each mode's Secret
	// is mounted.
	tlsCertKey      = "tls.crt"
	tlsKeyKey       = "tls.key"
	tlsCAKey        = "ca.crt"
	boltTLSMount    = "/etc/memgraph/ssl"
	intraTLSMount   = "/etc/memgraph/intra_cluster_tls"
	shell           = "/bin/sh"
	defaultImageRef = memgraphcomv1alpha1.DefaultImageReference
	coreDumpsVolume = "core-dumps"
	libVolume       = "lib-storage"
	logVolume       = "log-storage"
	uploaderImage   = "amazon/aws-cli:2.33.28"
	libPath         = "/var/lib/memgraph"
	logPath         = "/var/log/memgraph"
	coreDumpsPath   = "/var/core/memgraph"
	dataPath        = "/var/lib/memgraph/mg_data"
	logFilePath     = "/var/log/memgraph/memgraph.log"

	// The operator's identity labels, which custom labels may never override.
	nameLabel      = "app.kubernetes.io/name"
	instanceLabel  = "app.kubernetes.io/instance"
	componentLabel = "app.kubernetes.io/component"
	managedByLabel = "app.kubernetes.io/managed-by"

	// Custom label keys and values used by the tuning tests.
	teamLabel   = "team"
	tierLabel   = "tier"
	exposeLabel = "expose"

	platformTeam = "platform"
)

// minimalCluster returns a MemgraphCluster as a client would minimally create
// it, deliberately without CRD schema defaults applied: builders must resolve
// defaults themselves on specs that never passed admission.
func minimalCluster() *memgraphcomv1alpha1.MemgraphCluster {
	return &memgraphcomv1alpha1.MemgraphCluster{
		ObjectMeta: metav1.ObjectMeta{Name: clusterName, Namespace: testNamespace},
	}
}

// coordinatorStatefulSet and dataStatefulSet build a role's StatefulSet at the
// replica count the spec declares — the count the controller derives for a
// cluster that is growing or holding its size. The count a shrinking cluster is
// held at is the controller's decision, so the cases that cover it pass it to
// the builder directly.
func coordinatorStatefulSet(cluster *memgraphcomv1alpha1.MemgraphCluster) *appsv1.StatefulSet {
	return resources.CoordinatorStatefulSet(cluster, resources.DeclaredCoordinators(cluster))
}

func dataStatefulSet(cluster *memgraphcomv1alpha1.MemgraphCluster) *appsv1.StatefulSet {
	return resources.DataStatefulSet(cluster, resources.DeclaredDataInstances(cluster))
}

func endpoint(host string, port int32) string {
	return fmt.Sprintf("%s:%d", host, port)
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

// Non-default cluster domain shared by the tuning tests.
const customClusterDomain = "k8s.example.com"

// tunedCluster returns a MemgraphCluster with every pod-tuning knob set away
// from its default, so the golden tests can pin what each one lands on.
func tunedCluster() *memgraphcomv1alpha1.MemgraphCluster {
	cluster := minimalCluster()
	cluster.Spec.ClusterDomain = customClusterDomain
	cluster.Spec.ReadinessProbe = memgraphcomv1alpha1.ReadinessProbeSpec{
		TimeoutSeconds: ptr.To(int32(3)),
		PeriodSeconds:  ptr.To(int32(2)),
	}
	cluster.Spec.Resources = memgraphcomv1alpha1.ResourcesSpec{
		Coordinators: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
			Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
		},
		Data: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("4Gi")},
		},
	}
	cluster.Spec.Labels = memgraphcomv1alpha1.LabelsSpec{
		Coordinators: memgraphcomv1alpha1.RoleLabelsSpec{
			PodLabels:         map[string]string{teamLabel: platformTeam},
			StatefulSetLabels: map[string]string{tierLabel: "control"},
			ServiceLabels:     map[string]string{exposeLabel: "internal"},
		},
		Data: memgraphcomv1alpha1.RoleLabelsSpec{
			PodLabels:         map[string]string{teamLabel: dataComponent},
			StatefulSetLabels: map[string]string{tierLabel: "storage"},
			ServiceLabels:     map[string]string{exposeLabel: "bolt"},
		},
	}
	cluster.Spec.ExtraEnv = memgraphcomv1alpha1.ExtraEnvSpec{
		Coordinators: []memgraphcomv1alpha1.EnvVar{{Name: "COORDINATOR_LABEL", Value: "coord"}},
		Data: []memgraphcomv1alpha1.EnvVar{
			{Name: "DATA_LABEL_ONE", Value: "one"},
			{Name: "DATA_LABEL_TWO", Value: "two"},
		},
	}
	cluster.Spec.ExtraArgs = memgraphcomv1alpha1.ExtraArgsSpec{
		Coordinators: []string{"--log-level=WARNING"},
		Data:         []string{"--storage-snapshot-on-exit=true", "--memory-limit=2048"},
	}
	return cluster
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

// tcpProbe is a readiness probe with the default timings and the given failure
// threshold.
func tcpProbe(port, failureThreshold int32) *corev1.Probe {
	return tunedTCPProbe(port, failureThreshold, 10, 5)
}

func tunedTCPProbe(port, failureThreshold, timeoutSeconds, periodSeconds int32) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(port)},
		},
		FailureThreshold: failureThreshold,
		TimeoutSeconds:   timeoutSeconds,
		PeriodSeconds:    periodSeconds,
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

// expectedPrivilegedSecurityContext is what the init containers that write
// under /proc/sys run with: privileged root, and a read-only root filesystem
// plus the default seccomp profile all the same.
func expectedPrivilegedSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		Privileged:               ptr.To(true),
		AllowPrivilegeEscalation: ptr.To(true),
		ReadOnlyRootFilesystem:   ptr.To(true),
		RunAsUser:                ptr.To(int64(0)),
		RunAsNonRoot:             ptr.To(false),
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

// expectedSysctlInitContainer is the init container every pod of a cluster
// with the sysctlInitContainer block runs first: it raises vm.max_map_count to
// the floor, and only raises, on the cluster's own image.
func expectedSysctlInitContainer(maxMapCount int64) corev1.Container {
	return corev1.Container{
		Name:            "init-sysctl",
		Image:           defaultImageRef,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command: expectedCommand(fmt.Sprintf(`want=%d; have=$(cat /proc/sys/vm/max_map_count)
if [ "$have" -lt "$want" ]; then echo "$want" | tee /proc/sys/vm/max_map_count; else echo "vm.max_map_count is $have, keeping it"; fi`,
			maxMapCount)),
		SecurityContext: expectedPrivilegedSecurityContext(),
	}
}

// expectedCorePatternInitContainer is the init container a role collecting
// dumps runs to point the node's kernel at its volume.
func expectedCorePatternInitContainer() corev1.Container {
	return corev1.Container{
		Name:            "init-core-pattern",
		Image:           defaultImageRef,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command: expectedCommand(
			"echo '/var/core/memgraph/core.%e.%p.%t.%s' | tee /proc/sys/kernel/core_pattern"),
		SecurityContext: expectedPrivilegedSecurityContext(),
	}
}

// expectedFixOwnershipInitContainer is the init container every pod of a
// cluster with the fixOwnershipInitContainer block runs last: root with
// CAP_CHOWN alone, on the cluster's own image, chowning each of the given
// mounts to the pod's identity.
func expectedFixOwnershipInitContainer(owner string, mounts ...corev1.VolumeMount) corev1.Container {
	lines := make([]string, 0, len(mounts))
	for _, mount := range mounts {
		lines = append(lines, "chown -R "+owner+" "+mount.MountPath)
	}
	return corev1.Container{
		Name:            "init-fix-perms",
		Image:           defaultImageRef,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         expectedCommand(strings.Join(lines, "\n")),
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
				Add:  []corev1.Capability{"CHOWN"},
			},
			ReadOnlyRootFilesystem: ptr.To(true),
			RunAsUser:              ptr.To(int64(0)),
			RunAsNonRoot:           ptr.To(false),
			SeccompProfile:         &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		VolumeMounts: mounts,
	}
}

// sysctlCluster is a minimal cluster with the sysctlInitContainer block
// present and empty, the way the quickstart example carries it.
func sysctlCluster() *memgraphcomv1alpha1.MemgraphCluster {
	cluster := minimalCluster()
	cluster.Spec.SysctlInitContainer = &memgraphcomv1alpha1.SysctlInitContainerSpec{}
	return cluster
}

func expectedVolumeMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{Name: libVolume, MountPath: libPath},
		{Name: logVolume, MountPath: logPath},
		{Name: tmpVolume, MountPath: "/tmp"},
	}
}

// expectedVolumeMountsWithoutLog is the mount set of a role that opted out of
// log storage: everything except the log volume.
func expectedVolumeMountsWithoutLog() []corev1.VolumeMount {
	return slices.DeleteFunc(expectedVolumeMounts(), func(mount corev1.VolumeMount) bool {
		return mount.Name == logVolume
	})
}

// expectedCommand wraps a coordinator start script the way the builder does.
func expectedCommand(script string) []string {
	return []string{shell, "-ec", script}
}

// expectedArgs are the flags a role is started with: the shared ones in the
// order the builder emits them, then the fixture's extra args. The ports are
// the fixed internal ones; a role that opted out of log storage gets an empty
// --log-file, so that is a parameter.
func expectedArgs(logDestination string, extra ...string) []string {
	return append([]string{
		fmt.Sprintf("--bolt-port=%d", memgraphcomv1alpha1.BoltPort),
		fmt.Sprintf("--management-port=%d", memgraphcomv1alpha1.ManagementPort),
		fmt.Sprintf("--metrics-port=%d", memgraphcomv1alpha1.MetricsPort),
		"--metrics-format=OpenMetrics",
		"--data-directory=" + dataPath,
		"--log-level=TRACE",
		"--also-log-to-stderr",
		"--log-file=" + logDestination,
		"--log-retention-days=35",
	}, extra...)
}

// expectedCoordinatorArgs are the same flags as arguments to the coordinator's
// shell wrapper, which forwards them with "$@" — so they are never parsed by
// the shell. The leading element is the wrapper's $0, not a flag.
func expectedCoordinatorArgs(logDestination string, extra ...string) []string {
	return append([]string{memgraphName}, expectedArgs(logDestination, extra...)...)
}

// expectedVolumes covers only the ephemeral scratch volume: lib and log
// storage are provisioned through volumeClaimTemplates.
func expectedVolumes() []corev1.Volume {
	return []corev1.Volume{
		{Name: tmpVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
}

func expectedClaimTemplate(name, size string, accessMode corev1.PersistentVolumeAccessMode,
	class *string) corev1.PersistentVolumeClaim {
	return corev1.PersistentVolumeClaim{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{accessMode},
			StorageClassName: class,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)},
			},
		},
	}
}

// expectedClaimTemplates are the claims a spec that never set storage gets:
// 1Gi ReadWriteOnce on the cluster's default StorageClass for both volumes.
func expectedClaimTemplates() []corev1.PersistentVolumeClaim {
	return []corev1.PersistentVolumeClaim{
		expectedClaimTemplate("lib-storage", "1Gi", corev1.ReadWriteOnce, nil),
		expectedClaimTemplate(logVolume, "1Gi", corev1.ReadWriteOnce, nil),
	}
}

// expectedRetentionPolicy is the claim retention policy both roles get: the one
// retention knob decides both halves, so a claim orphaned by deleting the
// cluster and one orphaned by scaling a role down are treated alike.
func expectedRetentionPolicy(
	policy appsv1.PersistentVolumeClaimRetentionPolicyType,
) *appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy {
	return &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{
		WhenDeleted: policy,
		WhenScaled:  policy,
	}
}

func expectedLabels(component string) map[string]string {
	return map[string]string{
		nameLabel:      memgraphName,
		instanceLabel:  clusterName,
		componentLabel: component,
		managedByLabel: "memgraph-operator",
	}
}

// expectedLabelsWith is the full label set of a role's object once the user's
// custom labels are merged in.
func expectedLabelsWith(component string, custom map[string]string) map[string]string {
	l := expectedLabels(component)
	maps.Copy(l, custom)
	return l
}

func expectedSelectorLabels(component string) map[string]string {
	return map[string]string{
		nameLabel:      memgraphName,
		instanceLabel:  clusterName,
		componentLabel: component,
	}
}

var expectedCoordinatorScript = fmt.Sprintf(`ordinal="${POD_NAME##*-}"
exec /usr/lib/memgraph/memgraph \
  --coordinator-id="$ordinal" \
  --coordinator-hostname="${POD_NAME}.example-coordinator.memgraph-test.svc.cluster.local" \
  --coordinator-port=%d \
  "$@"`, memgraphcomv1alpha1.CoordinatorPort)

func TestCoordinatorStatefulSetDefaults(t *testing.T) {
	want := &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: statefulSetKind},
		ObjectMeta: metav1.ObjectMeta{
			Name:      coordinatorName,
			Namespace: testNamespace,
			Labels:    expectedLabels(coordinatorComponent),
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:            ptr.To(int32(3)),
			ServiceName:         coordinatorName,
			PodManagementPolicy: appsv1.ParallelPodManagement,
			// The operator replaces these pods itself, one at a time and MAIN or Raft
			// leader last, which no RollingUpdate can express.
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.OnDeleteStatefulSetStrategyType,
			},
			Selector: &metav1.LabelSelector{MatchLabels: expectedSelectorLabels(coordinatorComponent)},
			PersistentVolumeClaimRetentionPolicy: expectedRetentionPolicy(
				appsv1.RetainPersistentVolumeClaimRetentionPolicyType),
			VolumeClaimTemplates: expectedClaimTemplates(),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: expectedLabels(coordinatorComponent)},
				Spec: corev1.PodSpec{
					// Kubernetes' 30-second default is too short for a database that is
					// now restarted on every pod-template change: an instance killed
					// mid-shutdown recovers from its WAL and lengthens the catch-up the
					// rolling restart waits on.
					TerminationGracePeriodSeconds: ptr.To(int64(300)),
					Containers: []corev1.Container{{
						Name:            memgraphName,
						Image:           defaultImageRef,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Command:         expectedCommand(expectedCoordinatorScript),
						Args:            expectedCoordinatorArgs(logFilePath),
						Env: append([]corev1.EnvVar{{
							Name: "POD_NAME",
							ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
							},
						}}, licenseEnv("memgraph-secrets", "MEMGRAPH_ENTERPRISE_LICENSE", "MEMGRAPH_ORGANIZATION_NAME")...),
						Ports: []corev1.ContainerPort{
							{Name: boltPortName, ContainerPort: memgraphcomv1alpha1.BoltPort},
							{Name: managementPortName, ContainerPort: memgraphcomv1alpha1.ManagementPort},
							{Name: coordinatorComponent, ContainerPort: memgraphcomv1alpha1.CoordinatorPort},
							{Name: metricsPortName, ContainerPort: memgraphcomv1alpha1.MetricsPort},
						},
						ReadinessProbe:  tcpProbe(memgraphcomv1alpha1.CoordinatorPort, 20),
						VolumeMounts:    expectedVolumeMounts(),
						SecurityContext: expectedContainerSecurityContext(),
					}},
					SecurityContext: expectedPodSecurityContext(),
					Volumes:         expectedVolumes(),
				},
			},
		},
	}

	got := coordinatorStatefulSet(minimalCluster())
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("CoordinatorStatefulSet() mismatch (-want +got):\n%s", diff)
	}
}

func TestDataStatefulSetDefaults(t *testing.T) {
	want := &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: statefulSetKind},
		ObjectMeta: metav1.ObjectMeta{
			Name:      dataName,
			Namespace: testNamespace,
			Labels:    expectedLabels(dataComponent),
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:            ptr.To(int32(2)),
			ServiceName:         dataName,
			PodManagementPolicy: appsv1.ParallelPodManagement,
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.OnDeleteStatefulSetStrategyType,
			},
			Selector: &metav1.LabelSelector{MatchLabels: expectedSelectorLabels(dataComponent)},
			PersistentVolumeClaimRetentionPolicy: expectedRetentionPolicy(
				appsv1.RetainPersistentVolumeClaimRetentionPolicyType),
			VolumeClaimTemplates: expectedClaimTemplates(),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: expectedLabels(dataComponent)},
				Spec: corev1.PodSpec{
					// Kubernetes' 30-second default is too short for a database that is
					// now restarted on every pod-template change: an instance killed
					// mid-shutdown recovers from its WAL and lengthens the catch-up the
					// rolling restart waits on.
					TerminationGracePeriodSeconds: ptr.To(int64(300)),
					Containers: []corev1.Container{{
						Name:            memgraphName,
						Image:           defaultImageRef,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Args:            expectedArgs(logFilePath),
						Env:             licenseEnv("memgraph-secrets", "MEMGRAPH_ENTERPRISE_LICENSE", "MEMGRAPH_ORGANIZATION_NAME"),
						Ports: []corev1.ContainerPort{
							{Name: boltPortName, ContainerPort: memgraphcomv1alpha1.BoltPort},
							{Name: managementPortName, ContainerPort: memgraphcomv1alpha1.ManagementPort},
							{Name: replicationPortName, ContainerPort: memgraphcomv1alpha1.ReplicationPort},
							{Name: metricsPortName, ContainerPort: memgraphcomv1alpha1.MetricsPort},
						},
						ReadinessProbe:  tcpProbe(memgraphcomv1alpha1.BoltPort, 20),
						VolumeMounts:    expectedVolumeMounts(),
						SecurityContext: expectedContainerSecurityContext(),
					}},
					SecurityContext: expectedPodSecurityContext(),
					Volumes:         expectedVolumes(),
				},
			},
		},
	}

	got := dataStatefulSet(minimalCluster())
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("DataStatefulSet() mismatch (-want +got):\n%s", diff)
	}
}

// TestStatefulSetReplicasFollowTheArgument pins the replica count to the
// builder's argument rather than to the spec. That separation is what lets the
// controller hold a role at its current size while a lowered count is being
// retired, without the builders having to know anything about the live cluster.
func TestStatefulSetReplicasFollowTheArgument(t *testing.T) {
	// The spec declares fewer replicas than the cluster currently runs, which is
	// the count the controller passes so a shrink never sheds pods on its own.
	cluster := minimalCluster()
	cluster.Spec.Coordinators = ptr.To(int32(3))
	cluster.Spec.DataInstances = ptr.To(int32(2))

	tests := []struct {
		name string
		sts  *appsv1.StatefulSet
		want int32
	}{
		{name: coordinatorComponent, sts: resources.CoordinatorStatefulSet(cluster, 5), want: 5},
		{name: dataComponent, sts: resources.DataStatefulSet(cluster, 3), want: 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := *tc.sts.Spec.Replicas; got != tc.want {
				t.Errorf("replicas = %d, want the given %d, not the declared count", got, tc.want)
			}
		})
	}
}

// The declared counts are what the controller passes for a cluster that is not
// shrinking, so they resolve the same schema defaults the builders do.
func TestDeclaredCounts(t *testing.T) {
	if got := resources.DeclaredCoordinators(minimalCluster()); got != memgraphcomv1alpha1.DefaultCoordinatorCount {
		t.Errorf("DeclaredCoordinators() = %d, want the schema default %d",
			got, memgraphcomv1alpha1.DefaultCoordinatorCount)
	}
	if got := resources.DeclaredDataInstances(minimalCluster()); got != memgraphcomv1alpha1.DefaultDataInstanceCount {
		t.Errorf("DeclaredDataInstances() = %d, want the schema default %d",
			got, memgraphcomv1alpha1.DefaultDataInstanceCount)
	}

	cluster := specifiedCluster()
	if got := resources.DeclaredCoordinators(cluster); got != 5 {
		t.Errorf("DeclaredCoordinators() = %d, want 5", got)
	}
	if got := resources.DeclaredDataInstances(cluster); got != 3 {
		t.Errorf("DeclaredDataInstances() = %d, want 3", got)
	}
}

func TestStatefulSetSpecOverrides(t *testing.T) {
	cluster := specifiedCluster()

	tests := []struct {
		name     string
		sts      *appsv1.StatefulSet
		replicas int32
	}{
		{name: coordinatorComponent, sts: coordinatorStatefulSet(cluster), replicas: 5},
		{name: dataComponent, sts: dataStatefulSet(cluster), replicas: 3},
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

// TestStatefulSetStorageOverrides asserts each role's claim templates follow
// that role's storage block only, so sizing coordinators and data instances
// differently really produces differently sized claims.
func TestStatefulSetStorageOverrides(t *testing.T) {
	cluster := minimalCluster()
	cluster.Spec.Storage = memgraphcomv1alpha1.StorageSpec{
		Coordinators: memgraphcomv1alpha1.RoleStorageSpec{
			LibPVCSize:           ptr.To(resource.MustParse("4Gi")),
			LibStorageAccessMode: corev1.ReadWriteOncePod,
			LibStorageClassName:  ptr.To("fast-ssd"),
			LogPVCSize:           ptr.To(resource.MustParse("512Mi")),
			LogStorageClassName:  ptr.To(""),
		},
		Data: memgraphcomv1alpha1.RoleStorageSpec{
			LibPVCSize:          ptr.To(resource.MustParse("100Gi")),
			LibStorageClassName: ptr.To("gp3"),
		},
	}

	tests := []struct {
		name string
		sts  *appsv1.StatefulSet
		want []corev1.PersistentVolumeClaim
	}{
		{
			name: coordinatorComponent,
			sts:  coordinatorStatefulSet(cluster),
			want: []corev1.PersistentVolumeClaim{
				expectedClaimTemplate("lib-storage", "4Gi", corev1.ReadWriteOncePod, ptr.To("fast-ssd")),
				// An empty storage class is passed through verbatim: it means
				// "no dynamic provisioning", not "cluster default".
				expectedClaimTemplate(logVolume, "512Mi", corev1.ReadWriteOnce, ptr.To("")),
			},
		},
		{
			name: dataComponent,
			sts:  dataStatefulSet(cluster),
			want: []corev1.PersistentVolumeClaim{
				expectedClaimTemplate("lib-storage", "100Gi", corev1.ReadWriteOnce, ptr.To("gp3")),
				// Untouched by the spec, so it keeps every schema default.
				expectedClaimTemplate(logVolume, "1Gi", corev1.ReadWriteOnce, nil),
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, tc.sts.Spec.VolumeClaimTemplates); diff != "" {
				t.Errorf("volume claim templates mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestStatefulSetWithoutLogStorageClaim asserts that a role which opted out of
// log storage gets no log claim, no log mount, and an empty --log-file. The
// empty flag is load-bearing rather than cosmetic: the image's
// /etc/memgraph/memgraph.conf sets log_file, Memgraph reads it before the
// command line, and it fails startup when that path cannot be opened — so
// dropping the flag would crash-loop the pod instead of disabling file logging.
// Logs still reach `kubectl logs` through --also-log-to-stderr.
func TestStatefulSetWithoutLogStorageClaim(t *testing.T) {
	cluster := minimalCluster()
	cluster.Spec.Storage = memgraphcomv1alpha1.StorageSpec{
		Coordinators: memgraphcomv1alpha1.RoleStorageSpec{
			CreateLogStorageClaim: ptr.To(false),
		},
		// The data instances keep their log claim: the knob is per role.
		Data: memgraphcomv1alpha1.RoleStorageSpec{},
	}

	t.Run(coordinatorComponent, func(t *testing.T) {
		sts := coordinatorStatefulSet(cluster)

		wantClaims := []corev1.PersistentVolumeClaim{
			expectedClaimTemplate("lib-storage", "1Gi", corev1.ReadWriteOnce, nil),
		}
		if diff := cmp.Diff(wantClaims, sts.Spec.VolumeClaimTemplates); diff != "" {
			t.Errorf("volume claim templates mismatch (-want +got):\n%s", diff)
		}

		container := sts.Spec.Template.Spec.Containers[0]
		if diff := cmp.Diff(expectedVolumeMountsWithoutLog(), container.VolumeMounts); diff != "" {
			t.Errorf("volume mounts mismatch (-want +got):\n%s", diff)
		}
		wantCommand := expectedCommand(expectedCoordinatorScript)
		if diff := cmp.Diff(wantCommand, container.Command); diff != "" {
			t.Errorf("start script mismatch (-want +got):\n%s", diff)
		}
		wantArgs := expectedCoordinatorArgs("")
		if diff := cmp.Diff(wantArgs, container.Args); diff != "" {
			t.Errorf("args mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run(dataComponent, func(t *testing.T) {
		sts := dataStatefulSet(cluster)

		if diff := cmp.Diff(expectedClaimTemplates(), sts.Spec.VolumeClaimTemplates); diff != "" {
			t.Errorf("volume claim templates mismatch (-want +got):\n%s", diff)
		}

		container := sts.Spec.Template.Spec.Containers[0]
		if diff := cmp.Diff(expectedVolumeMounts(), container.VolumeMounts); diff != "" {
			t.Errorf("volume mounts mismatch (-want +got):\n%s", diff)
		}
		if !slices.Contains(container.Args, "--log-file=/var/log/memgraph/memgraph.log") {
			t.Errorf("args = %v, want the log file the role still has storage for", container.Args)
		}
	})
}

// TestStatefulSetCoreDumpsDisabledByDefault pins that a spec which never
// mentions core dumps carries no trace of the feature: no claim, no mount, no
// init container, no sidecar.
func TestStatefulSetCoreDumpsDisabledByDefault(t *testing.T) {
	for _, sts := range []*appsv1.StatefulSet{
		coordinatorStatefulSet(minimalCluster()),
		dataStatefulSet(minimalCluster()),
	} {
		t.Run(sts.Name, func(t *testing.T) {
			for _, claim := range sts.Spec.VolumeClaimTemplates {
				if claim.Name == coreDumpsVolume {
					t.Errorf("claim %s exists without core dumps being enabled", claim.Name)
				}
			}
			podSpec := sts.Spec.Template.Spec
			if len(podSpec.InitContainers) != 0 {
				t.Errorf("init containers = %v, want none", podSpec.InitContainers)
			}
			if len(podSpec.Containers) != 1 {
				t.Errorf("containers = %d, want only Memgraph's", len(podSpec.Containers))
			}
		})
	}
}

// TestStatefulSetCoreDumps covers a role with the block present end to end
// while the other stays untouched: the claim, the Memgraph container's mount,
// and the privileged init container that points the node's kernel at it.
func TestStatefulSetCoreDumps(t *testing.T) {
	cluster := minimalCluster()
	cluster.Spec.CoreDumps = memgraphcomv1alpha1.CoreDumpsSpec{
		Data: &memgraphcomv1alpha1.RoleCoreDumpsSpec{
			Size: ptr.To(resource.MustParse("20Gi")),
		},
		StorageClassName: ptr.To("cheap-hdd"),
	}

	t.Run(dataComponent, func(t *testing.T) {
		sts := dataStatefulSet(cluster)

		wantClaims := append(expectedClaimTemplates(),
			expectedClaimTemplate(coreDumpsVolume, "20Gi", corev1.ReadWriteOnce, ptr.To("cheap-hdd")))
		if diff := cmp.Diff(wantClaims, sts.Spec.VolumeClaimTemplates); diff != "" {
			t.Errorf("volume claim templates mismatch (-want +got):\n%s", diff)
		}

		podSpec := sts.Spec.Template.Spec
		wantMounts := append(expectedVolumeMounts(),
			corev1.VolumeMount{Name: coreDumpsVolume, MountPath: coreDumpsPath})
		if diff := cmp.Diff(wantMounts, podSpec.Containers[0].VolumeMounts); diff != "" {
			t.Errorf("volume mounts mismatch (-want +got):\n%s", diff)
		}

		// The init container has to be privileged root to write a kernel sysctl,
		// and it reuses the cluster's Memgraph image so nothing else is pulled.
		wantInit := []corev1.Container{expectedCorePatternInitContainer()}
		if diff := cmp.Diff(wantInit, podSpec.InitContainers); diff != "" {
			t.Errorf("init containers mismatch (-want +got):\n%s", diff)
		}
		if len(podSpec.Containers) != 1 {
			t.Errorf("containers = %d, want only Memgraph's without an uploader", len(podSpec.Containers))
		}
	})

	// The block is per role: coordinators have none and get nothing.
	t.Run(coordinatorComponent, func(t *testing.T) {
		sts := coordinatorStatefulSet(cluster)

		if diff := cmp.Diff(expectedClaimTemplates(), sts.Spec.VolumeClaimTemplates); diff != "" {
			t.Errorf("volume claim templates mismatch (-want +got):\n%s", diff)
		}
		if got := sts.Spec.Template.Spec.InitContainers; len(got) != 0 {
			t.Errorf("init containers = %v, want none", got)
		}
	})
}

// TestStatefulSetCoreDumpsWithoutCorePattern covers the restricted-namespace
// path: the volume is provisioned and mounted, but the operator runs no
// privileged container and trusts the node's own core pattern.
func TestStatefulSetCoreDumpsWithoutCorePattern(t *testing.T) {
	cluster := minimalCluster()
	cluster.Spec.CoreDumps = memgraphcomv1alpha1.CoreDumpsSpec{
		Data:                 &memgraphcomv1alpha1.RoleCoreDumpsSpec{},
		ConfigureCorePattern: ptr.To(false),
	}

	sts := dataStatefulSet(cluster)
	podSpec := sts.Spec.Template.Spec

	if got := podSpec.InitContainers; len(got) != 0 {
		t.Errorf("init containers = %v, want none when the node owns the core pattern", got)
	}
	wantMount := corev1.VolumeMount{Name: coreDumpsVolume, MountPath: coreDumpsPath}
	if got := podSpec.Containers[0].VolumeMounts; !slices.Contains(got, wantMount) {
		t.Errorf("volume mounts = %v, want the core dumps volume mounted anyway", got)
	}
	claims := sts.Spec.VolumeClaimTemplates
	if diff := cmp.Diff(
		expectedClaimTemplate(coreDumpsVolume, "10Gi", corev1.ReadWriteOnce, nil),
		claims[len(claims)-1],
	); diff != "" {
		t.Errorf("core dumps claim mismatch (-want +got):\n%s", diff)
	}
}

// TestStatefulSetSysctlInitContainer covers the presence-based sysctl block:
// present, both roles run the container first with the default or the given
// floor; absent, which the minimal cluster is, no init container at all.
func TestStatefulSetSysctlInitContainer(t *testing.T) {
	t.Run("absent by default", func(t *testing.T) {
		for _, sts := range []*appsv1.StatefulSet{coordinatorStatefulSet(minimalCluster()), dataStatefulSet(minimalCluster())} {
			if got := sts.Spec.Template.Spec.InitContainers; len(got) != 0 {
				t.Errorf("%s init containers = %v, want none without the block", sts.Name, got)
			}
		}
	})

	t.Run("empty block takes the default floor on both roles", func(t *testing.T) {
		cluster := sysctlCluster()

		want := []corev1.Container{expectedSysctlInitContainer(memgraphcomv1alpha1.DefaultMaxMapCount)}
		for _, sts := range []*appsv1.StatefulSet{coordinatorStatefulSet(cluster), dataStatefulSet(cluster)} {
			if diff := cmp.Diff(want, sts.Spec.Template.Spec.InitContainers); diff != "" {
				t.Errorf("%s init containers mismatch (-want +got):\n%s", sts.Name, diff)
			}
		}
	})

	t.Run("floor override", func(t *testing.T) {
		cluster := sysctlCluster()
		cluster.Spec.SysctlInitContainer.MaxMapCount = 1048576

		want := []corev1.Container{expectedSysctlInitContainer(1048576)}
		if diff := cmp.Diff(want, dataStatefulSet(cluster).Spec.Template.Spec.InitContainers); diff != "" {
			t.Errorf("init containers mismatch (-want +got):\n%s", diff)
		}
	})

	// The two privileged containers run in the HA chart's order, sysctl first,
	// and stay separate decisions: a role collecting dumps keeps its core
	// pattern container whether or not the block is present.
	t.Run("before the core pattern container", func(t *testing.T) {
		cluster := sysctlCluster()
		cluster.Spec.CoreDumps.Data = &memgraphcomv1alpha1.RoleCoreDumpsSpec{}

		want := []corev1.Container{
			expectedSysctlInitContainer(memgraphcomv1alpha1.DefaultMaxMapCount),
			expectedCorePatternInitContainer(),
		}
		if diff := cmp.Diff(want, dataStatefulSet(cluster).Spec.Template.Spec.InitContainers); diff != "" {
			t.Errorf("init containers mismatch (-want +got):\n%s", diff)
		}
	})
}

// TestStatefulSetFixOwnershipInitContainer covers the presence-based
// ownership block: present, both roles run the container with exactly the
// claims Memgraph will use mounted; absent, which the minimal cluster is, no
// init container at all.
func TestStatefulSetFixOwnershipInitContainer(t *testing.T) {
	libMount := corev1.VolumeMount{Name: libVolume, MountPath: libPath}
	logMount := corev1.VolumeMount{Name: logVolume, MountPath: logPath}
	coreDumpsMount := corev1.VolumeMount{Name: coreDumpsVolume, MountPath: coreDumpsPath}

	withBlock := func() *memgraphcomv1alpha1.MemgraphCluster {
		cluster := minimalCluster()
		cluster.Spec.FixOwnershipInitContainer = &memgraphcomv1alpha1.FixOwnershipInitContainerSpec{}
		return cluster
	}

	t.Run("absent by default", func(t *testing.T) {
		for _, sts := range []*appsv1.StatefulSet{coordinatorStatefulSet(minimalCluster()), dataStatefulSet(minimalCluster())} {
			if got := sts.Spec.Template.Spec.InitContainers; len(got) != 0 {
				t.Errorf("%s init containers = %v, want none without the block", sts.Name, got)
			}
		}
	})

	t.Run("chowns the lib and log volumes of both roles", func(t *testing.T) {
		cluster := withBlock()

		want := []corev1.Container{expectedFixOwnershipInitContainer("101:103", libMount, logMount)}
		for _, sts := range []*appsv1.StatefulSet{coordinatorStatefulSet(cluster), dataStatefulSet(cluster)} {
			if diff := cmp.Diff(want, sts.Spec.Template.Spec.InitContainers); diff != "" {
				t.Errorf("%s init containers mismatch (-want +got):\n%s", sts.Name, diff)
			}
		}
	})

	// A role without a log claim has no log volume to mount, let alone chown.
	t.Run("skips the log volume a role opted out of", func(t *testing.T) {
		cluster := withBlock()
		cluster.Spec.Storage.Data.CreateLogStorageClaim = ptr.To(false)

		want := []corev1.Container{expectedFixOwnershipInitContainer("101:103", libMount)}
		if diff := cmp.Diff(want, dataStatefulSet(cluster).Spec.Template.Spec.InitContainers); diff != "" {
			t.Errorf("init containers mismatch (-want +got):\n%s", diff)
		}
	})

	// The core dumps volume is a claim like the others and needs the same fix;
	// the container runs last, after the two node-tuning ones, in the HA
	// chart's order.
	t.Run("chowns the core dumps volume and runs after the node-tuning containers", func(t *testing.T) {
		cluster := withBlock()
		cluster.Spec.SysctlInitContainer = &memgraphcomv1alpha1.SysctlInitContainerSpec{}
		cluster.Spec.CoreDumps.Data = &memgraphcomv1alpha1.RoleCoreDumpsSpec{}

		want := []corev1.Container{
			expectedSysctlInitContainer(memgraphcomv1alpha1.DefaultMaxMapCount),
			expectedCorePatternInitContainer(),
			expectedFixOwnershipInitContainer("101:103", libMount, logMount, coreDumpsMount),
		}
		if diff := cmp.Diff(want, dataStatefulSet(cluster).Spec.Template.Spec.InitContainers); diff != "" {
			t.Errorf("init containers mismatch (-want +got):\n%s", diff)
		}
	})
}

// TestStatefulSetSecurityContext covers the presence-based identity block:
// absent, the images' uid and gid on both roles; present, exactly the fields
// written and no other, so an empty block leaves all three to the platform;
// and the ownership container chowning to whatever the block resolved to.
func TestStatefulSetSecurityContext(t *testing.T) {
	libMount := corev1.VolumeMount{Name: libVolume, MountPath: libPath}
	logMount := corev1.VolumeMount{Name: logVolume, MountPath: logPath}

	// The two fields every policy requires are written whatever the block says.
	policyOnly := &corev1.PodSecurityContext{
		RunAsNonRoot:   ptr.To(true),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	podContext := func(sts *appsv1.StatefulSet) *corev1.PodSecurityContext {
		return sts.Spec.Template.Spec.SecurityContext
	}

	t.Run("absent writes the images' identity on both roles", func(t *testing.T) {
		for _, sts := range []*appsv1.StatefulSet{coordinatorStatefulSet(minimalCluster()), dataStatefulSet(minimalCluster())} {
			if diff := cmp.Diff(expectedPodSecurityContext(), podContext(sts)); diff != "" {
				t.Errorf("%s pod security context mismatch (-want +got):\n%s", sts.Name, diff)
			}
		}
	})

	t.Run("empty block leaves the identity to the platform", func(t *testing.T) {
		cluster := minimalCluster()
		cluster.Spec.SecurityContext = &memgraphcomv1alpha1.PodSecurityContextSpec{}

		for _, sts := range []*appsv1.StatefulSet{coordinatorStatefulSet(cluster), dataStatefulSet(cluster)} {
			if diff := cmp.Diff(policyOnly, podContext(sts)); diff != "" {
				t.Errorf("%s pod security context mismatch (-want +got):\n%s", sts.Name, diff)
			}
		}
	})

	t.Run("named fields land as written and no other", func(t *testing.T) {
		cluster := minimalCluster()
		cluster.Spec.SecurityContext = &memgraphcomv1alpha1.PodSecurityContextSpec{
			RunAsUser: ptr.To(int64(1000680000)),
			FSGroup:   ptr.To(int64(1000680000)),
		}

		want := policyOnly.DeepCopy()
		want.RunAsUser = ptr.To(int64(1000680000))
		want.FSGroup = ptr.To(int64(1000680000))
		if diff := cmp.Diff(want, podContext(dataStatefulSet(cluster))); diff != "" {
			t.Errorf("pod security context mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("the ownership container chowns to runAsUser and runAsGroup", func(t *testing.T) {
		cluster := minimalCluster()
		cluster.Spec.FixOwnershipInitContainer = &memgraphcomv1alpha1.FixOwnershipInitContainerSpec{}
		cluster.Spec.SecurityContext = &memgraphcomv1alpha1.PodSecurityContextSpec{
			RunAsUser:  ptr.To(int64(1000)),
			RunAsGroup: ptr.To(int64(2000)),
			FSGroup:    ptr.To(int64(3000)),
		}

		want := []corev1.Container{expectedFixOwnershipInitContainer("1000:2000", libMount, logMount)}
		if diff := cmp.Diff(want, dataStatefulSet(cluster).Spec.Template.Spec.InitContainers); diff != "" {
			t.Errorf("init containers mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("the ownership container falls back to fsGroup without a runAsGroup", func(t *testing.T) {
		cluster := minimalCluster()
		cluster.Spec.FixOwnershipInitContainer = &memgraphcomv1alpha1.FixOwnershipInitContainerSpec{}
		cluster.Spec.SecurityContext = &memgraphcomv1alpha1.PodSecurityContextSpec{
			RunAsUser: ptr.To(int64(1000)),
			FSGroup:   ptr.To(int64(3000)),
		}

		want := []corev1.Container{expectedFixOwnershipInitContainer("1000:3000", libMount, logMount)}
		if diff := cmp.Diff(want, dataStatefulSet(cluster).Spec.Template.Spec.InitContainers); diff != "" {
			t.Errorf("init containers mismatch (-want +got):\n%s", diff)
		}
	})

	// The CRD rejects this combination; a spec that never passed admission
	// gets no container rather than a chown to an identity nobody named.
	t.Run("no ownership container without a named identity", func(t *testing.T) {
		cluster := minimalCluster()
		cluster.Spec.FixOwnershipInitContainer = &memgraphcomv1alpha1.FixOwnershipInitContainerSpec{}
		cluster.Spec.SecurityContext = &memgraphcomv1alpha1.PodSecurityContextSpec{
			FSGroup: ptr.To(int64(3000)),
		}

		if got := dataStatefulSet(cluster).Spec.Template.Spec.InitContainers; len(got) != 0 {
			t.Errorf("init containers = %v, want none without a uid to chown to", got)
		}
	})
}

// TestStatefulSetCoreDumpsUploader pins the wiring the operator owns on behalf
// of the sidecar: a read-only view of the dumps, the path as CORE_DUMPS_DIR, a
// writable /tmp, credentials by Secret reference, and the same locked-down
// security context the Memgraph container runs under.
func TestStatefulSetCoreDumpsUploader(t *testing.T) {
	cluster := minimalCluster()
	// The uploader is declared once for the cluster; only the role that
	// collects dumps gets it.
	cluster.Spec.CoreDumps = memgraphcomv1alpha1.CoreDumpsSpec{
		Data: &memgraphcomv1alpha1.RoleCoreDumpsSpec{},
		Uploader: &memgraphcomv1alpha1.CoreDumpsUploaderSpec{
			Image:          uploaderImage,
			Command:        []string{shell, "-c"},
			Args:           []string{"upload-loop"},
			Env:            []memgraphcomv1alpha1.EnvVar{{Name: "S3_BUCKET", Value: "dumps"}},
			EnvFromSecrets: []string{"aws-s3-credentials"},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")},
			},
		},
	}

	containers := dataStatefulSet(cluster).Spec.Template.Spec.Containers
	if len(containers) != 2 {
		t.Fatalf("containers = %d, want Memgraph plus the uploader", len(containers))
	}
	if containers[0].Name != memgraphName {
		t.Errorf("first container = %q, want Memgraph to stay first", containers[0].Name)
	}

	want := corev1.Container{
		Name:            "core-dumps-uploader",
		Image:           uploaderImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{shell, "-c"},
		Args:            []string{"upload-loop"},
		Env: []corev1.EnvVar{
			{Name: "CORE_DUMPS_DIR", Value: coreDumpsPath},
			{Name: "S3_BUCKET", Value: "dumps"},
		},
		EnvFrom: []corev1.EnvFromSource{{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: "aws-s3-credentials"},
			},
		}},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: coreDumpsVolume, MountPath: coreDumpsPath, ReadOnly: true},
			{Name: tmpVolume, MountPath: "/tmp"},
		},
		SecurityContext: expectedContainerSecurityContext(),
	}
	if diff := cmp.Diff(want, containers[1]); diff != "" {
		t.Errorf("uploader sidecar mismatch (-want +got):\n%s", diff)
	}

	// Coordinators collect no dumps, so the shared uploader has nothing to read
	// in their pods and must not be injected there.
	coordinators := coordinatorStatefulSet(cluster).Spec.Template.Spec.Containers
	if len(coordinators) != 1 {
		t.Errorf("coordinator containers = %d, want only Memgraph's: the role collects no dumps",
			len(coordinators))
	}
}

// TestStatefulSetExtraVolumes covers the passthrough: the role's volumes join
// the pod after the operator's scratch volume, its mounts join the Memgraph
// container after the operator's, and the other role is untouched.
func TestStatefulSetExtraVolumes(t *testing.T) {
	certVolume := corev1.Volume{
		Name: "bolt-certs",
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: boltTLSSecretName},
		},
	}
	certMount := corev1.VolumeMount{Name: "bolt-certs", MountPath: boltTLSMount, ReadOnly: true}

	cluster := minimalCluster()
	cluster.Spec.ExtraVolumes = memgraphcomv1alpha1.ExtraVolumesSpec{
		Data: []corev1.Volume{certVolume},
	}
	cluster.Spec.ExtraVolumeMounts = memgraphcomv1alpha1.ExtraVolumeMountsSpec{
		Data: []corev1.VolumeMount{certMount},
	}

	t.Run(dataComponent, func(t *testing.T) {
		podSpec := dataStatefulSet(cluster).Spec.Template.Spec

		wantVolumes := append(expectedVolumes(), certVolume)
		if diff := cmp.Diff(wantVolumes, podSpec.Volumes); diff != "" {
			t.Errorf("volumes mismatch (-want +got):\n%s", diff)
		}
		wantMounts := append(expectedVolumeMounts(), certMount)
		if diff := cmp.Diff(wantMounts, podSpec.Containers[0].VolumeMounts); diff != "" {
			t.Errorf("volume mounts mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run(coordinatorComponent, func(t *testing.T) {
		podSpec := coordinatorStatefulSet(cluster).Spec.Template.Spec

		if diff := cmp.Diff(expectedVolumes(), podSpec.Volumes); diff != "" {
			t.Errorf("volumes mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(expectedVolumeMounts(), podSpec.Containers[0].VolumeMounts); diff != "" {
			t.Errorf("volume mounts mismatch (-want +got):\n%s", diff)
		}
	})
}

// A volume the role declares but never mounts is still a legitimate pod volume
// — the uploader sidecar or a future consumer may be its reader — so the
// builder passes it through rather than second-guessing it.
func TestStatefulSetExtraVolumeWithoutMount(t *testing.T) {
	cluster := minimalCluster()
	cluster.Spec.ExtraVolumes.Coordinators = []corev1.Volume{{
		Name:         "scratch",
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	}}

	podSpec := coordinatorStatefulSet(cluster).Spec.Template.Spec
	if len(podSpec.Volumes) != 2 {
		t.Errorf("volumes = %v, want the scratch volume alongside tmp", podSpec.Volumes)
	}
	if diff := cmp.Diff(expectedVolumeMounts(), podSpec.Containers[0].VolumeMounts); diff != "" {
		t.Errorf("volume mounts mismatch (-want +got):\n%s", diff)
	}
}

// TestStatefulSetUserContainers covers the passthrough: a role's containers
// join its pods after the operator's own and only its pods, a container naming
// no securityContext gets the restricted one, and one naming its own keeps it.
func TestStatefulSetUserContainers(t *testing.T) {
	debugger := corev1.Container{
		Name:    "my-debugger",
		Image:   "docker.io/library/busybox:1.37.0",
		Command: []string{"sh", "-c", "echo hi; sleep 10000"},
	}
	shipper := corev1.Container{
		Name:  "log-shipper",
		Image: "docker.io/fluent/fluent-bit:4.0.0",
		VolumeMounts: []corev1.VolumeMount{{
			Name: logVolume, MountPath: logPath, ReadOnly: true,
		}},
		SecurityContext: &corev1.SecurityContext{RunAsUser: ptr.To(int64(1000))},
	}

	cluster := minimalCluster()
	cluster.Spec.UserContainers = memgraphcomv1alpha1.UserContainersSpec{
		Data: []corev1.Container{debugger, shipper},
	}

	t.Run(dataComponent, func(t *testing.T) {
		containers := dataStatefulSet(cluster).Spec.Template.Spec.Containers

		lockedDebugger := debugger
		lockedDebugger.SecurityContext = expectedContainerSecurityContext()
		if len(containers) != 3 || containers[0].Name != memgraphName {
			t.Fatalf("containers = %v, want Memgraph first then the two user containers", containers)
		}
		if diff := cmp.Diff([]corev1.Container{lockedDebugger, shipper}, containers[1:]); diff != "" {
			t.Errorf("user containers mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run(coordinatorComponent, func(t *testing.T) {
		containers := coordinatorStatefulSet(cluster).Spec.Template.Spec.Containers
		if len(containers) != 1 {
			t.Errorf("containers = %v, want only Memgraph's on the role that declared none", containers)
		}
	})

	// The operator's own sidecar keeps its place ahead of the user's.
	t.Run("after the uploader", func(t *testing.T) {
		cluster := minimalCluster()
		cluster.Spec.CoreDumps = memgraphcomv1alpha1.CoreDumpsSpec{
			Data:     &memgraphcomv1alpha1.RoleCoreDumpsSpec{},
			Uploader: &memgraphcomv1alpha1.CoreDumpsUploaderSpec{Image: uploaderImage},
		}
		cluster.Spec.UserContainers.Data = []corev1.Container{debugger}

		containers := dataStatefulSet(cluster).Spec.Template.Spec.Containers
		names := make([]string, 0, len(containers))
		for _, c := range containers {
			names = append(names, c.Name)
		}
		if diff := cmp.Diff([]string{memgraphName, "core-dumps-uploader", "my-debugger"}, names); diff != "" {
			t.Errorf("container order mismatch (-want +got):\n%s", diff)
		}
	})
}

// TestStatefulSetInitContainers covers the passthrough of the user's init
// containers: a role's join its pods after the operator's own and only its
// pods, a container naming no securityContext gets the restricted one, and
// one naming its own keeps it.
func TestStatefulSetInitContainers(t *testing.T) {
	seeder := corev1.Container{
		Name:    "seed-modules",
		Image:   "docker.io/library/busybox:1.37.0",
		Command: []string{"sh", "-c", "echo hello world"},
	}
	fetcher := corev1.Container{
		Name:  "fetch-snapshot",
		Image: "docker.io/curlimages/curl:8.14.1",
		VolumeMounts: []corev1.VolumeMount{{
			Name: libVolume, MountPath: libPath,
		}},
		SecurityContext: &corev1.SecurityContext{RunAsUser: ptr.To(int64(0))},
	}

	cluster := minimalCluster()
	cluster.Spec.InitContainers = memgraphcomv1alpha1.InitContainersSpec{
		Data: []corev1.Container{seeder, fetcher},
	}

	t.Run(dataComponent, func(t *testing.T) {
		lockedSeeder := seeder
		lockedSeeder.SecurityContext = expectedContainerSecurityContext()
		want := []corev1.Container{lockedSeeder, fetcher}
		if diff := cmp.Diff(want, dataStatefulSet(cluster).Spec.Template.Spec.InitContainers); diff != "" {
			t.Errorf("init containers mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run(coordinatorComponent, func(t *testing.T) {
		if got := coordinatorStatefulSet(cluster).Spec.Template.Spec.InitContainers; len(got) != 0 {
			t.Errorf("init containers = %v, want none on the role that declared none", got)
		}
	})

	// The operator's own init containers keep their place ahead of the user's,
	// so a user container runs on volumes the ownership one already fixed.
	t.Run("after the operator's own", func(t *testing.T) {
		cluster := minimalCluster()
		cluster.Spec.SysctlInitContainer = &memgraphcomv1alpha1.SysctlInitContainerSpec{}
		cluster.Spec.CoreDumps.Data = &memgraphcomv1alpha1.RoleCoreDumpsSpec{}
		cluster.Spec.FixOwnershipInitContainer = &memgraphcomv1alpha1.FixOwnershipInitContainerSpec{}
		cluster.Spec.InitContainers.Data = []corev1.Container{seeder}

		containers := dataStatefulSet(cluster).Spec.Template.Spec.InitContainers
		names := make([]string, 0, len(containers))
		for _, c := range containers {
			names = append(names, c.Name)
		}
		want := []string{"init-sysctl", "init-core-pattern", "init-fix-perms", "seed-modules"}
		if diff := cmp.Diff(want, names); diff != "" {
			t.Errorf("init container order mismatch (-want +got):\n%s", diff)
		}
	})
}

// TestStatefulSetRetentionPolicy pins the mapping from the spec's retention
// policy onto the StatefulSet machinery that is the only deleter of this
// cluster's storage. Both whenDeleted and whenScaled follow it: the claim of a
// pod a scale-down removes is the same data as the claim of a pod a cluster
// deletion removes, so one knob answers for both.
func TestStatefulSetRetentionPolicy(t *testing.T) {
	tests := []struct {
		name     string
		policy   memgraphcomv1alpha1.StorageRetentionPolicy
		expected appsv1.PersistentVolumeClaimRetentionPolicyType
	}{
		{
			name:     "unset defaults to retain",
			policy:   "",
			expected: appsv1.RetainPersistentVolumeClaimRetentionPolicyType,
		},
		{
			name:     "retain",
			policy:   memgraphcomv1alpha1.RetentionPolicyRetain,
			expected: appsv1.RetainPersistentVolumeClaimRetentionPolicyType,
		},
		{
			name:     "delete",
			policy:   memgraphcomv1alpha1.RetentionPolicyDelete,
			expected: appsv1.DeletePersistentVolumeClaimRetentionPolicyType,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cluster := minimalCluster()
			cluster.Spec.Storage.RetentionPolicy = tc.policy

			want := expectedRetentionPolicy(tc.expected)
			for _, sts := range []*appsv1.StatefulSet{
				coordinatorStatefulSet(cluster),
				dataStatefulSet(cluster),
			} {
				got := sts.Spec.PersistentVolumeClaimRetentionPolicy
				if diff := cmp.Diff(want, got); diff != "" {
					t.Errorf("%s retention policy mismatch (-want +got):\n%s", sts.Name, diff)
				}
			}
		})
	}
}

// The coordinator start script derives the zero-based ID and advertised
// hostname at runtime, so the fixed coordinator port and configured cluster
// domain have to be baked into it.
var expectedTunedCoordinatorScript = fmt.Sprintf(`ordinal="${POD_NAME##*-}"
exec /usr/lib/memgraph/memgraph \
  --coordinator-id="$ordinal" \
  --coordinator-hostname="${POD_NAME}.example-coordinator.memgraph-test.svc.k8s.example.com" \
  --coordinator-port=%d \
  "$@"`, memgraphcomv1alpha1.CoordinatorPort)

// The remaining flags reach the wrapper as
// container arguments, which is what keeps a value with whitespace or shell
// metacharacters from being re-parsed by the shell. spec.extraArgs.coordinators
// comes last so it wins.
func expectedTunedCoordinatorArgs() []string {
	return expectedCoordinatorArgs(logFilePath, "--log-level=WARNING")
}

// TestStatefulSetFixedPortsAndClusterDomain pins the fixed ports and configured
// cluster domain everywhere they surface: container ports, Memgraph flags,
// probes, and the coordinator's advertised hostname.
func TestStatefulSetFixedPortsAndClusterDomain(t *testing.T) {
	cluster := tunedCluster()

	t.Run(coordinatorComponent, func(t *testing.T) {
		container := coordinatorStatefulSet(cluster).Spec.Template.Spec.Containers[0]

		wantPorts := []corev1.ContainerPort{
			{Name: boltPortName, ContainerPort: memgraphcomv1alpha1.BoltPort},
			{Name: managementPortName, ContainerPort: memgraphcomv1alpha1.ManagementPort},
			{Name: coordinatorComponent, ContainerPort: memgraphcomv1alpha1.CoordinatorPort},
			{Name: metricsPortName, ContainerPort: memgraphcomv1alpha1.MetricsPort},
		}
		if diff := cmp.Diff(wantPorts, container.Ports); diff != "" {
			t.Errorf("container ports mismatch (-want +got):\n%s", diff)
		}
		wantCommand := expectedCommand(expectedTunedCoordinatorScript)
		if diff := cmp.Diff(wantCommand, container.Command); diff != "" {
			t.Errorf("start script mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(expectedTunedCoordinatorArgs(), container.Args); diff != "" {
			t.Errorf("args mismatch (-want +got):\n%s", diff)
		}
		if got := container.ReadinessProbe.TCPSocket.Port; got != intstr.FromInt32(memgraphcomv1alpha1.CoordinatorPort) {
			t.Errorf("readiness probe dials %v, want the fixed coordinator port %d",
				got, memgraphcomv1alpha1.CoordinatorPort)
		}
	})

	t.Run(dataComponent, func(t *testing.T) {
		container := dataStatefulSet(cluster).Spec.Template.Spec.Containers[0]

		wantPorts := []corev1.ContainerPort{
			{Name: boltPortName, ContainerPort: memgraphcomv1alpha1.BoltPort},
			{Name: managementPortName, ContainerPort: memgraphcomv1alpha1.ManagementPort},
			{Name: replicationPortName, ContainerPort: memgraphcomv1alpha1.ReplicationPort},
			{Name: metricsPortName, ContainerPort: memgraphcomv1alpha1.MetricsPort},
		}
		if diff := cmp.Diff(wantPorts, container.Ports); diff != "" {
			t.Errorf("container ports mismatch (-want +got):\n%s", diff)
		}
		wantArgs := expectedArgs(logFilePath,
			"--storage-snapshot-on-exit=true", "--memory-limit=2048")
		if diff := cmp.Diff(wantArgs, container.Args); diff != "" {
			t.Errorf("args mismatch (-want +got):\n%s", diff)
		}
		if got := container.ReadinessProbe.TCPSocket.Port; got != intstr.FromInt32(memgraphcomv1alpha1.BoltPort) {
			t.Errorf("readiness probe dials %v, want the fixed bolt port %d",
				got, memgraphcomv1alpha1.BoltPort)
		}
	})
}

// TestStatefulSetExtraArgsAreNotShellParsed asserts an extra argument survives
// verbatim on both roles, whitespace and shell metacharacters included. The
// coordinators are the interesting half: they start through a /bin/sh wrapper,
// so an argument interpolated into that script would be word-split by the shell
// (or worse, run as a command) instead of reaching Memgraph as one flag.
func TestStatefulSetExtraArgsAreNotShellParsed(t *testing.T) {
	hostile := []string{
		"--query-modules-directory=/var/lib/memgraph/my modules",
		"--log-level=$(id)`id`;id",
		"--experimental-enabled=text-search,'vector-search'",
	}
	cluster := minimalCluster()
	cluster.Spec.ExtraArgs = memgraphcomv1alpha1.ExtraArgsSpec{Coordinators: hostile, Data: hostile}

	for _, tc := range []struct {
		name string
		sts  *appsv1.StatefulSet
	}{
		{coordinatorComponent, coordinatorStatefulSet(cluster)},
		{dataComponent, dataStatefulSet(cluster)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			container := tc.sts.Spec.Template.Spec.Containers[0]
			if got := container.Args[len(container.Args)-len(hostile):]; !slices.Equal(got, hostile) {
				t.Errorf("trailing args = %q, want the extra args unmodified %q", got, hostile)
			}
			for _, arg := range hostile {
				if strings.Contains(strings.Join(container.Command, "\x00"), arg) {
					t.Errorf("command %q embeds the extra arg %q, which the shell would then parse",
						container.Command, arg)
				}
			}
		})
	}
}

// TestStatefulSetProbeOverrides asserts the one readiness probe block reaches
// both roles, each on its own port, and that a partially specified block keeps
// the defaults for the timings it leaves out.
func TestStatefulSetProbeOverrides(t *testing.T) {
	cluster := tunedCluster()

	tests := []struct {
		name      string
		sts       *appsv1.StatefulSet
		readiness *corev1.Probe
	}{
		{
			name: coordinatorComponent,
			sts:  coordinatorStatefulSet(cluster),
			// Timings tightened, failure threshold left at its default.
			readiness: tunedTCPProbe(memgraphcomv1alpha1.CoordinatorPort, 20, 3, 2),
		},
		{
			name:      dataComponent,
			sts:       dataStatefulSet(cluster),
			readiness: tunedTCPProbe(memgraphcomv1alpha1.BoltPort, 20, 3, 2),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			container := tc.sts.Spec.Template.Spec.Containers[0]
			if diff := cmp.Diff(tc.readiness, container.ReadinessProbe); diff != "" {
				t.Errorf("readiness probe mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestStatefulSetCarriesNoLivenessOrStartupProbe pins the deliberate absence:
// a data instance opens no port until every database is recovered, so any
// liveness check would only ever kill a recovery that outlived a guessed
// budget, and a startup probe exists only to hold a liveness check off.
func TestStatefulSetCarriesNoLivenessOrStartupProbe(t *testing.T) {
	for _, tc := range []struct {
		name string
		sts  *appsv1.StatefulSet
	}{
		{coordinatorComponent, coordinatorStatefulSet(tunedCluster())},
		{dataComponent, dataStatefulSet(tunedCluster())},
	} {
		t.Run(tc.name, func(t *testing.T) {
			container := tc.sts.Spec.Template.Spec.Containers[0]
			if container.LivenessProbe != nil {
				t.Errorf("container carries a liveness probe: %+v", container.LivenessProbe)
			}
			if container.StartupProbe != nil {
				t.Errorf("container carries a startup probe: %+v", container.StartupProbe)
			}
			if container.ReadinessProbe == nil {
				t.Error("container carries no readiness probe")
			}
		})
	}
}

func TestStatefulSetResourceOverrides(t *testing.T) {
	cluster := tunedCluster()

	tests := []struct {
		name string
		sts  *appsv1.StatefulSet
		want corev1.ResourceRequirements
	}{
		{
			name: coordinatorComponent,
			sts:  coordinatorStatefulSet(cluster),
			want: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
				Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
			},
		},
		{
			name: dataComponent,
			sts:  dataStatefulSet(cluster),
			want: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("4Gi")},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.sts.Spec.Template.Spec.Containers[0].Resources
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("resources mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestStatefulSetLabelOverrides asserts custom labels land on the object they
// name: StatefulSet labels on the StatefulSet, pod labels on the pod template,
// and neither on the selector, which stays operator-owned.
func TestStatefulSetLabelOverrides(t *testing.T) {
	cluster := tunedCluster()

	tests := []struct {
		name      string
		sts       *appsv1.StatefulSet
		component string
		stsLabels map[string]string
		podLabels map[string]string
	}{
		{
			name:      coordinatorComponent,
			sts:       coordinatorStatefulSet(cluster),
			component: coordinatorComponent,
			stsLabels: map[string]string{tierLabel: "control"},
			podLabels: map[string]string{teamLabel: platformTeam},
		},
		{
			name:      dataComponent,
			sts:       dataStatefulSet(cluster),
			component: dataComponent,
			stsLabels: map[string]string{tierLabel: "storage"},
			podLabels: map[string]string{teamLabel: dataComponent},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(expectedLabelsWith(tc.component, tc.stsLabels), tc.sts.Labels); diff != "" {
				t.Errorf("StatefulSet labels mismatch (-want +got):\n%s", diff)
			}
			wantPodLabels := expectedLabelsWith(tc.component, tc.podLabels)
			if diff := cmp.Diff(wantPodLabels, tc.sts.Spec.Template.Labels); diff != "" {
				t.Errorf("pod labels mismatch (-want +got):\n%s", diff)
			}
			wantSelector := &metav1.LabelSelector{MatchLabels: expectedSelectorLabels(tc.component)}
			if diff := cmp.Diff(wantSelector, tc.sts.Spec.Selector); diff != "" {
				t.Errorf("selector mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// A custom label that collides with one of the operator's identity labels must
// lose: those labels are what the StatefulSet and Service select on, so a
// custom label winning would detach the pods from their cluster.
func TestStatefulSetCustomLabelsCannotOverrideIdentity(t *testing.T) {
	cluster := minimalCluster()
	hijack := map[string]string{
		nameLabel:      "not-memgraph",
		instanceLabel:  "other-cluster",
		componentLabel: dataComponent,
		managedByLabel: "someone-else",
		teamLabel:      platformTeam,
	}
	cluster.Spec.Labels.Coordinators = memgraphcomv1alpha1.RoleLabelsSpec{
		PodLabels:         hijack,
		StatefulSetLabels: hijack,
		ServiceLabels:     hijack,
	}

	want := expectedLabelsWith(coordinatorComponent, map[string]string{teamLabel: platformTeam})
	sts := coordinatorStatefulSet(cluster)
	for name, got := range map[string]map[string]string{
		statefulSetKind: sts.Labels,
		"pod":           sts.Spec.Template.Labels,
		serviceKind:     resources.CoordinatorHeadlessService(cluster).Labels,
	} {
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("%s labels mismatch (-want +got):\n%s", name, diff)
		}
	}
}

// TestStatefulSetExtraEnv asserts the passthrough environment lands after the
// license variables the operator wires from the secrets block, and that no
// secret material can ride along with it.
func TestStatefulSetExtraEnv(t *testing.T) {
	cluster := tunedCluster()

	tests := []struct {
		name string
		sts  *appsv1.StatefulSet
		want []corev1.EnvVar
	}{
		{
			name: coordinatorComponent,
			sts:  coordinatorStatefulSet(cluster),
			want: append(
				append([]corev1.EnvVar{{
					Name: "POD_NAME",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
					},
				}}, licenseEnv("memgraph-secrets", "MEMGRAPH_ENTERPRISE_LICENSE", "MEMGRAPH_ORGANIZATION_NAME")...),
				corev1.EnvVar{Name: "COORDINATOR_LABEL", Value: "coord"},
			),
		},
		{
			name: dataComponent,
			sts:  dataStatefulSet(cluster),
			want: append(
				licenseEnv("memgraph-secrets", "MEMGRAPH_ENTERPRISE_LICENSE", "MEMGRAPH_ORGANIZATION_NAME"),
				corev1.EnvVar{Name: "DATA_LABEL_ONE", Value: "one"},
				corev1.EnvVar{Name: "DATA_LABEL_TWO", Value: "two"},
			),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.sts.Spec.Template.Spec.Containers[0].Env
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("env mismatch (-want +got):\n%s", diff)
			}
			for _, variable := range got {
				if variable.ValueFrom != nil && variable.ValueFrom.SecretKeyRef != nil {
					if variable.Name != "MEMGRAPH_ENTERPRISE_LICENSE" && variable.Name != "MEMGRAPH_ORGANIZATION_NAME" {
						t.Errorf("env %q reads a Secret; only the secrets block may", variable.Name)
					}
				}
			}
		})
	}
}

// TestStatefulSetBoltTLS pins what spec.tls.bolt adds to both roles: the
// Secret mounted read-only under the HA chart's path with the two
// kubernetes.io/tls keys projected by name, and the two flags that turn Bolt —
// and with it the metrics endpoint — to TLS. The default-off case is pinned by
// the defaults tests, which expect exactly the volumes and mounts a plaintext
// cluster carries.
func TestStatefulSetBoltTLS(t *testing.T) {
	cluster := minimalCluster()
	cluster.Spec.TLS = &memgraphcomv1alpha1.TLSSpec{
		Bolt: &memgraphcomv1alpha1.BoltTLSSpec{SecretName: boltTLSSecretName},
	}

	wantVolumes := append(expectedVolumes(), corev1.Volume{
		Name: boltTLSVolume,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: boltTLSSecretName,
				Items: []corev1.KeyToPath{
					{Key: tlsCertKey, Path: tlsCertKey},
					{Key: tlsKeyKey, Path: tlsKeyKey},
				},
			},
		},
	})
	wantMounts := append(expectedVolumeMounts(),
		corev1.VolumeMount{Name: boltTLSVolume, MountPath: boltTLSMount, ReadOnly: true})
	tlsFlags := []string{
		"--bolt-cert-file=" + boltTLSMount + "/" + tlsCertKey,
		"--bolt-key-file=" + boltTLSMount + "/" + tlsKeyKey,
	}

	t.Run(dataComponent, func(t *testing.T) {
		podSpec := dataStatefulSet(cluster).Spec.Template.Spec
		if diff := cmp.Diff(wantVolumes, podSpec.Volumes); diff != "" {
			t.Errorf("volumes mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(wantMounts, podSpec.Containers[0].VolumeMounts); diff != "" {
			t.Errorf("volume mounts mismatch (-want +got):\n%s", diff)
		}
		wantArgs := expectedArgs(logFilePath, tlsFlags...)
		if diff := cmp.Diff(wantArgs, podSpec.Containers[0].Args); diff != "" {
			t.Errorf("args mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run(coordinatorComponent, func(t *testing.T) {
		podSpec := coordinatorStatefulSet(cluster).Spec.Template.Spec
		if diff := cmp.Diff(wantVolumes, podSpec.Volumes); diff != "" {
			t.Errorf("volumes mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(wantMounts, podSpec.Containers[0].VolumeMounts); diff != "" {
			t.Errorf("volume mounts mismatch (-want +got):\n%s", diff)
		}
		wantArgs := expectedCoordinatorArgs(logFilePath, tlsFlags...)
		if diff := cmp.Diff(wantArgs, podSpec.Containers[0].Args); diff != "" {
			t.Errorf("args mismatch (-want +got):\n%s", diff)
		}
	})
}

// TestStatefulSetBoltTLSAfterExtras pins the order the TLS flags and mount
// land in relative to a role's extras: the flags come before the role's extra
// args, so a user-supplied --bolt-cert-file still wins as the last occurrence,
// and the mount comes before the role's extra mounts.
func TestStatefulSetBoltTLSAfterExtras(t *testing.T) {
	cluster := minimalCluster()
	cluster.Spec.TLS = &memgraphcomv1alpha1.TLSSpec{
		Bolt: &memgraphcomv1alpha1.BoltTLSSpec{SecretName: boltTLSSecretName},
	}
	cluster.Spec.ExtraArgs.Data = []string{"--bolt-cert-file=/elsewhere/cert.pem"}

	args := dataStatefulSet(cluster).Spec.Template.Spec.Containers[0].Args
	certFlags := slices.DeleteFunc(slices.Clone(args), func(arg string) bool {
		return !strings.HasPrefix(arg, "--bolt-cert-file=")
	})
	want := []string{"--bolt-cert-file=" + boltTLSMount + "/" + tlsCertKey, "--bolt-cert-file=/elsewhere/cert.pem"}
	if diff := cmp.Diff(want, certFlags); diff != "" {
		t.Errorf("--bolt-cert-file order mismatch (-want +got):\n%s", diff)
	}
}

// TestStatefulSetIntraClusterTLS pins what spec.tls.intraCluster adds to both
// roles: the Secret mounted read-only under the HA chart's path with the two
// kubernetes.io/tls keys and the CA projected by name, and the three cluster
// flags, which Memgraph insists arrive together. It is independent of bolt:
// this fixture has no Bolt TLS, so none of its volume, mount or flags appear.
func TestStatefulSetIntraClusterTLS(t *testing.T) {
	cluster := minimalCluster()
	cluster.Spec.TLS = &memgraphcomv1alpha1.TLSSpec{
		IntraCluster: &memgraphcomv1alpha1.IntraClusterTLSSpec{SecretName: intraTLSSecretName},
	}

	wantVolumes := append(expectedVolumes(), corev1.Volume{
		Name: intraTLSVolume,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: intraTLSSecretName,
				Items: []corev1.KeyToPath{
					{Key: tlsCertKey, Path: tlsCertKey},
					{Key: tlsKeyKey, Path: tlsKeyKey},
					{Key: tlsCAKey, Path: tlsCAKey},
				},
			},
		},
	})
	wantMounts := append(expectedVolumeMounts(),
		corev1.VolumeMount{Name: intraTLSVolume, MountPath: intraTLSMount, ReadOnly: true})
	tlsFlags := []string{
		"--cluster-cert-file=" + intraTLSMount + "/" + tlsCertKey,
		"--cluster-key-file=" + intraTLSMount + "/" + tlsKeyKey,
		"--cluster-ca-file=" + intraTLSMount + "/" + tlsCAKey,
	}

	t.Run(dataComponent, func(t *testing.T) {
		podSpec := dataStatefulSet(cluster).Spec.Template.Spec
		if diff := cmp.Diff(wantVolumes, podSpec.Volumes); diff != "" {
			t.Errorf("volumes mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(wantMounts, podSpec.Containers[0].VolumeMounts); diff != "" {
			t.Errorf("volume mounts mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(expectedArgs(logFilePath, tlsFlags...), podSpec.Containers[0].Args); diff != "" {
			t.Errorf("args mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run(coordinatorComponent, func(t *testing.T) {
		podSpec := coordinatorStatefulSet(cluster).Spec.Template.Spec
		if diff := cmp.Diff(wantVolumes, podSpec.Volumes); diff != "" {
			t.Errorf("volumes mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(wantMounts, podSpec.Containers[0].VolumeMounts); diff != "" {
			t.Errorf("volume mounts mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(expectedCoordinatorArgs(logFilePath, tlsFlags...), podSpec.Containers[0].Args); diff != "" {
			t.Errorf("args mismatch (-want +got):\n%s", diff)
		}
	})
}

// TestStatefulSetBothTLSModes pins the order the two modes land in when a
// cluster has both: bolt first, intra-cluster second, in volumes, mounts and
// flags alike, so the two never shadow each other.
func TestStatefulSetBothTLSModes(t *testing.T) {
	cluster := minimalCluster()
	cluster.Spec.TLS = &memgraphcomv1alpha1.TLSSpec{
		Bolt:         &memgraphcomv1alpha1.BoltTLSSpec{SecretName: boltTLSSecretName},
		IntraCluster: &memgraphcomv1alpha1.IntraClusterTLSSpec{SecretName: intraTLSSecretName},
	}

	podSpec := dataStatefulSet(cluster).Spec.Template.Spec

	volumes := make([]string, 0, len(podSpec.Volumes))
	for _, volume := range podSpec.Volumes {
		volumes = append(volumes, volume.Name)
	}
	if diff := cmp.Diff([]string{tmpVolume, boltTLSVolume, intraTLSVolume}, volumes); diff != "" {
		t.Errorf("volume order mismatch (-want +got):\n%s", diff)
	}

	mounts := make([]string, 0, len(podSpec.Containers[0].VolumeMounts))
	for _, mount := range podSpec.Containers[0].VolumeMounts {
		mounts = append(mounts, mount.MountPath)
	}
	wantMounts := make([]string, 0, len(expectedVolumeMounts())+2)
	for _, mount := range expectedVolumeMounts() {
		wantMounts = append(wantMounts, mount.MountPath)
	}
	wantMounts = append(wantMounts, boltTLSMount, intraTLSMount)
	if diff := cmp.Diff(wantMounts, mounts); diff != "" {
		t.Errorf("mount order mismatch (-want +got):\n%s", diff)
	}

	wantArgs := expectedArgs(logFilePath,
		"--bolt-cert-file="+boltTLSMount+"/"+tlsCertKey,
		"--bolt-key-file="+boltTLSMount+"/"+tlsKeyKey,
		"--cluster-cert-file="+intraTLSMount+"/"+tlsCertKey,
		"--cluster-key-file="+intraTLSMount+"/"+tlsKeyKey,
		"--cluster-ca-file="+intraTLSMount+"/"+tlsCAKey,
	)
	if diff := cmp.Diff(wantArgs, podSpec.Containers[0].Args); diff != "" {
		t.Errorf("args mismatch (-want +got):\n%s", diff)
	}
}

// TestStatefulSetCarriesNoSchedulingByDefault pins that a cluster without a
// scheduling block leaves the whole surface unset: no affinity of any kind, no
// node selector, no tolerations, no spread constraints, no priority class. The
// operator's spread rule is presence-based like every other optional block,
// so the scheduler places these pods by free capacity alone.
func TestStatefulSetCarriesNoSchedulingByDefault(t *testing.T) {
	cluster := minimalCluster()
	for _, sts := range []*appsv1.StatefulSet{coordinatorStatefulSet(cluster), dataStatefulSet(cluster)} {
		t.Run(sts.Name, func(t *testing.T) {
			pod := sts.Spec.Template.Spec
			if pod.Affinity != nil {
				t.Errorf("affinity = %+v, want none", pod.Affinity)
			}
			if pod.NodeSelector != nil || pod.Tolerations != nil || pod.TopologySpreadConstraints != nil {
				t.Errorf("node selector %v, tolerations %v, spread constraints %v: want none",
					pod.NodeSelector, pod.Tolerations, pod.TopologySpreadConstraints)
			}
			if pod.PriorityClassName != "" {
				t.Errorf("priorityClassName = %q, want none", pod.PriorityClassName)
			}
		})
	}
}

// antiAffinityTerm is the operator's rule as it lands on a pod template: one
// term selecting on the operator's identity labels over the given topology.
func antiAffinityTerm(selector map[string]string, topologyKey string) corev1.PodAffinityTerm {
	return corev1.PodAffinityTerm{
		LabelSelector: &metav1.LabelSelector{MatchLabels: selector},
		TopologyKey:   topologyKey,
	}
}

// clusterScopeSelector is the identity label set without the role: what a
// scope cluster rule selects on, so every pod of the cluster repels every other.
func clusterScopeSelector() map[string]string {
	return map[string]string{nameLabel: "memgraph", instanceLabel: clusterName}
}

// TestStatefulSetPodAntiAffinity pins what each corner of the operator's rule
// lands on, on both roles: the empty block is the HA chart's default (a
// preferred term, weight 100, per role, over hostname), required with scope
// role is the chart's parity, required with scope cluster is its unique, and
// the topology key is passed through.
func TestStatefulSetPodAntiAffinity(t *testing.T) {
	const zoneKey = "topology.kubernetes.io/zone"
	tests := []struct {
		name string
		rule memgraphcomv1alpha1.PodAntiAffinitySpec
		want func(component string) *corev1.PodAntiAffinity
	}{
		{
			name: "empty block is preferred per role over hostname",
			rule: memgraphcomv1alpha1.PodAntiAffinitySpec{},
			want: func(component string) *corev1.PodAntiAffinity {
				return &corev1.PodAntiAffinity{
					PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
						Weight:          100,
						PodAffinityTerm: antiAffinityTerm(expectedSelectorLabels(component), "kubernetes.io/hostname"),
					}},
				}
			},
		},
		{
			name: "required per role is the chart's parity",
			rule: memgraphcomv1alpha1.PodAntiAffinitySpec{Type: memgraphcomv1alpha1.PodAntiAffinityRequired},
			want: func(component string) *corev1.PodAntiAffinity {
				return &corev1.PodAntiAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
						antiAffinityTerm(expectedSelectorLabels(component), "kubernetes.io/hostname"),
					},
				}
			},
		},
		{
			name: "required per cluster is the chart's unique",
			rule: memgraphcomv1alpha1.PodAntiAffinitySpec{
				Type:  memgraphcomv1alpha1.PodAntiAffinityRequired,
				Scope: memgraphcomv1alpha1.PodAntiAffinityScopeCluster,
			},
			want: func(string) *corev1.PodAntiAffinity {
				return &corev1.PodAntiAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
						antiAffinityTerm(clusterScopeSelector(), "kubernetes.io/hostname"),
					},
				}
			},
		},
		{
			name: "preferred per cluster over a zone label",
			rule: memgraphcomv1alpha1.PodAntiAffinitySpec{
				Scope:       memgraphcomv1alpha1.PodAntiAffinityScopeCluster,
				TopologyKey: zoneKey,
			},
			want: func(string) *corev1.PodAntiAffinity {
				return &corev1.PodAntiAffinity{
					PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
						Weight:          100,
						PodAffinityTerm: antiAffinityTerm(clusterScopeSelector(), zoneKey),
					}},
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cluster := minimalCluster()
			cluster.Spec.Scheduling.PodAntiAffinity = &tc.rule
			for component, sts := range map[string]*appsv1.StatefulSet{
				coordinatorComponent: coordinatorStatefulSet(cluster),
				dataComponent:        dataStatefulSet(cluster),
			} {
				want := &corev1.Affinity{PodAntiAffinity: tc.want(component)}
				if diff := cmp.Diff(want, sts.Spec.Template.Spec.Affinity); diff != "" {
					t.Errorf("%s affinity mismatch (-want +got):\n%s", component, diff)
				}
			}
		})
	}
}

// TestStatefulSetPodAntiAffinityAppendsRoleTerms pins the merge rule: a
// role's own anti-affinity is appended after the operator's term, list by
// list, and never replaces it — and without the operator's rule the role's
// own terms are the whole affinity.
func TestStatefulSetPodAntiAffinityAppendsRoleTerms(t *testing.T) {
	ownRequired := antiAffinityTerm(map[string]string{"app": "noisy-neighbour"}, "kubernetes.io/hostname")
	ownPreferred := corev1.WeightedPodAffinityTerm{
		Weight:          10,
		PodAffinityTerm: antiAffinityTerm(map[string]string{"app": "batch"}, "topology.kubernetes.io/zone"),
	}
	own := &corev1.PodAntiAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution:  []corev1.PodAffinityTerm{ownRequired},
		PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{ownPreferred},
	}
	operatorTerm := corev1.WeightedPodAffinityTerm{
		Weight:          100,
		PodAffinityTerm: antiAffinityTerm(expectedSelectorLabels(dataComponent), "kubernetes.io/hostname"),
	}

	t.Run("appended after the operator's rule", func(t *testing.T) {
		cluster := minimalCluster()
		cluster.Spec.Scheduling.PodAntiAffinity = &memgraphcomv1alpha1.PodAntiAffinitySpec{}
		cluster.Spec.Scheduling.Data.PodAntiAffinity = own

		want := &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution:  []corev1.PodAffinityTerm{ownRequired},
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{operatorTerm, ownPreferred},
		}}
		if diff := cmp.Diff(want, dataStatefulSet(cluster).Spec.Template.Spec.Affinity); diff != "" {
			t.Errorf("data affinity mismatch (-want +got):\n%s", diff)
		}
		// The coordinators asked for nothing of their own and get the rule alone.
		wantCoordinator := &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
				Weight:          100,
				PodAffinityTerm: antiAffinityTerm(expectedSelectorLabels(coordinatorComponent), "kubernetes.io/hostname"),
			}},
		}}
		if diff := cmp.Diff(wantCoordinator, coordinatorStatefulSet(cluster).Spec.Template.Spec.Affinity); diff != "" {
			t.Errorf("coordinator affinity mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("the whole affinity without the operator's rule", func(t *testing.T) {
		cluster := minimalCluster()
		cluster.Spec.Scheduling.Data.PodAntiAffinity = own

		want := &corev1.Affinity{PodAntiAffinity: own}
		if diff := cmp.Diff(want, dataStatefulSet(cluster).Spec.Template.Spec.Affinity); diff != "" {
			t.Errorf("data affinity mismatch (-want +got):\n%s", diff)
		}
		if got := coordinatorStatefulSet(cluster).Spec.Template.Spec.Affinity; got != nil {
			t.Errorf("coordinator affinity = %+v, want none", got)
		}
	})
}

// nodeRoleLabel is the node label the HA chart's nodeSelection mode keyed on.
const nodeRoleLabel = "role"

// TestStatefulSetSchedulingPassthrough pins that the per-role node selector,
// tolerations and priority class land on that role's pod template verbatim
// and on no other, and that a spread constraint naming no labelSelector is
// given the role's own pod selector while one naming its own keeps it.
func TestStatefulSetSchedulingPassthrough(t *testing.T) {
	const zoneKey = "topology.kubernetes.io/zone"
	coordinatorToleration := corev1.Toleration{
		Key: "memgraph", Operator: corev1.TolerationOpEqual, Value: "coordinator", Effect: corev1.TaintEffectNoSchedule,
	}
	ownSelector := &metav1.LabelSelector{MatchLabels: map[string]string{"team": platformTeam}}

	cluster := minimalCluster()
	cluster.Spec.Scheduling.Coordinators = memgraphcomv1alpha1.RoleSchedulingSpec{
		NodeSelector:      map[string]string{nodeRoleLabel: "coordinator-node"},
		Tolerations:       []corev1.Toleration{coordinatorToleration},
		PriorityClassName: "system-cluster-critical",
		TopologySpreadConstraints: []corev1.TopologySpreadConstraint{
			{MaxSkew: 1, TopologyKey: zoneKey, WhenUnsatisfiable: corev1.DoNotSchedule},
		},
	}
	cluster.Spec.Scheduling.Data = memgraphcomv1alpha1.RoleSchedulingSpec{
		NodeSelector: map[string]string{nodeRoleLabel: "data-node"},
		TopologySpreadConstraints: []corev1.TopologySpreadConstraint{
			{MaxSkew: 2, TopologyKey: zoneKey, WhenUnsatisfiable: corev1.ScheduleAnyway, LabelSelector: ownSelector},
		},
	}

	t.Run(coordinatorComponent, func(t *testing.T) {
		pod := coordinatorStatefulSet(cluster).Spec.Template.Spec
		if diff := cmp.Diff(map[string]string{nodeRoleLabel: "coordinator-node"}, pod.NodeSelector); diff != "" {
			t.Errorf("node selector mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff([]corev1.Toleration{coordinatorToleration}, pod.Tolerations); diff != "" {
			t.Errorf("tolerations mismatch (-want +got):\n%s", diff)
		}
		if pod.PriorityClassName != "system-cluster-critical" {
			t.Errorf("priorityClassName = %q, want system-cluster-critical", pod.PriorityClassName)
		}
		wantSpread := []corev1.TopologySpreadConstraint{{
			MaxSkew:           1,
			TopologyKey:       zoneKey,
			WhenUnsatisfiable: corev1.DoNotSchedule,
			LabelSelector:     &metav1.LabelSelector{MatchLabels: expectedSelectorLabels(coordinatorComponent)},
		}}
		if diff := cmp.Diff(wantSpread, pod.TopologySpreadConstraints); diff != "" {
			t.Errorf("spread constraints mismatch (-want +got):\n%s", diff)
		}
		if pod.Affinity != nil {
			t.Errorf("affinity = %+v, want none: the passthrough fields write no anti-affinity", pod.Affinity)
		}
	})

	t.Run(dataComponent, func(t *testing.T) {
		pod := dataStatefulSet(cluster).Spec.Template.Spec
		if diff := cmp.Diff(map[string]string{nodeRoleLabel: "data-node"}, pod.NodeSelector); diff != "" {
			t.Errorf("node selector mismatch (-want +got):\n%s", diff)
		}
		if pod.Tolerations != nil || pod.PriorityClassName != "" {
			t.Errorf("tolerations %v, priorityClassName %q: the coordinators' settings must not leak",
				pod.Tolerations, pod.PriorityClassName)
		}
		wantSpread := []corev1.TopologySpreadConstraint{{
			MaxSkew:           2,
			TopologyKey:       zoneKey,
			WhenUnsatisfiable: corev1.ScheduleAnyway,
			LabelSelector:     ownSelector,
		}}
		if diff := cmp.Diff(wantSpread, pod.TopologySpreadConstraints); diff != "" {
			t.Errorf("spread constraints mismatch (-want +got):\n%s", diff)
		}
	})
}
