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
	"k8s.io/utils/ptr"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
)

const (
	// Memgraph workload pods run as the non-root memgraph user baked into the
	// official images.
	memgraphUserID  int64 = 101
	memgraphGroupID int64 = 103

	coordinatorComponent = "coordinator"
	dataComponent        = "data"

	// ManagedByLabel and ManagedByValue mark every object the operator builds.
	// They are exported because the manager scopes its Pod cache to them: the
	// rolling restart needs per-pod revisions, and caching every pod in the
	// cluster to get them would be a rude surprise on a large one.
	ManagedByLabel = "app.kubernetes.io/managed-by"
	ManagedByValue = "memgraph-operator"
)

// Named container and Service port names shared by both roles.
const (
	boltPortName        = "bolt"
	managementPortName  = "management"
	coordinatorPortName = "coordinator"
	replicationPortName = "replication"
	metricsPortName     = "metrics"

	// configMapKind is the kind of the ConfigMaps the operator builds, which
	// server-side apply needs stated on each.
	configMapKind = "ConfigMap"
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

// LicenseSecret is the Secret the pods read the license from, and the two keys
// in it, as the secrets block names them or by their defaults.
type LicenseSecret struct {
	Name            string
	LicenseKey      string
	OrganizationKey string
}

// LicenseSecretOf returns the license Secret the cluster's pods are built to
// read: the same resolution the StatefulSets' environment uses, so the
// operator applies on a running pod exactly what the pod would read on a
// restart.
func LicenseSecretOf(cluster *memgraphcomv1alpha1.MemgraphCluster) LicenseSecret {
	spec := normalize(cluster.Spec)
	return LicenseSecret{Name: spec.secretName, LicenseKey: spec.licenseKey, OrganizationKey: spec.organizationKey}
}

// AWSCredentialsSecretOf returns the name of the Secret the data instances
// read their AWS credentials from, and an empty string for a cluster that
// names none.
func AWSCredentialsSecretOf(cluster *memgraphcomv1alpha1.MemgraphCluster) string {
	return normalize(cluster.Spec).awsCredentialsSecret
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
	l[ManagedByLabel] = ManagedByValue
	return l
}

// CoordinatorPodSelector matches the pods of this cluster's coordinator
// StatefulSet, and DataPodSelector those of its data StatefulSet. Both are the
// StatefulSets' own selectors, so they cannot drift from the pods they describe.
func CoordinatorPodSelector(cluster *memgraphcomv1alpha1.MemgraphCluster) map[string]string {
	return selectorLabels(cluster, coordinatorComponent)
}

// DataPodSelector matches the pods of this cluster's data StatefulSet.
func DataPodSelector(cluster *memgraphcomv1alpha1.MemgraphCluster) map[string]string {
	return selectorLabels(cluster, dataComponent)
}

// instanceLabel carries the cluster's name on every object of the cluster,
// and componentLabel the role.
const (
	instanceLabel  = "app.kubernetes.io/instance"
	componentLabel = "app.kubernetes.io/component"
)

// selectorLabels returns the immutable subset of labels used as StatefulSet
// and Service selectors.
func selectorLabels(cluster *memgraphcomv1alpha1.MemgraphCluster, component string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name": "memgraph",
		instanceLabel:            cluster.Name,
		componentLabel:           component,
	}
}

