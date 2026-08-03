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

	statefulSetKind = "StatefulSet"
	serviceKind     = "Service"

	tmpVolume       = "tmp"
	shell           = "/bin/sh"
	defaultImageRef = "docker.io/memgraph/memgraph:3.12.0-relwithdebinfo"
	coreDumpsVolume = "core-dumps"
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

// Non-default ports and cluster domain shared by the tuning tests. Every one
// differs from its default, so a knob that fails to propagate cannot hide
// behind a value that happened to be right anyway.
const (
	customBoltPort        int32 = 7777
	customManagementPort  int32 = 10001
	customReplicationPort int32 = 20001
	customCoordinatorPort int32 = 12001

	customClusterDomain = "k8s.example.com"
)

// tunedCluster returns a MemgraphCluster with every pod-tuning knob set away
// from its default, so the golden tests can pin what each one lands on.
func tunedCluster() *memgraphcomv1alpha1.MemgraphCluster {
	cluster := minimalCluster()
	cluster.Spec.ClusterDomain = customClusterDomain
	cluster.Spec.Ports = memgraphcomv1alpha1.PortsSpec{
		BoltPort:        ptr.To(customBoltPort),
		ManagementPort:  ptr.To(customManagementPort),
		ReplicationPort: ptr.To(customReplicationPort),
		CoordinatorPort: ptr.To(customCoordinatorPort),
	}
	cluster.Spec.Probes = memgraphcomv1alpha1.ProbesSpec{
		Coordinators: memgraphcomv1alpha1.RoleProbesSpec{
			StartupProbe: memgraphcomv1alpha1.ProbeSpec{FailureThreshold: ptr.To(int32(30))},
			ReadinessProbe: memgraphcomv1alpha1.ProbeSpec{
				TimeoutSeconds: ptr.To(int32(3)),
				PeriodSeconds:  ptr.To(int32(2)),
			},
		},
		Data: memgraphcomv1alpha1.RoleProbesSpec{
			StartupProbe: memgraphcomv1alpha1.ProbeSpec{
				FailureThreshold: ptr.To(int32(4320)),
				TimeoutSeconds:   ptr.To(int32(15)),
				PeriodSeconds:    ptr.To(int32(10)),
			},
			LivenessProbe: memgraphcomv1alpha1.ProbeSpec{FailureThreshold: ptr.To(int32(6))},
		},
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

// tcpProbe is a probe with the default timings, of which only the failure
// threshold differs between probes.
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

func expectedVolumeMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{Name: "lib-storage", MountPath: "/var/lib/memgraph"},
		{Name: "log-storage", MountPath: "/var/log/memgraph"},
		{Name: tmpVolume, MountPath: "/tmp"},
	}
}

// expectedVolumeMountsWithoutLog is the mount set of a role that opted out of
// log storage: everything except the log volume.
func expectedVolumeMountsWithoutLog() []corev1.VolumeMount {
	return slices.DeleteFunc(expectedVolumeMounts(), func(mount corev1.VolumeMount) bool {
		return mount.Name == "log-storage"
	})
}

// expectedCommand wraps a coordinator start script the way the builder does.
func expectedCommand(script string) []string {
	return []string{shell, "-ec", script}
}

