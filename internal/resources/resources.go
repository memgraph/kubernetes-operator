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

// Package resources contains pure builders from a MemgraphCluster spec to the
// desired Kubernetes objects. Builders make no API calls and have no side
// effects; the controller server-side-applies their output.
package resources

import (
	"maps"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
)

const (
	// Memgraph workload pods run as the non-root memgraph user baked into the
	// official images.
	memgraphUserID  int64 = 101
	memgraphGroupID int64 = 103

	coordinatorComponent = "coordinator"
	dataComponent        = "data"
)

// Named container and Service port names shared by both roles.
const (
	boltPortName        = "bolt"
	managementPortName  = "management"
	coordinatorPortName = "coordinator"
	replicationPortName = "replication"
)

// CoordinatorName is the name shared by the coordinator StatefulSet and its
// headless Service.
func CoordinatorName(cluster *memgraphcomv1alpha1.MemgraphCluster) string {
	return cluster.Name + "-" + coordinatorComponent
}

// DataName is the name shared by the data-instance StatefulSet and its
// headless Service.
func DataName(cluster *memgraphcomv1alpha1.MemgraphCluster) string {
	return cluster.Name + "-" + dataComponent
}

// labels returns the full label set stamped on all objects of a role, with the
// role's custom labels merged underneath: the operator's own identity labels
// always win a key collision, so a custom label can never detach an object
// from its cluster.
func labels(
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	component string,
	custom map[string]string,
) map[string]string {
	l := make(map[string]string, len(custom)+4)
	maps.Copy(l, custom)
	maps.Copy(l, selectorLabels(cluster, component))
	l["app.kubernetes.io/managed-by"] = "memgraph-operator"
	return l
}

// selectorLabels returns the immutable subset of labels used as StatefulSet
// and Service selectors.
func selectorLabels(cluster *memgraphcomv1alpha1.MemgraphCluster, component string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":      "memgraph",
		"app.kubernetes.io/instance":  cluster.Name,
		"app.kubernetes.io/component": component,
	}
}

// normalizedSpec is a MemgraphClusterSpec with every optional field resolved
// to its default, so builders behave correctly on specs that never passed
// admission. Most defaults are CRD schema defaults mirrored as Go constants;
// the probe failure thresholds are Go-only, because they depend on the role.
type normalizedSpec struct {
	coordinators    int32
	dataInstances   int32
	image           string
	pullPolicy      corev1.PullPolicy
	secretName      string
	licenseKey      string
	organizationKey string
	clusterDomain   string
	ports           normalizedPorts
	retentionPolicy memgraphcomv1alpha1.StorageRetentionPolicy
	coordinatorRole normalizedRole
	dataRole        normalizedRole
}

// normalizedPorts are the internal ports every advertised address, container
// port and Service port is built from.
type normalizedPorts struct {
	bolt        int32
	management  int32
	replication int32
	coordinator int32
}

// normalizedRole is everything the builders need that is configured per role.
type normalizedRole struct {
	storage           normalizedStorage
	startupProbe      normalizedProbe
	readinessProbe    normalizedProbe
	livenessProbe     normalizedProbe
	resources         corev1.ResourceRequirements
	podLabels         map[string]string
	statefulSetLabels map[string]string
	serviceLabels     map[string]string
	env               []corev1.EnvVar
	extraArgs         []string
}

// normalizedProbe is one probe's timings; the probe type is always a TCP-socket
// check against the role's own port.
type normalizedProbe struct {
	failureThreshold int32
	timeoutSeconds   int32
	periodSeconds    int32
}

// normalizedStorage is one role's lib and log claim configuration with every
// optional field resolved to its CRD schema default. A nil storage class means
// "use the cluster default" and is passed through as nil, which is distinct
// from the empty string (no dynamic provisioning).
type normalizedStorage struct {
	libSize       resource.Quantity
	libAccessMode corev1.PersistentVolumeAccessMode
	libClass      *string
	logSize       resource.Quantity
	logAccessMode corev1.PersistentVolumeAccessMode
	logClass      *string
}