// normalizedSpec is a MemgraphClusterSpec with every optional field resolved
// to its default, so builders behave correctly on specs that never passed
// admission. Most defaults are CRD schema defaults mirrored as Go constants;
// the readiness probe timings are Go-only.
type normalizedSpec struct {
	coordinators    int32
	dataInstances   int32
	image           string
	pullPolicy      corev1.PullPolicy
	secretName      string
	licenseKey      string
	organizationKey string
	clusterDomain   string
	retentionPolicy memgraphcomv1alpha1.StorageRetentionPolicy
	readinessProbe  normalizedProbe
	coordinatorRole normalizedRole
	dataRole        normalizedRole
	external        normalizedExternal
	monitoring      normalizedMonitoring
	// boltTLSSecret names the Secret both roles serve Bolt and metrics with,
	// and is empty for a cluster that serves them in plaintext.
	boltTLSSecret string
	// intraClusterTLSSecret names the Secret the members authenticate each
	// other with, and is empty for a cluster whose members talk in plaintext.
	intraClusterTLSSecret string
	// awsCredentialsSecret names the Secret the data instances read their AWS
	// credentials from, and is empty for a cluster that names none.
	awsCredentialsSecret string
	// podAntiAffinity is the operator's own anti-affinity rule with its knobs
	// resolved, and nil for a cluster that asked for none.
	podAntiAffinity *normalizedPodAntiAffinity
	// maxMapCount is the vm.max_map_count floor the sysctl init container
	// raises every node to, and zero for a cluster that asked for none.
	maxMapCount int64
	// fixOwnership is whether every pod chowns its volume mount points to the
	// pod's identity from a root init container before Memgraph starts.
	fixOwnership bool
	// securityContext is the identity every pod runs under, resolved to the
	// images' uid and gid for a cluster that named none.
	securityContext normalizedSecurityContext
}

// normalizedSecurityContext is the securityContext block resolved: the three
// identity fields as the pod will carry them, nil for one the pod names not.
type normalizedSecurityContext struct {
	runAsUser  *int64
	runAsGroup *int64
	fsGroup    *int64
}

// chownTarget is the uid and gid the ownership init container chowns the
// volumes to: runAsUser, and runAsGroup or else fsGroup. It is false when the
// cluster left the uid or both groups to the platform, which the CRD rejects
// beside the fixOwnershipInitContainer block; the builder still has to decide.
func (c normalizedSecurityContext) chownTarget() (uid, gid int64, ok bool) {
	if c.runAsUser == nil {
		return 0, 0, false
	}
	switch {
	case c.runAsGroup != nil:
		return *c.runAsUser, *c.runAsGroup, true
	case c.fsGroup != nil:
		return *c.runAsUser, *c.fsGroup, true
	}
	return 0, 0, false
}

// normalizedPodAntiAffinity is the scheduling.podAntiAffinity block with every
// optional field resolved to its CRD schema default.
type normalizedPodAntiAffinity struct {
	typ         memgraphcomv1alpha1.PodAntiAffinityType
	scope       memgraphcomv1alpha1.PodAntiAffinityScope
	topologyKey string
}

// normalizedMonitoring is the monitoring block with each optional object
// resolved: a nil serviceMonitor is a cluster that asked for none.
type normalizedMonitoring struct {
	serviceMonitor   *normalizedServiceMonitor
	grafanaDashboard *normalizedGrafanaDashboard
	vmagent          *normalizedVMAgent
	vector           *normalizedVector
}

// normalizedVector is the vectorRemote block with its image defaults
// resolved; authSecret is empty for an unauthenticated endpoint.
type normalizedVector struct {
	image        string
	pullPolicy   corev1.PullPolicy
	logsEndpoint string
	authSecret   string
	extraLabels  map[string]string
	resources    corev1.ResourceRequirements
}

// normalizedVMAgent is the vmagentRemote block with its image and interval
// defaults resolved; basicAuthSecret is empty for an unauthenticated endpoint.
type normalizedVMAgent struct {
	image           string
	pullPolicy      corev1.PullPolicy
	remoteWriteURL  string
	basicAuthSecret string
	scrapeInterval  string
	externalLabels  map[string]string
	resources       corev1.ResourceRequirements
}

// normalizedGrafanaDashboard is what decorates the dashboard ConfigMap, its
// labels resolved to the sidecar default when the block names none.
type normalizedGrafanaDashboard struct {
	labels      map[string]string
	annotations map[string]string
}

// normalizedServiceMonitor is what decorates the ServiceMonitor.
type normalizedServiceMonitor struct {
	labels      map[string]string
	annotations map[string]string
	interval    string
}