// expectedArgs are the flags a role is started with: the shared ones in the
// order the builder emits them, then the fixture's extra args. The ports vary
// per fixture and a role that opted out of log storage gets an empty
// --log-file, so both are parameters.
func expectedArgs(boltPort, managementPort int32, logDestination string, extra ...string) []string {
	return append([]string{
		fmt.Sprintf("--bolt-port=%d", boltPort),
		fmt.Sprintf("--management-port=%d", managementPort),
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
func expectedCoordinatorArgs(boltPort, managementPort int32, logDestination string, extra ...string) []string {
	return append([]string{memgraphName}, expectedArgs(boltPort, managementPort, logDestination, extra...)...)
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
		expectedClaimTemplate("log-storage", "1Gi", corev1.ReadWriteOnce, nil),
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

const expectedCoordinatorScript = `ordinal="${POD_NAME##*-}"
exec /usr/lib/memgraph/memgraph \
  --coordinator-id="$((ordinal + 1))" \
  --coordinator-hostname="${POD_NAME}.example-coordinator.memgraph-test.svc.cluster.local" \
  --coordinator-port=12000 \
  "$@"`

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
						Args:            expectedCoordinatorArgs(7687, 10000, logFilePath),
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
						Args:            expectedArgs(7687, 10000, logFilePath),
						Env:             licenseEnv("memgraph-secrets", "MEMGRAPH_ENTERPRISE_LICENSE", "MEMGRAPH_ORGANIZATION_NAME"),
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
				expectedClaimTemplate("log-storage", "512Mi", corev1.ReadWriteOnce, ptr.To("")),
			},
		},
		{
			name: dataComponent,
			sts:  dataStatefulSet(cluster),
			want: []corev1.PersistentVolumeClaim{
				expectedClaimTemplate("lib-storage", "100Gi", corev1.ReadWriteOnce, ptr.To("gp3")),
				// Untouched by the spec, so it keeps every schema default.
				expectedClaimTemplate("log-storage", "1Gi", corev1.ReadWriteOnce, nil),
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
		wantArgs := expectedCoordinatorArgs(7687, 10000, "")
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

// TestStatefulSetCoreDumps covers the enabled path end to end for one role
// while the other stays untouched: the claim, the Memgraph container's mount,
// and the privileged init container that points the node's kernel at it.
func TestStatefulSetCoreDumps(t *testing.T) {
	cluster := minimalCluster()
	cluster.Spec.CoreDumps = memgraphcomv1alpha1.CoreDumpsSpec{
		Data: memgraphcomv1alpha1.RoleCoreDumpsSpec{
			Enabled: true,
			Size:    ptr.To(resource.MustParse("20Gi")),
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
		wantInit := []corev1.Container{{
			Name:            "init-core-pattern",
			Image:           defaultImageRef,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Command: expectedCommand(
				"echo '/var/core/memgraph/core.%e.%p.%t.%s' | tee /proc/sys/kernel/core_pattern"),
			SecurityContext: &corev1.SecurityContext{
				Privileged:               ptr.To(true),
				AllowPrivilegeEscalation: ptr.To(true),
				ReadOnlyRootFilesystem:   ptr.To(true),
				RunAsUser:                ptr.To(int64(0)),
				RunAsNonRoot:             ptr.To(false),
				SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
		}}
		if diff := cmp.Diff(wantInit, podSpec.InitContainers); diff != "" {
			t.Errorf("init containers mismatch (-want +got):\n%s", diff)
		}
		if len(podSpec.Containers) != 1 {
			t.Errorf("containers = %d, want only Memgraph's without an uploader", len(podSpec.Containers))
		}
	})

	// The knob is per role: coordinators asked for nothing and get nothing.
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
		Data:                 memgraphcomv1alpha1.RoleCoreDumpsSpec{Enabled: true},
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

// TestStatefulSetCoreDumpsUploader pins the wiring the operator owns on behalf
// of the sidecar: a read-only view of the dumps, the path as CORE_DUMPS_DIR, a
// writable /tmp, credentials by Secret reference, and the same locked-down
// security context the Memgraph container runs under.
func TestStatefulSetCoreDumpsUploader(t *testing.T) {
	cluster := minimalCluster()
	// The uploader is declared once for the cluster; only the role that
	// collects dumps gets it.
	cluster.Spec.CoreDumps = memgraphcomv1alpha1.CoreDumpsSpec{
		Data: memgraphcomv1alpha1.RoleCoreDumpsSpec{Enabled: true},
		Uploader: &memgraphcomv1alpha1.CoreDumpsUploaderSpec{
			Image:          "amazon/aws-cli:2.33.28",
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
		Image:           "amazon/aws-cli:2.33.28",
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
			Secret: &corev1.SecretVolumeSource{SecretName: "bolt-tls"},
		},
	}
	certMount := corev1.VolumeMount{Name: "bolt-certs", MountPath: "/etc/memgraph/ssl", ReadOnly: true}

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

// The coordinator start script derives per-pod identity at runtime, so the
// configured coordinator port and cluster domain have to be baked into it —
// this is the same identity the operator registers with the cluster.
const expectedTunedCoordinatorScript = `ordinal="${POD_NAME##*-}"
exec /usr/lib/memgraph/memgraph \
  --coordinator-id="$((ordinal + 1))" \
  --coordinator-hostname="${POD_NAME}.example-coordinator.memgraph-test.svc.k8s.example.com" \
  --coordinator-port=12001 \
  "$@"`

// The remaining flags — the configured ports among them — reach the wrapper as
// container arguments, which is what keeps a value with whitespace or shell
// metacharacters from being re-parsed by the shell. spec.extraArgs.coordinators
// comes last so it wins.
func expectedTunedCoordinatorArgs() []string {
	return expectedCoordinatorArgs(customBoltPort, customManagementPort, logFilePath, "--log-level=WARNING")
}

// TestStatefulSetPortsAndClusterDomain pins every place a configured port or
// cluster domain has to surface: the container ports, the flags Memgraph is
// started with, the ports the probes dial, and the coordinator's advertised
// hostname.
func TestStatefulSetPortsAndClusterDomain(t *testing.T) {
	cluster := tunedCluster()

	t.Run(coordinatorComponent, func(t *testing.T) {
		container := coordinatorStatefulSet(cluster).Spec.Template.Spec.Containers[0]

		wantPorts := []corev1.ContainerPort{
			{Name: boltPortName, ContainerPort: customBoltPort},
			{Name: managementPortName, ContainerPort: customManagementPort},
			{Name: coordinatorComponent, ContainerPort: customCoordinatorPort},
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
		for name, probe := range map[string]*corev1.Probe{
			"startup":   container.StartupProbe,
			"readiness": container.ReadinessProbe,
			"liveness":  container.LivenessProbe,
		} {
			if got := probe.TCPSocket.Port; got != intstr.FromInt32(customCoordinatorPort) {
				t.Errorf("%s probe dials %v, want the configured coordinator port %d",
					name, got, customCoordinatorPort)
			}
		}
	})

	t.Run(dataComponent, func(t *testing.T) {
		container := dataStatefulSet(cluster).Spec.Template.Spec.Containers[0]

		wantPorts := []corev1.ContainerPort{
			{Name: boltPortName, ContainerPort: customBoltPort},
			{Name: managementPortName, ContainerPort: customManagementPort},
			{Name: replicationPortName, ContainerPort: customReplicationPort},
		}
		if diff := cmp.Diff(wantPorts, container.Ports); diff != "" {
			t.Errorf("container ports mismatch (-want +got):\n%s", diff)
		}
		wantArgs := expectedArgs(customBoltPort, customManagementPort, logFilePath,
			"--storage-snapshot-on-exit=true", "--memory-limit=2048")
		if diff := cmp.Diff(wantArgs, container.Args); diff != "" {
			t.Errorf("args mismatch (-want +got):\n%s", diff)
		}
		for name, probe := range map[string]*corev1.Probe{
			"startup":   container.StartupProbe,
			"readiness": container.ReadinessProbe,
			"liveness":  container.LivenessProbe,
		} {
			if got := probe.TCPSocket.Port; got != intstr.FromInt32(customBoltPort) {
				t.Errorf("%s probe dials %v, want the configured bolt port %d", name, got, customBoltPort)
			}
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

// TestStatefulSetProbeOverrides asserts probe timings are per role and per
// probe, and that a partially specified probe keeps the defaults for the
// timings it leaves out — including the data instances' 2h startup budget.
func TestStatefulSetProbeOverrides(t *testing.T) {
	cluster := tunedCluster()

	tests := []struct {
		name                         string
		sts                          *appsv1.StatefulSet
		startup, readiness, liveness *corev1.Probe
	}{
		{
			name: coordinatorComponent,
			sts:  coordinatorStatefulSet(cluster),
			// Only the failure threshold was raised, so the timings default.
			startup: tunedTCPProbe(customCoordinatorPort, 30, 10, 5),
			// Timings tightened, failure threshold left at its default.
			readiness: tunedTCPProbe(customCoordinatorPort, 20, 3, 2),
			liveness:  tunedTCPProbe(customCoordinatorPort, 20, 10, 5),
		},
		{
			name:      dataComponent,
			sts:       dataStatefulSet(cluster),
			startup:   tunedTCPProbe(customBoltPort, 4320, 15, 10),
			readiness: tunedTCPProbe(customBoltPort, 20, 10, 5),
			liveness:  tunedTCPProbe(customBoltPort, 6, 10, 5),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			container := tc.sts.Spec.Template.Spec.Containers[0]
			if diff := cmp.Diff(tc.startup, container.StartupProbe); diff != "" {
				t.Errorf("startup probe mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.readiness, container.ReadinessProbe); diff != "" {
				t.Errorf("readiness probe mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.liveness, container.LivenessProbe); diff != "" {
				t.Errorf("liveness probe mismatch (-want +got):\n%s", diff)
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