func normalize(spec memgraphcomv1alpha1.MemgraphClusterSpec) normalizedSpec {
	n := normalizedSpec{
		coordinators:    memgraphcomv1alpha1.DefaultCoordinatorCount,
		dataInstances:   memgraphcomv1alpha1.DefaultDataInstanceCount,
		image:           imageRef(spec.Image),
		pullPolicy:      spec.Image.PullPolicy,
		secretName:      spec.Secrets.Name,
		licenseKey:      spec.Secrets.LicenseKey,
		organizationKey: spec.Secrets.OrganizationKey,
		clusterDomain:   spec.ClusterDomain,
		ports:           normalizePorts(spec.Ports),
		retentionPolicy: spec.Storage.RetentionPolicy,
		coordinatorRole: normalizeRole(roleSpec{
			storage:   spec.Storage.Coordinators,
			probes:    spec.Probes.Coordinators,
			resources: spec.Resources.Coordinators,
			labels:    spec.Labels.Coordinators,
			env:       spec.ExtraEnv.Coordinators,
			extraArgs: spec.ExtraArgs.Coordinators,

			startupFailureThreshold: memgraphcomv1alpha1.DefaultProbeFailureThreshold,
		}),
		dataRole: normalizeRole(roleSpec{
			storage:   spec.Storage.Data,
			probes:    spec.Probes.Data,
			resources: spec.Resources.Data,
			labels:    spec.Labels.Data,
			env:       spec.ExtraEnv.Data,
			extraArgs: spec.ExtraArgs.Data,

			// Data instances get the long startup budget: only they load
			// snapshots, and a large restore must not be killed mid-load.
			startupFailureThreshold: memgraphcomv1alpha1.DefaultDataStartupProbeFailureThreshold,
		}),
	}
	if spec.Coordinators != nil {
		n.coordinators = *spec.Coordinators
	}
	if spec.DataInstances != nil {
		n.dataInstances = *spec.DataInstances
	}
	if n.clusterDomain == "" {
		n.clusterDomain = memgraphcomv1alpha1.DefaultClusterDomain
	}
	if n.pullPolicy == "" {
		n.pullPolicy = memgraphcomv1alpha1.DefaultImagePullPolicy
	}
	if n.secretName == "" {
		n.secretName = memgraphcomv1alpha1.DefaultSecretName
	}
	if n.licenseKey == "" {
		n.licenseKey = memgraphcomv1alpha1.DefaultLicenseSecretKey
	}
	if n.organizationKey == "" {
		n.organizationKey = memgraphcomv1alpha1.DefaultOrganizationSecretKey
	}
	if n.retentionPolicy == "" {
		n.retentionPolicy = memgraphcomv1alpha1.DefaultStorageRetention
	}
	return n
}

// roleSpec gathers the per-role pieces the spec's concern-first blocks
// (storage, probes, resources, labels, extraEnv, extraArgs) scatter across the
// CR, so normalization is written once and both roles resolve their defaults
// the same way.
type roleSpec struct {
	storage   memgraphcomv1alpha1.RoleStorageSpec
	probes    memgraphcomv1alpha1.RoleProbesSpec
	resources corev1.ResourceRequirements
	labels    memgraphcomv1alpha1.RoleLabelsSpec
	env       []memgraphcomv1alpha1.EnvVar
	extraArgs []string

	// startupFailureThreshold is this role's default startup probe failure
	// budget — the one default that differs between the roles.
	startupFailureThreshold int32
}

func normalizeRole(role roleSpec) normalizedRole {
	return normalizedRole{
		storage:           normalizeStorage(role.storage),
		startupProbe:      normalizeProbe(role.probes.StartupProbe, role.startupFailureThreshold),
		readinessProbe:    normalizeProbe(role.probes.ReadinessProbe, memgraphcomv1alpha1.DefaultProbeFailureThreshold),
		livenessProbe:     normalizeProbe(role.probes.LivenessProbe, memgraphcomv1alpha1.DefaultProbeFailureThreshold),
		resources:         role.resources,
		podLabels:         role.labels.PodLabels,
		statefulSetLabels: role.labels.StatefulSetLabels,
		serviceLabels:     role.labels.ServiceLabels,
		env:               normalizeEnv(role.env),
		extraArgs:         role.extraArgs,
	}
}