// normalizedExternal is the external access block with its type, its per-role
// decorations and its Gateway settings resolved. It is the zero value for an
// unexposed cluster, which no builder reads: the external builders check the
// spec's block before building anything.
type normalizedExternal struct {
	typ          memgraphcomv1alpha1.ExternalAccessType
	coordinators normalizedExternalRole
	data         normalizedExternalRole
	gateway      normalizedGateway
}

// normalizedGateway is the Gateway block with its port base defaulted.
type normalizedGateway struct {
	className    string
	dataPortBase int32
	labels       map[string]string
	annotations  map[string]string
}

// normalizedExternalRole is what decorates one role's external objects.
type normalizedExternalRole struct {
	labels      map[string]string
	annotations map[string]string
}

// normalizedRole is everything the builders need that is configured per role.
type normalizedRole struct {
	storage           normalizedStorage
	coreDumps         normalizedCoreDumps
	resources         corev1.ResourceRequirements
	podLabels         map[string]string
	statefulSetLabels map[string]string
	serviceLabels     map[string]string
	env               []corev1.EnvVar
	flags             map[string]string
	extraVolumes      []corev1.Volume
	extraMounts       []corev1.VolumeMount
	userContainers    []corev1.Container
	initContainers    []corev1.Container
	scheduling        memgraphcomv1alpha1.RoleSchedulingSpec
}