// normalizePorts resolves the internal ports, whose defaults mirror the
// memgraph-high-availability Helm chart's.
func normalizePorts(spec memgraphcomv1alpha1.PortsSpec) normalizedPorts {
	return normalizedPorts{
		bolt:        intOrDefault(spec.BoltPort, memgraphcomv1alpha1.DefaultBoltPort),
		management:  intOrDefault(spec.ManagementPort, memgraphcomv1alpha1.DefaultManagementPort),
		replication: intOrDefault(spec.ReplicationPort, memgraphcomv1alpha1.DefaultReplicationPort),
		coordinator: intOrDefault(spec.CoordinatorPort, memgraphcomv1alpha1.DefaultCoordinatorPort),
	}
}

// intOrDefault resolves an optional numeric knob against its default.
func intOrDefault(configured *int32, fallback int32) int32 {
	if configured == nil {
		return fallback
	}
	return *configured
}

// normalizeProbe resolves one probe's timings. Only the failure threshold's
// default depends on the role — the probe that guards a snapshot restore needs
// a far larger budget than the rest.
func normalizeProbe(spec memgraphcomv1alpha1.ProbeSpec, defaultFailureThreshold int32) normalizedProbe {
	return normalizedProbe{
		failureThreshold: intOrDefault(spec.FailureThreshold, defaultFailureThreshold),
		timeoutSeconds:   intOrDefault(spec.TimeoutSeconds, memgraphcomv1alpha1.DefaultProbeTimeoutSeconds),
		periodSeconds:    intOrDefault(spec.PeriodSeconds, memgraphcomv1alpha1.DefaultProbePeriodSeconds),
	}
}

// normalizeEnv converts the spec's non-secret name/value pairs into container
// environment variables. Nothing is defaulted: an unset list means no extra
// environment.
func normalizeEnv(spec []memgraphcomv1alpha1.EnvVar) []corev1.EnvVar {
	if len(spec) == 0 {
		return nil
	}
	env := make([]corev1.EnvVar, 0, len(spec))
	for _, variable := range spec {
		env = append(env, corev1.EnvVar{Name: variable.Name, Value: variable.Value})
	}
	return env
}

func normalizeStorage(spec memgraphcomv1alpha1.RoleStorageSpec) normalizedStorage {
	n := normalizedStorage{
		libSize:       resource.MustParse(memgraphcomv1alpha1.DefaultLibPVCSize),
		libAccessMode: spec.LibStorageAccessMode,
		libClass:      spec.LibStorageClassName,
		logSize:       resource.MustParse(memgraphcomv1alpha1.DefaultLogPVCSize),
		logAccessMode: spec.LogStorageAccessMode,
		logClass:      spec.LogStorageClassName,
	}
	if spec.LibPVCSize != nil {
		n.libSize = *spec.LibPVCSize
	}
	if spec.LogPVCSize != nil {
		n.logSize = *spec.LogPVCSize
	}
	if n.libAccessMode == "" {
		n.libAccessMode = memgraphcomv1alpha1.DefaultStorageAccessMode
	}
	if n.logAccessMode == "" {
		n.logAccessMode = memgraphcomv1alpha1.DefaultStorageAccessMode
	}
	return n
}

func imageRef(image memgraphcomv1alpha1.ImageSpec) string {
	repository := image.Repository
	if repository == "" {
		repository = memgraphcomv1alpha1.DefaultImageRepository
	}
	tag := image.Tag
	if tag == "" {
		tag = memgraphcomv1alpha1.DefaultImageTag
	}
	return repository + ":" + tag
}