// normalizedCoreDumps is one role's core dump configuration with every optional
// field resolved to its CRD schema default. Everything hangs off enabled, which
// is the role's block being present: without it the rest is unused, and no
// claim, mount, init container or sidecar reaches the role's pods.
type normalizedCoreDumps struct {
	enabled          bool
	size             resource.Quantity
	class            *string
	configurePattern bool
	uploader         *memgraphcomv1alpha1.CoreDumpsUploaderSpec
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
	// createLogClaim is false when the role opted out of log storage; the log
	// fields below are then unused.
	createLogClaim bool
	logSize        resource.Quantity
	logAccessMode  corev1.PersistentVolumeAccessMode
	logClass       *string
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
		retentionPolicy: spec.Storage.RetentionPolicy,
		readinessProbe:  normalizeProbe(spec.ReadinessProbe),
		maxMapCount:     normalizeMaxMapCount(spec.SysctlInitContainer),
		fixOwnership:    spec.FixOwnershipInitContainer != nil,
		securityContext: normalizeSecurityContext(spec.SecurityContext),
		coordinatorRole: normalizeRole(roleSpec{
			storage:        spec.Storage.Coordinators,
			coreDumps:      normalizeCoreDumps(spec.CoreDumps, spec.CoreDumps.Coordinators),
			resources:      spec.Resources.Coordinators,
			labels:         spec.Labels.Coordinators,
			env:            spec.ExtraEnv.Coordinators,
			flags:          spec.Flags.Coordinators,
			extraVolumes:   spec.ExtraVolumes.Coordinators,
			extraMounts:    spec.ExtraVolumeMounts.Coordinators,
			userContainers: spec.UserContainers.Coordinators,
			initContainers: spec.InitContainers.Coordinators,
			scheduling:     spec.Scheduling.Coordinators,
		}),
		dataRole: normalizeRole(roleSpec{
			storage:        spec.Storage.Data,
			coreDumps:      normalizeCoreDumps(spec.CoreDumps, spec.CoreDumps.Data),
			resources:      spec.Resources.Data,
			labels:         spec.Labels.Data,
			env:            spec.ExtraEnv.Data,
			flags:          spec.Flags.Data,
			extraVolumes:   spec.ExtraVolumes.Data,
			extraMounts:    spec.ExtraVolumeMounts.Data,
			userContainers: spec.UserContainers.Data,
			initContainers: spec.InitContainers.Data,
			scheduling:     spec.Scheduling.Data,
		}),
	}
	if spec.ExternalAccess != nil {
		n.external = normalizedExternal{
			typ: spec.ExternalAccess.Type,
			coordinators: normalizedExternalRole{
				labels:      spec.ExternalAccess.Coordinators.Labels,
				annotations: spec.ExternalAccess.Coordinators.Annotations,
			},
			data: normalizedExternalRole{
				labels:      spec.ExternalAccess.Data.Labels,
				annotations: spec.ExternalAccess.Data.Annotations,
			},
			gateway: normalizedGateway{
				className:    spec.ExternalAccess.Gateway.GatewayClassName,
				dataPortBase: intOrDefault(spec.ExternalAccess.Gateway.DataPortBase, memgraphcomv1alpha1.DefaultGatewayDataPortBase),
				labels:       spec.ExternalAccess.Gateway.Labels,
				annotations:  spec.ExternalAccess.Gateway.Annotations,
			},
		}
		if n.external.typ == "" {
			n.external.typ = memgraphcomv1alpha1.ExternalAccessLoadBalancer
		}
	}
	if spec.Monitoring != nil && spec.Monitoring.ServiceMonitor != nil {
		n.monitoring.serviceMonitor = &normalizedServiceMonitor{
			labels:      spec.Monitoring.ServiceMonitor.Labels,
			annotations: spec.Monitoring.ServiceMonitor.Annotations,
			interval:    spec.Monitoring.ServiceMonitor.Interval,
		}
	}
	if spec.Monitoring != nil && spec.Monitoring.GrafanaDashboard != nil {
		labels := spec.Monitoring.GrafanaDashboard.Labels
		if len(labels) == 0 {
			labels = map[string]string{
				memgraphcomv1alpha1.DefaultGrafanaDashboardLabel: memgraphcomv1alpha1.DefaultGrafanaDashboardValue,
			}
		}
		n.monitoring.grafanaDashboard = &normalizedGrafanaDashboard{
			labels:      labels,
			annotations: spec.Monitoring.GrafanaDashboard.Annotations,
		}
	}
	if spec.Monitoring != nil && spec.Monitoring.VMAgentRemote != nil {
		n.monitoring.vmagent = normalizeVMAgent(spec.Monitoring.VMAgentRemote)
	}
	if spec.Monitoring != nil && spec.Monitoring.VectorRemote != nil {
		n.monitoring.vector = normalizeVector(spec.Monitoring.VectorRemote)
	}
	if spec.TLS != nil && spec.TLS.Bolt != nil {
		n.boltTLSSecret = spec.TLS.Bolt.SecretName
	}
	if spec.TLS != nil && spec.TLS.IntraCluster != nil {
		n.intraClusterTLSSecret = spec.TLS.IntraCluster.SecretName
	}
	if spec.AWSCredentials != nil {
		n.awsCredentialsSecret = spec.AWSCredentials.SecretName
	}
	if rule := spec.Scheduling.PodAntiAffinity; rule != nil {
		n.podAntiAffinity = &normalizedPodAntiAffinity{
			typ:         rule.Type,
			scope:       rule.Scope,
			topologyKey: rule.TopologyKey,
		}
		if n.podAntiAffinity.typ == "" {
			n.podAntiAffinity.typ = memgraphcomv1alpha1.DefaultPodAntiAffinityType
		}
		if n.podAntiAffinity.scope == "" {
			n.podAntiAffinity.scope = memgraphcomv1alpha1.DefaultPodAntiAffinityScope
		}
		if n.podAntiAffinity.topologyKey == "" {
			n.podAntiAffinity.topologyKey = memgraphcomv1alpha1.DefaultPodAntiAffinityTopologyKey
		}
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
// (storage, resources, labels, extraEnv, flags) scatter across the
// CR, so normalization is written once and both roles resolve their defaults
// the same way.
type roleSpec struct {
	storage memgraphcomv1alpha1.RoleStorageSpec
	// coreDumps arrives already normalized: unlike the other entries it is
	// folded from two spec blocks (the cluster-wide settings and the role's
	// own), which the caller does before handing it over.
	coreDumps      normalizedCoreDumps
	resources      corev1.ResourceRequirements
	labels         memgraphcomv1alpha1.RoleLabelsSpec
	env            []memgraphcomv1alpha1.EnvVar
	flags          map[string]memgraphcomv1alpha1.FlagValue
	extraVolumes   []corev1.Volume
	extraMounts    []corev1.VolumeMount
	userContainers []corev1.Container
	initContainers []corev1.Container
	scheduling     memgraphcomv1alpha1.RoleSchedulingSpec
}

func normalizeRole(role roleSpec) normalizedRole {
	return normalizedRole{
		storage:           normalizeStorage(role.storage),
		coreDumps:         role.coreDumps,
		resources:         role.resources,
		podLabels:         role.labels.PodLabels,
		statefulSetLabels: role.labels.StatefulSetLabels,
		serviceLabels:     role.labels.ServiceLabels,
		env:               normalizeEnv(role.env),
		flags:             normalizeFlags(role.flags),
		extraVolumes:      role.extraVolumes,
		extraMounts:       role.extraMounts,
		userContainers:    role.userContainers,
		initContainers:    role.initContainers,
		scheduling:        role.scheduling,
	}
}

// intOrDefault resolves an optional numeric knob against its default.
func intOrDefault(configured *int32, fallback int32) int32 {
	if configured == nil {
		return fallback
	}
	return *configured
}

// normalizeProbe resolves the readiness probe's timings against their defaults.
func normalizeProbe(spec memgraphcomv1alpha1.ReadinessProbeSpec) normalizedProbe {
	return normalizedProbe{
		failureThreshold: intOrDefault(spec.FailureThreshold, memgraphcomv1alpha1.DefaultProbeFailureThreshold),
		timeoutSeconds:   intOrDefault(spec.TimeoutSeconds, memgraphcomv1alpha1.DefaultProbeTimeoutSeconds),
		periodSeconds:    intOrDefault(spec.PeriodSeconds, memgraphcomv1alpha1.DefaultProbePeriodSeconds),
	}
}

// normalizeFlags converts the spec's flag values to plain strings. Nothing is
// defaulted here: the operator's own defaults are merged in by the flag file
// builder, where the two can be told apart.
func normalizeFlags(spec map[string]memgraphcomv1alpha1.FlagValue) map[string]string {
	if len(spec) == 0 {
		return nil
	}
	flags := make(map[string]string, len(spec))
	for key, value := range spec {
		flags[key] = string(value)
	}
	return flags
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
		libSize:        resource.MustParse(memgraphcomv1alpha1.DefaultLibPVCSize),
		libAccessMode:  spec.LibStorageAccessMode,
		libClass:       spec.LibStorageClassName,
		createLogClaim: memgraphcomv1alpha1.DefaultCreateLogStorageClaim,
		logSize:        resource.MustParse(memgraphcomv1alpha1.DefaultLogPVCSize),
		logAccessMode:  spec.LogStorageAccessMode,
		logClass:       spec.LogStorageClassName,
	}
	if spec.CreateLogStorageClaim != nil {
		n.createLogClaim = *spec.CreateLogStorageClaim
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

// normalizeSecurityContext resolves the securityContext block: absent, the
// identity baked into the Memgraph images; present, exactly what it names,
// including nothing at all for a platform that assigns the identity itself.
// normalizeVMAgent resolves the vmagentRemote block's defaults, which mirror
// its CRD schema defaults so the builder behaves the same on a spec that never
// passed admission.
func normalizeVMAgent(block *memgraphcomv1alpha1.VMAgentRemoteSpec) *normalizedVMAgent {
	repository := block.Image.Repository
	if repository == "" {
		repository = memgraphcomv1alpha1.DefaultVMAgentImageRepository
	}
	tag := block.Image.Tag
	if tag == "" {
		tag = memgraphcomv1alpha1.DefaultVMAgentImageTag
	}
	pullPolicy := block.Image.PullPolicy
	if pullPolicy == "" {
		pullPolicy = memgraphcomv1alpha1.DefaultImagePullPolicy
	}
	interval := block.ScrapeInterval
	if interval == "" {
		interval = memgraphcomv1alpha1.DefaultVMAgentScrapeInterval
	}
	n := &normalizedVMAgent{
		image:          repository + ":" + tag,
		pullPolicy:     pullPolicy,
		remoteWriteURL: block.RemoteWrite.URL,
		scrapeInterval: interval,
		externalLabels: block.ExternalLabels,
		resources:      block.Resources,
	}
	if block.RemoteWrite.BasicAuth != nil {
		n.basicAuthSecret = block.RemoteWrite.BasicAuth.SecretName
	}
	return n
}

// normalizeVector resolves the vectorRemote block's defaults, mirroring its
// CRD schema defaults.
func normalizeVector(block *memgraphcomv1alpha1.VectorRemoteSpec) *normalizedVector {
	repository := block.Image.Repository
	if repository == "" {
		repository = memgraphcomv1alpha1.DefaultVectorImageRepository
	}
	tag := block.Image.Tag
	if tag == "" {
		tag = memgraphcomv1alpha1.DefaultVectorImageTag
	}
	pullPolicy := block.Image.PullPolicy
	if pullPolicy == "" {
		pullPolicy = memgraphcomv1alpha1.DefaultImagePullPolicy
	}
	n := &normalizedVector{
		image:        repository + ":" + tag,
		pullPolicy:   pullPolicy,
		logsEndpoint: block.LogsEndpoint,
		extraLabels:  block.ExtraLabels,
		resources:    block.Resources,
	}
	if block.Auth != nil {
		n.authSecret = block.Auth.SecretName
	}
	return n
}

func normalizeSecurityContext(spec *memgraphcomv1alpha1.PodSecurityContextSpec) normalizedSecurityContext {
	if spec == nil {
		return normalizedSecurityContext{
			runAsUser:  ptr.To(memgraphUserID),
			runAsGroup: ptr.To(memgraphGroupID),
			fsGroup:    ptr.To(memgraphGroupID),
		}
	}
	return normalizedSecurityContext{
		runAsUser:  spec.RunAsUser,
		runAsGroup: spec.RunAsGroup,
		fsGroup:    spec.FSGroup,
	}
}

// normalizeMaxMapCount resolves the sysctl init container block to the one
// number the builder needs: the floor to raise vm.max_map_count to, or zero
// for a cluster without the block.
func normalizeMaxMapCount(spec *memgraphcomv1alpha1.SysctlInitContainerSpec) int64 {
	if spec == nil {
		return 0
	}
	if spec.MaxMapCount > 0 {
		return spec.MaxMapCount
	}
	return memgraphcomv1alpha1.DefaultMaxMapCount
}

// normalizeCoreDumps folds the cluster-wide core dump settings together with
// the role's own into the single view the builders work from. A role collects
// dumps when its block is present; nothing is resolved eagerly for a role
// without one beyond its defaults, and the builders check enabled before
// reading the rest.
func normalizeCoreDumps(
	shared memgraphcomv1alpha1.CoreDumpsSpec,
	role *memgraphcomv1alpha1.RoleCoreDumpsSpec,
) normalizedCoreDumps {
	n := normalizedCoreDumps{
		enabled:          role != nil,
		size:             resource.MustParse(memgraphcomv1alpha1.DefaultCoreDumpsSize),
		class:            shared.StorageClassName,
		configurePattern: memgraphcomv1alpha1.DefaultConfigureCorePattern,
		uploader:         shared.Uploader,
	}
	if role != nil && role.Size != nil {
		n.size = *role.Size
	}
	if shared.ConfigureCorePattern != nil {
		n.configurePattern = *shared.ConfigureCorePattern
	}
	return n
}

func imageRef(image memgraphcomv1alpha1.ImageSpec) string {
	if image.Repository == "" && image.Tag == "" {
		return memgraphcomv1alpha1.DefaultImageReference
	}

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
