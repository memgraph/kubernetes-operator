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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// Defaults for optional spec fields. They are declared as CRD schema defaults
// on the field markers below and mirrored here so resource builders behave
// correctly on specs that never passed admission (e.g. in unit tests).
const (
	DefaultCoordinatorCount  int32 = 3
	DefaultDataInstanceCount int32 = 2

	DefaultImageRepository = "docker.io/memgraph/memgraph"
	DefaultImageTag        = "3.12.0-relwithdebinfo"
	DefaultImagePullPolicy = corev1.PullIfNotPresent

	DefaultSecretName            = "memgraph-secrets"
	DefaultLicenseSecretKey      = "MEMGRAPH_ENTERPRISE_LICENSE"
	DefaultOrganizationSecretKey = "MEMGRAPH_ORGANIZATION_NAME"

	DefaultCoreDumpsSize        = "10Gi"
	DefaultConfigureCorePattern = true

	DefaultLibPVCSize            = "1Gi"
	DefaultLogPVCSize            = "1Gi"
	DefaultCreateLogStorageClaim = true
	DefaultStorageAccessMode     = corev1.ReadWriteOnce
	DefaultStorageRetention      = RetentionPolicyRetain

	DefaultClusterDomain = "cluster.local"

	DefaultBoltPort        int32 = 7687
	DefaultManagementPort  int32 = 10000
	DefaultReplicationPort int32 = 20000
	DefaultCoordinatorPort int32 = 12000
)

// Probe timing defaults. Unlike the other defaults these are Go constants only:
// a CRD schema default is per-field, and the data instances' startup budget
// deliberately differs from every other probe's, which one shared schema
// default cannot express. The doc comments on ProbeSpec name them.
const (
	// DefaultProbeFailureThreshold is the failure budget of every probe except
	// the data instances' startup probe.
	DefaultProbeFailureThreshold int32 = 20

	// DefaultDataStartupProbeFailureThreshold gives data instances a generous
	// startup budget so a large snapshot restore is not killed mid-load: 1440
	// failures at the default 5s period is 2h, mirroring the
	// memgraph-high-availability Helm chart's default.
	DefaultDataStartupProbeFailureThreshold int32 = 1440

	DefaultProbeTimeoutSeconds int32 = 10
	DefaultProbePeriodSeconds  int32 = 5
)

// Names of the environment variables the operator itself sets on the Memgraph
// container, which extraEnv therefore may not carry.
const (
	// EnvLicense holds the enterprise license, wired from the secrets block.
	EnvLicense = "MEMGRAPH_ENTERPRISE_LICENSE"

	// EnvOrganization holds the organization name, wired from the secrets block.
	EnvOrganization = "MEMGRAPH_ORGANIZATION_NAME"

	// EnvPodName carries the pod's own name, from which a coordinator derives
	// its ordinal-dependent identity at startup.
	EnvPodName = "POD_NAME"

	// EnvCoreDumpsDir carries the core dumps mount path into the uploader
	// sidecar, which therefore may not set it itself. It is the only variable
	// the operator sets on a container other than Memgraph's.
	EnvCoreDumpsDir = "CORE_DUMPS_DIR"
)

// StorageRetentionPolicy decides what happens to the cluster's
// PersistentVolumeClaims when the MemgraphCluster is deleted.
// +kubebuilder:validation:Enum=Retain;Delete
type StorageRetentionPolicy string

const (
	// RetentionPolicyRetain leaves the PVCs behind when the MemgraphCluster is
	// deleted, so the data survives an accidental deletion.
	RetentionPolicyRetain StorageRetentionPolicy = "Retain"

	// RetentionPolicyDelete lets the StatefulSet controller garbage-collect the
	// PVCs together with the MemgraphCluster.
	RetentionPolicyDelete StorageRetentionPolicy = "Delete"
)

// Condition types reported on MemgraphCluster status. Both use normal-True
// polarity: True is the healthy state. Ready answers "is the cluster serving"
// (a MAIN is elected and reachable); Converged answers "does the cluster match
// the declared topology" (every declared coordinator and data instance is
// registered, and both StatefulSets run the declared number of replicas). A
// cluster can be Ready but not Converged — a MAIN still serves while a lost
// replica registration is being restored, or while a scale finishes.
const (
	// ConditionReady is True when a MAIN data instance is elected and the
	// coordinator leader is reachable.
	ConditionReady = "Ready"

	// ConditionConverged is True when the observed cluster matches the declared
	// topology: no registration commands are pending and both StatefulSets'
	// replica counts equal the declared counts. `kubectl wait
	// --for=condition=Converged` therefore means a scale is genuinely finished,
	// not merely accepted.
	ConditionConverged = "Converged"
)

// Condition reasons reported on MemgraphCluster status. Reasons are CamelCase
// per Kubernetes API conventions and are stable enough for tooling to gate on.
const (
	// ReasonApplyFailed is set when the API server rejected one of the desired
	// workload objects, so the cluster does not run the declared spec. The
	// condition message carries the rejection verbatim: the operator cannot act
	// on it, but it names exactly what a human has to change.
	ReasonApplyFailed = "ApplyFailed"

	// ReasonWorkloadsNotReady is set while not every workload pod is ready, so
	// registration has not been attempted.
	ReasonWorkloadsNotReady = "WorkloadsNotReady"

	// ReasonCoordinatorUnreachable is set when no coordinator answered
	// SHOW INSTANCES, so the cluster state cannot be observed.
	ReasonCoordinatorUnreachable = "CoordinatorUnreachable"

	// ReasonNoCoordinatorLeader is set when coordinators answer SHOW INSTANCES
	// but none of them reports a leader, so their views come from stale state
	// machines and no management query would be accepted anyway. It is kept
	// apart from CoordinatorUnreachable because the remedy differs: the pods are
	// up and serving Bolt, what is missing is a Raft quorum.
	ReasonNoCoordinatorLeader = "NoCoordinatorLeader"

	// ReasonRegistrationInProgress is set while registration commands are being
	// issued to converge the cluster toward the declared topology.
	ReasonRegistrationInProgress = "RegistrationInProgress"

	// ReasonAllInstancesRegistered is set when the observed cluster matches the
	// declared topology.
	ReasonAllInstancesRegistered = "AllInstancesRegistered"

	// ReasonRetirementInProgress is set while a lowered count of either role is
	// being carried out: the members beyond the declared count are still part of
	// the cluster, or their pods are still being shed. The message names them, so
	// a scale-down that stalls says which member it is waiting on.
	ReasonRetirementInProgress = "RetirementInProgress"

	// ReasonLeadershipTransferInProgress is set while a lowered coordinators
	// count is waiting on Raft leadership to move: Raft refuses to remove its own
	// leader, so a retiring coordinator holding leadership is asked to yield it
	// first. YIELD LEADERSHIP cannot name a successor, so the operator re-observes
	// the cluster under whichever coordinator won the election and may have to ask
	// again — which is exactly what this reason means when it persists.
	ReasonLeadershipTransferInProgress = "LeadershipTransferInProgress"

	// ReasonNoCaughtUpSurvivor is set while a lowered dataInstances count is
	// waiting to move MAIN off the instance it retires: no surviving instance is
	// both reachable and holding every transaction the MAIN has committed, so
	// demoting it now would drop those writes. The retiring MAIN keeps serving
	// until one catches up, which is a scale-down that pauses rather than one that
	// loses data. It persisting means replication is not progressing — the
	// survivors are down, or too far behind to catch up.
	ReasonNoCaughtUpSurvivor = "NoCaughtUpSurvivor"

	// ReasonMainElected is set when a data instance is observed as MAIN.
	ReasonMainElected = "MainElected"

	// ReasonNoMainElected is set when the cluster is reachable but no data
	// instance has yet been promoted to MAIN.
	ReasonNoMainElected = "NoMainElected"
)

// ImageSpec selects the Memgraph container image run by all cluster pods.
type ImageSpec struct {
	// repository is the Memgraph container image repository. It carries the
	// optional registry host and the image path only; the version belongs in
	// tag.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	// +kubebuilder:validation:XValidation:rule="!self.contains('@')",message="repository must not contain a digest; pin the image with tag instead"
	// +kubebuilder:validation:XValidation:rule="!self.substring(self.lastIndexOf('/') + 1).contains(':')",message="repository must not contain a tag; set image.tag instead"
	// +kubebuilder:default="docker.io/memgraph/memgraph"
	// +optional
	Repository string `json:"repository,omitempty"`

	// tag is the Memgraph container image tag. Prefer pinning a specific
	// Memgraph version over mutable tags such as "latest".
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9_][a-zA-Z0-9._-]*$`
	// +kubebuilder:default="3.12.0-relwithdebinfo"
	// +optional
	Tag string `json:"tag,omitempty"`

	// pullPolicy is the image pull policy applied to all cluster pods.
	// +kubebuilder:validation:Enum=Always;IfNotPresent;Never
	// +kubebuilder:default=IfNotPresent
	// +optional
	PullPolicy corev1.PullPolicy `json:"pullPolicy,omitempty"`
}

// SecretsSpec references an existing Kubernetes Secret holding the Memgraph
// enterprise license and organization name. The block mirrors the
// memgraph-high-availability Helm chart's secrets vocabulary; secret material
// is consumed by reference only and never appears in the CR.
//
// The has() guards keep the rule evaluable against the block's empty object
// default, which the API server checks before nested field defaults apply.
//
// +kubebuilder:validation:XValidation:rule="!has(self.licenseKey) || !has(self.organizationKey) || self.licenseKey != self.organizationKey",message="licenseKey and organizationKey must name different keys of the Secret"
type SecretsSpec struct {
	// name is the name of the Secret in the cluster's namespace.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	// +kubebuilder:default="memgraph-secrets"
	// +optional
	Name string `json:"name,omitempty"`

	// licenseKey is the key within the Secret holding the enterprise license.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[-._a-zA-Z0-9]+$`
	// +kubebuilder:default="MEMGRAPH_ENTERPRISE_LICENSE"
	// +optional
	LicenseKey string `json:"licenseKey,omitempty"`

	// organizationKey is the key within the Secret holding the organization
	// name the license was issued to.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[-._a-zA-Z0-9]+$`
	// +kubebuilder:default="MEMGRAPH_ORGANIZATION_NAME"
	// +optional
	OrganizationKey string `json:"organizationKey,omitempty"`
}

// RoleStorageSpec configures the two PersistentVolumeClaims every pod of a
// role gets: lib storage backing Memgraph's data directory, and log storage
// backing its log file. The knob names mirror the
// memgraph-high-availability Helm chart's storage.<role> block so translating
// a values file is mechanical.
//
// The fields below become StatefulSet volumeClaimTemplates, which Kubernetes
// treats as immutable: changing them on a live MemgraphCluster is rejected by
// the StatefulSet controller, not silently applied. Storage changes are a
// day-2 operation and out of scope for v1alpha1.
type RoleStorageSpec struct {
	// libPVCSize is the requested size of the lib storage claim, which backs
	// Memgraph's data directory (snapshots, WAL, and durability metadata).
	// +kubebuilder:default="1Gi"
	// +optional
	LibPVCSize *resource.Quantity `json:"libPVCSize,omitempty"`

	// libStorageAccessMode is the access mode requested for the lib storage
	// claim.
	// +kubebuilder:validation:Enum=ReadWriteOnce;ReadOnlyMany;ReadWriteMany;ReadWriteOncePod
	// +kubebuilder:default=ReadWriteOnce
	// +optional
	LibStorageAccessMode corev1.PersistentVolumeAccessMode `json:"libStorageAccessMode,omitempty"`

	// libStorageClassName is the StorageClass backing the lib storage claim.
	// Leave it unset to use the cluster's default StorageClass; set it to the
	// empty string to disable dynamic provisioning and bind a pre-created
	// PersistentVolume.
	// +kubebuilder:validation:MaxLength=253
	// +optional
	LibStorageClassName *string `json:"libStorageClassName,omitempty"`

	// createLogStorageClaim decides whether every pod of the role gets a log
	// storage claim at all. With it disabled the operator drops the claim and
	// passes an empty --log-file, which turns file logging off, so stderr and
	// `kubectl logs` (plus whatever collects it) become the single log sink. Use
	// it to avoid a second PersistentVolumeClaim per pod on clusters that ship
	// logs off-node anyway.
	//
	// The remaining log* knobs below are ignored while this is false.
	//
	// Like the sizes and classes around it this is effectively a create-time
	// choice: flipping it adds or removes a volumeClaimTemplate, which
	// Kubernetes forbids on a live StatefulSet, so the operator's apply is
	// rejected until the StatefulSet is recreated (delete it with
	// --cascade=orphan and the operator rebuilds it around the running pods).
	// +kubebuilder:default=true
	// +optional
	CreateLogStorageClaim *bool `json:"createLogStorageClaim,omitempty"`

	// logPVCSize is the requested size of the log storage claim, which backs
	// Memgraph's log file.
	// +kubebuilder:default="1Gi"
	// +optional
	LogPVCSize *resource.Quantity `json:"logPVCSize,omitempty"`

	// logStorageAccessMode is the access mode requested for the log storage
	// claim.
	// +kubebuilder:validation:Enum=ReadWriteOnce;ReadOnlyMany;ReadWriteMany;ReadWriteOncePod
	// +kubebuilder:default=ReadWriteOnce
	// +optional
	LogStorageAccessMode corev1.PersistentVolumeAccessMode `json:"logStorageAccessMode,omitempty"`

	// logStorageClassName is the StorageClass backing the log storage claim.
	// Leave it unset to use the cluster's default StorageClass; set it to the
	// empty string to disable dynamic provisioning and bind a pre-created
	// PersistentVolume.
	// +kubebuilder:validation:MaxLength=253
	// +optional
	LogStorageClassName *string `json:"logStorageClassName,omitempty"`
}

// StorageSpec configures persistence for both roles plus what happens to the
// claims when the MemgraphCluster goes away.
type StorageSpec struct {
	// retentionPolicy decides whether the cluster's PersistentVolumeClaims
	// survive deletion of the MemgraphCluster. It maps directly onto the
	// StatefulSets' persistentVolumeClaimRetentionPolicy.whenDeleted, so the
	// StatefulSet controller is the only thing that ever deletes storage — the
	// operator owns no finalizer and runs no cleanup of its own. The default
	// keeps production data safe from an accidental delete; dev clusters can
	// opt into self-cleanup.
	// +kubebuilder:default=Retain
	// +optional
	RetentionPolicy StorageRetentionPolicy `json:"retentionPolicy,omitempty"`

	// coordinators configures the storage of every coordinator pod.
	// +kubebuilder:default={}
	// +optional
	Coordinators RoleStorageSpec `json:"coordinators,omitzero"`

	// data configures the storage of every data instance pod.
	// +kubebuilder:default={}
	// +optional
	Data RoleStorageSpec `json:"data,omitzero"`
}

// RoleCoreDumpsSpec is the part of core dump collection that genuinely differs
// between the roles: whether they collect at all, and how much room a dump
// needs. Everything else — the storage class, the kernel setup, the uploader —
// is the same decision for both and lives on CoreDumpsSpec.
type RoleCoreDumpsSpec struct {
	// enabled provisions a core dumps volume for every pod of the role and
	// mounts it at /var/core/memgraph. It is off by default: a crashing Memgraph
	// is not the normal case, and the volume costs a third
	// PersistentVolumeClaim per pod.
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// size is the requested size of the role's core dumps claim. A dump is
	// roughly as large as the crashing process' resident memory, which is why
	// this is per role: a data instance holds the graph, a coordinator holds
	// Raft state. Size it against the role's memory limit, not its data.
	// +kubebuilder:default="10Gi"
	// +optional
	Size *resource.Quantity `json:"size,omitempty"`
}

// CoreDumpsUploaderSpec is a sidecar that reads the core dumps volume. It is
// deliberately not a full core/v1 Container: the narrow shape keeps the
// operator's pod security posture non-negotiable (no privileged sidecar, no
// extra volume mounts, no valueFrom smuggling secret material into the CR) and
// keeps the CRD small enough to apply client-side, which one inlined Container
// per role does not.
//
// +kubebuilder:validation:XValidation:rule="!has(self.env) || self.env.all(e, e.name != 'CORE_DUMPS_DIR')",message="env must not set CORE_DUMPS_DIR: the operator passes the core dumps mount path in it"
type CoreDumpsUploaderSpec struct {
	// image is the full sidecar image reference including its tag, for example
	// "amazon/aws-cli:2.33.28". Unlike the Memgraph image the operator has no
	// default for it, so a tag belongs here rather than in a separate field.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=383
	Image string `json:"image"`

	// pullPolicy is the image pull policy of the sidecar.
	// +kubebuilder:validation:Enum=Always;IfNotPresent;Never
	// +kubebuilder:default=IfNotPresent
	// +optional
	PullPolicy corev1.PullPolicy `json:"pullPolicy,omitempty"`

	// command overrides the image's entrypoint.
	// +optional
	Command []string `json:"command,omitempty"`

	// args are the arguments passed to the sidecar's entrypoint.
	// +optional
	Args []string `json:"args,omitempty"`

	// env passes literal, non-secret environment variables to the sidecar —
	// bucket names, prefixes, regions. Credentials belong in envFromSecrets.
	// +listType=map
	// +listMapKey=name
	// +optional
	Env []EnvVar `json:"env,omitempty"`

	// envFromSecrets names Secrets in the cluster's namespace whose keys become
	// environment variables of the sidecar. This is how credentials reach it:
	// by reference, so no secret material ever appears in this resource.
	// +listType=set
	// +optional
	EnvFromSecrets []string `json:"envFromSecrets,omitempty"`

	// resources sets the sidecar's compute resources. Leave it unset and the
	// sidecar schedules without requests or limits.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitzero"`
}

// CoreDumpsSpec configures core dump collection: what the two roles decide for
// themselves below, and above that the settings that are one decision for the
// whole cluster — where the volumes come from, whether the operator configures
// the node, and what ships the dumps away.
//
// The memgraph-high-availability Helm chart spreads the same feature across
// storage.<role>.coreDumps* and a separate top-level coreDumpUploader block
// that silently does nothing unless the per-role claim is enabled too. Here the
// dependency is structural: an uploader with no role collecting dumps is
// rejected, not ignored.
//
// Dumps are for debugging a crash, not for the cluster to run: nothing in the
// operator reads them, and the claims follow the same storage.retentionPolicy
// as the rest of the cluster's volumes.
//
// The has() guards keep the rule evaluable against the block's empty object
// default, which the API server checks before nested field defaults apply.
//
// +kubebuilder:validation:XValidation:rule="!has(self.uploader) || (has(self.coordinators) && has(self.coordinators.enabled) && self.coordinators.enabled) || (has(self.data) && has(self.data.enabled) && self.data.enabled)",message="uploader requires core dumps enabled for at least one role — there would be no volume for it to read"
type CoreDumpsSpec struct {
	// coordinators decides whether every coordinator pod collects dumps, and
	// how much room it gets for them.
	// +kubebuilder:default={}
	// +optional
	Coordinators RoleCoreDumpsSpec `json:"coordinators,omitzero"`

	// data decides whether every data instance pod collects dumps, and how much
	// room it gets for them.
	// +kubebuilder:default={}
	// +optional
	Data RoleCoreDumpsSpec `json:"data,omitzero"`

	// storageClassName is the StorageClass backing every core dumps claim of
	// this cluster. Leave it unset to use the cluster's default StorageClass;
	// set it to the empty string to disable dynamic provisioning and bind
	// pre-created PersistentVolumes.
	// +kubebuilder:validation:MaxLength=253
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`

	// configureCorePattern lets the operator point the kernel at
	// /var/core/memgraph by running a privileged init container that writes
	// /proc/sys/kernel/core_pattern. It uses the cluster's own Memgraph image,
	// so no second image has to be pulled.
	//
	// It is cluster-wide rather than per role for two reasons: core_pattern is a
	// property of the **node**, so it applies to every process that crashes
	// there regardless of which role asked for it, and what really decides this
	// is whether the namespace tolerates a privileged container at all.
	// PodSecurity "restricted" does not — set this to false there, or wherever
	// the platform manages core_pattern itself, and the operator only provisions
	// and mounts the volumes, trusting the node to already point at them.
	// +kubebuilder:default=true
	// +optional
	ConfigureCorePattern *bool `json:"configureCorePattern,omitempty"`

	// uploader is an optional sidecar that ships collected dumps off the volume
	// — to object storage, a debug host, wherever. Any image and destination
	// works, so no provider or credential vocabulary has to live in this API:
	// the operator mounts the core dumps volume into the sidecar read-only,
	// passes the directory as CORE_DUMPS_DIR, and gives it the same locked-down
	// security context as the Memgraph container. See
	// config/samples/v1alpha1_memgraphcluster.yaml for an S3 uploader
	// equivalent to the Helm chart's.
	//
	// One definition serves both roles — the destination and credentials do not
	// differ between them, and the pods are already distinguishable by hostname
	// — and it joins the pods of every role that collects dumps. It counts
	// toward pod readiness, so a sidecar that crash-loops keeps those roles from
	// ever being registered.
	// +optional
	Uploader *CoreDumpsUploaderSpec `json:"uploader,omitempty"`
}

// PortsSpec configures the internal ports Memgraph listens on. The knob names
// mirror the memgraph-high-availability Helm chart's ports block.
//
// These ports are load-bearing beyond the container: they are part of every
// advertised address the operator registers with the cluster (bolt_server,
// coordinator_server, management_server, replication_server), so a change
// reaches container ports, Services, and registration commands together.
// Changing a port on a live cluster is a day-2 operation and out of scope for
// v1alpha1: the pods restart on the new ports while the coordinators keep the
// addresses they were registered with.
//
// The has() guards keep the rule evaluable against the block's empty object
// default, which the API server checks before nested field defaults apply.
//
// +kubebuilder:validation:XValidation:rule="!(has(self.boltPort) && has(self.managementPort) && has(self.replicationPort) && has(self.coordinatorPort)) || [self.boltPort, self.managementPort, self.replicationPort, self.coordinatorPort].all(p, [self.boltPort, self.managementPort, self.replicationPort, self.coordinatorPort].exists_one(q, q == p))",message="boltPort, managementPort, replicationPort and coordinatorPort must all be different ports"
type PortsSpec struct {
	// boltPort is the port Memgraph serves the Bolt protocol on. Clients and
	// the operator's own management queries both use it.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=7687
	// +optional
	BoltPort *int32 `json:"boltPort,omitempty"`

	// managementPort is the port instances exchange HA management traffic on.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=10000
	// +optional
	ManagementPort *int32 `json:"managementPort,omitempty"`

	// replicationPort is the port data instances replicate over.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=20000
	// +optional
	ReplicationPort *int32 `json:"replicationPort,omitempty"`

	// coordinatorPort is the port coordinators run their Raft protocol on.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=12000
	// +optional
	CoordinatorPort *int32 `json:"coordinatorPort,omitempty"`
}

// ProbeSpec tunes the timings of one probe. The probe type itself is not
// configurable: every probe is a TCP-socket check against the role's own port
// (the coordinator port for coordinators, the Bolt port for data instances),
// which is the memgraph-high-availability Helm chart's established convention.
//
// Every field defaults to the value named in its doc comment.
type ProbeSpec struct {
	// failureThreshold is how many consecutive failures the probe tolerates
	// before acting. Defaults to 1440 for the data instances' startup probe —
	// 2h at the default period, so a large snapshot restore is not killed
	// mid-load — and to 20 for every other probe.
	// +kubebuilder:validation:Minimum=1
	// +optional
	FailureThreshold *int32 `json:"failureThreshold,omitempty"`

	// timeoutSeconds is how long a single probe attempt may take. Defaults to
	// 10.
	// +kubebuilder:validation:Minimum=1
	// +optional
	TimeoutSeconds *int32 `json:"timeoutSeconds,omitempty"`

	// periodSeconds is how often the probe runs. Defaults to 5.
	// +kubebuilder:validation:Minimum=1
	// +optional
	PeriodSeconds *int32 `json:"periodSeconds,omitempty"`
}

// RoleProbesSpec tunes all three probes of one role.
type RoleProbesSpec struct {
	// startupProbe gates the other two probes until the instance has started.
	// +optional
	StartupProbe ProbeSpec `json:"startupProbe,omitzero"`

	// readinessProbe decides whether the pod receives traffic and whether the
	// operator considers the workloads ready to register.
	// +optional
	ReadinessProbe ProbeSpec `json:"readinessProbe,omitzero"`

	// livenessProbe decides whether the container is restarted.
	// +optional
	LivenessProbe ProbeSpec `json:"livenessProbe,omitzero"`
}

// ProbesSpec tunes probe timings per role.
type ProbesSpec struct {
	// coordinators tunes the probes of every coordinator pod.
	// +optional
	Coordinators RoleProbesSpec `json:"coordinators,omitzero"`

	// data tunes the probes of every data instance pod.
	// +optional
	Data RoleProbesSpec `json:"data,omitzero"`
}

// ResourcesSpec sets the compute resources of the Memgraph container per role.
// When setting Memgraph's own --memory-limit through extraArgs, keep it below
// the pod's memory limit: Memgraph must hit its own limit and raise a query
// exception before the kubelet evicts the pod.
type ResourcesSpec struct {
	// coordinators are the resource requests and limits of every coordinator
	// pod's Memgraph container.
	// +optional
	Coordinators corev1.ResourceRequirements `json:"coordinators,omitzero"`

	// data are the resource requests and limits of every data instance pod's
	// Memgraph container.
	// +optional
	Data corev1.ResourceRequirements `json:"data,omitzero"`
}

// RoleLabelsSpec adds custom labels to one role's objects. The operator's own
// identity labels (app.kubernetes.io/name, /instance, /component, /managed-by)
// always win a key collision: they are what the StatefulSets and Services
// select on, so a custom label can never detach a pod from its cluster.
type RoleLabelsSpec struct {
	// podLabels are added to the role's pods.
	// +optional
	PodLabels map[string]string `json:"podLabels,omitempty"`

	// statefulSetLabels are added to the role's StatefulSet.
	// +optional
	StatefulSetLabels map[string]string `json:"statefulSetLabels,omitempty"`

	// serviceLabels are added to the role's headless Service.
	// +optional
	ServiceLabels map[string]string `json:"serviceLabels,omitempty"`
}

// LabelsSpec adds custom labels per role, mirroring the
// memgraph-high-availability Helm chart's labels block.
type LabelsSpec struct {
	// coordinators labels the coordinator objects.
	// +optional
	Coordinators RoleLabelsSpec `json:"coordinators,omitzero"`

	// data labels the data instance objects.
	// +optional
	Data RoleLabelsSpec `json:"data,omitzero"`
}

// EnvVar is one non-secret environment variable set on a role's Memgraph
// container. Only literal values are supported — there is deliberately no
// valueFrom — so secret material stays confined to the secrets block and the CR
// remains safe to commit.
type EnvVar struct {
	// name is the environment variable's name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[A-Za-z_][A-Za-z0-9_]*$`
	// +required
	Name string `json:"name"`

	// value is the literal, non-secret value.
	// +kubebuilder:validation:MaxLength=4096
	// +optional
	Value string `json:"value,omitempty"`
}

// ExtraEnvSpec passes additional non-secret environment variables to a role's
// Memgraph container, mirroring the memgraph-high-availability Helm chart's
// extraEnv block.
type ExtraEnvSpec struct {
	// coordinators are added to every coordinator pod's Memgraph container.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:XValidation:rule="self.all(e, !(e.name in ['MEMGRAPH_ENTERPRISE_LICENSE', 'MEMGRAPH_ORGANIZATION_NAME', 'POD_NAME']))",message="extraEnv must not set MEMGRAPH_ENTERPRISE_LICENSE or MEMGRAPH_ORGANIZATION_NAME (they come from the secrets block) or POD_NAME (it carries the pod's own identity)"
	// +optional
	Coordinators []EnvVar `json:"coordinators,omitempty"`

	// data are added to every data instance pod's Memgraph container.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:XValidation:rule="self.all(e, !(e.name in ['MEMGRAPH_ENTERPRISE_LICENSE', 'MEMGRAPH_ORGANIZATION_NAME', 'POD_NAME']))",message="extraEnv must not set MEMGRAPH_ENTERPRISE_LICENSE or MEMGRAPH_ORGANIZATION_NAME (they come from the secrets block) or POD_NAME (it carries the pod's own identity)"
	// +optional
	Data []EnvVar `json:"data,omitempty"`
}

// ExtraVolumesSpec adds pod volumes to a role beyond the ones the operator
// provisions, mirroring the memgraph-high-availability Helm chart's
// storage.<role>.extraVolumes block. Each entry is a core/v1 Volume: a Secret
// holding certificates, a ConfigMap, a CSI volume, an emptyDir, whatever the
// pod needs. extraVolumeMounts is what puts them in the Memgraph container.
//
// The entries are deliberately schemaless. A core/v1 Volume carries every
// volume source Kubernetes has, and inlining that schema twice grows this CRD
// past the size a client-side kubectl apply can carry — so the field accepts
// the same arbitrary volume YAML the Helm chart does, and the API server keeps
// it verbatim without validating its contents. What that costs: kubectl
// explain says nothing about the entries, and a malformed or misspelled volume
// source is caught when the operator applies the StatefulSet, surfacing on this
// resource as the ApplyFailed condition rather than as an admission error. Two
// mistakes that arrive that way in particular: reusing one of the volume names
// the operator owns (lib-storage, log-storage, core-dumps, tmp), and naming a
// volume source that does not exist.
type ExtraVolumesSpec struct {
	// coordinators are added to every coordinator pod.
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	Coordinators []corev1.Volume `json:"coordinators,omitempty"`

	// data are added to every data instance pod.
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	Data []corev1.Volume `json:"data,omitempty"`
}

// ExtraVolumeMountsSpec mounts volumes into a role's Memgraph container beyond
// the ones the operator mounts, mirroring the memgraph-high-availability Helm
// chart's storage.<role>.extraVolumeMounts block. Each entry names a volume the
// pod has — usually one from extraVolumes.
//
// The paths the operator already mounts are off limits: two mounts cannot share
// a path, and mounting over Memgraph's data or log directory would hide it.
type ExtraVolumeMountsSpec struct {
	// coordinators are added to every coordinator pod's Memgraph container.
	// +listType=map
	// +listMapKey=mountPath
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:XValidation:rule="self.all(m, !(m.mountPath in ['/var/lib/memgraph', '/var/log/memgraph', '/var/core/memgraph', '/tmp']))",message="extraVolumeMounts must not mount over a path the operator already mounts (/var/lib/memgraph, /var/log/memgraph, /var/core/memgraph, /tmp)"
	// +optional
	Coordinators []corev1.VolumeMount `json:"coordinators,omitempty"`

	// data are added to every data instance pod's Memgraph container.
	// +listType=map
	// +listMapKey=mountPath
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:XValidation:rule="self.all(m, !(m.mountPath in ['/var/lib/memgraph', '/var/log/memgraph', '/var/core/memgraph', '/tmp']))",message="extraVolumeMounts must not mount over a path the operator already mounts (/var/lib/memgraph, /var/log/memgraph, /var/core/memgraph, /tmp)"
	// +optional
	Data []corev1.VolumeMount `json:"data,omitempty"`
}

// ExtraArgsSpec passes additional Memgraph flags to a role, so any flag is
// usable without waiting for a typed field. The flags are appended after the
// ones the operator derives, and Memgraph takes the last occurrence of a
// repeated flag, so a flag set here overrides the operator's value.
//
// The ports and the coordinator identity are excluded from that override: they
// must stay consistent with the advertised addresses the operator registers
// with the cluster. Configure ports through spec.ports instead.
type ExtraArgsSpec struct {
	// coordinators are appended to every coordinator pod's Memgraph flags.
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=4096
	// +kubebuilder:validation:XValidation:rule="self.all(a, !a.replace('-', '_').matches('^_{1,2}(bolt_port|management_port|coordinator_id|coordinator_hostname|coordinator_port)($|[= ])'))",message="extraArgs must not set a port or the coordinator identity the operator derives (bolt-port, management-port, coordinator-id, coordinator-hostname, coordinator-port), in any spelling gflags accepts; configure ports through spec.ports"
	// +optional
	Coordinators []string `json:"coordinators,omitempty"`

	// data are appended to every data instance pod's Memgraph flags.
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=4096
	// +kubebuilder:validation:XValidation:rule="self.all(a, !a.replace('-', '_').matches('^_{1,2}(bolt_port|management_port|coordinator_id|coordinator_hostname|coordinator_port)($|[= ])'))",message="extraArgs must not set a port or the coordinator identity the operator derives (bolt-port, management-port, coordinator-id, coordinator-hostname, coordinator-port), in any spelling gflags accepts; configure ports through spec.ports"
	// +optional
	Data []string `json:"data,omitempty"`
}

// MemgraphClusterSpec defines the desired state of MemgraphCluster.
type MemgraphClusterSpec struct {
	// coordinators is the number of Raft coordinator instances. It must be odd
	// so the Raft quorum cannot split, and at least three, which is the
	// smallest quorum that survives losing a coordinator — this operator
	// builds real HA clusters, so the floor holds at creation as well as on an
	// update. Raising the count on a live cluster grows it: the operator adds
	// the new coordinators to the Raft cluster as their pods become ready.
	// +kubebuilder:validation:Minimum=3
	// +kubebuilder:validation:XValidation:rule="self % 2 == 1",message="coordinators must be an odd number so the Raft quorum cannot split"
	// +kubebuilder:default=3
	// +optional
	Coordinators *int32 `json:"coordinators,omitempty"`

	// dataInstances is the number of data instances. Raising the count on a
	// live cluster grows it: the operator registers the new instances as their
	// pods become ready.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=2
	// +optional
	DataInstances *int32 `json:"dataInstances,omitempty"`

	// image selects the Memgraph container image run by all cluster pods.
	// +kubebuilder:default={}
	// +optional
	Image ImageSpec `json:"image,omitzero"`

	// secrets references the Secret holding the enterprise license and
	// organization name.
	// +kubebuilder:default={}
	// +optional
	Secrets SecretsSpec `json:"secrets,omitzero"`

	// storage configures the persistent volumes backing both roles and their
	// retention on cluster deletion.
	// +kubebuilder:default={}
	// +optional
	Storage StorageSpec `json:"storage,omitzero"`

	// coreDumps optionally collects crash dumps of either role onto a volume of
	// its own.
	// +kubebuilder:default={}
	// +optional
	CoreDumps CoreDumpsSpec `json:"coreDumps,omitzero"`

	// clusterDomain is the Kubernetes cluster domain the advertised FQDN
	// addresses are built from: <pod>.<service>.<namespace>.svc.<clusterDomain>.
	// Override it on clusters configured with a domain other than the default.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	// +kubebuilder:default="cluster.local"
	// +optional
	ClusterDomain string `json:"clusterDomain,omitempty"`

	// ports configures the internal ports Memgraph listens on.
	// +kubebuilder:default={}
	// +optional
	Ports PortsSpec `json:"ports,omitzero"`

	// probes tunes the probe timings of both roles.
	// +optional
	Probes ProbesSpec `json:"probes,omitzero"`

	// resources sets the compute resources of both roles' Memgraph containers.
	// +optional
	Resources ResourcesSpec `json:"resources,omitzero"`

	// labels adds custom labels to both roles' pods, StatefulSets and Services.
	// +optional
	Labels LabelsSpec `json:"labels,omitzero"`

	// extraEnv passes additional non-secret environment variables to both
	// roles' Memgraph containers.
	// +optional
	ExtraEnv ExtraEnvSpec `json:"extraEnv,omitzero"`

	// extraArgs passes additional Memgraph flags to both roles.
	// +optional
	ExtraArgs ExtraArgsSpec `json:"extraArgs,omitzero"`

	// extraVolumes adds pod volumes to both roles beyond the ones the operator
	// provisions.
	// +optional
	ExtraVolumes ExtraVolumesSpec `json:"extraVolumes,omitzero"`

	// extraVolumeMounts mounts volumes into both roles' Memgraph containers
	// beyond the ones the operator mounts.
	// +optional
	ExtraVolumeMounts ExtraVolumeMountsSpec `json:"extraVolumeMounts,omitzero"`
}

// MemgraphClusterStatus defines the observed state of MemgraphCluster.
//
// Status is observation only: it carries no secret material and is never read
// back as reconcile input state.
type MemgraphClusterStatus struct {
	// main is the name of the data instance currently observed as MAIN, as
	// reported by SHOW INSTANCES on the coordinator leader. It is empty before
	// the initial MAIN is elected and updates when the Raft coordinators fail
	// over to a different instance.
	// +optional
	Main string `json:"main,omitempty"`

	// coordinators is how many of the declared coordinators the coordinator
	// leader reports as registered members of the Raft cluster. It reaches
	// spec.coordinators once registration has converged, so it is what a scale
	// is watched through.
	// +optional
	Coordinators int32 `json:"coordinators,omitempty"`

	// dataInstances is how many of the declared data instances the coordinator
	// leader reports as registered. It reaches spec.dataInstances once
	// registration has converged.
	// +optional
	DataInstances int32 `json:"dataInstances,omitempty"`

	// conditions represent the current state of the MemgraphCluster resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mgc
// +kubebuilder:printcolumn:name="Coordinators",type=integer,JSONPath=`.spec.coordinators`
// +kubebuilder:printcolumn:name="Data",type=integer,JSONPath=`.spec.dataInstances`
// +kubebuilder:printcolumn:name="Registered-Coordinators",type=integer,JSONPath=`.status.coordinators`,priority=1
// +kubebuilder:printcolumn:name="Registered-Data",type=integer,JSONPath=`.status.dataInstances`,priority=1
// +kubebuilder:printcolumn:name="Main",type=string,JSONPath=`.status.main`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Converged",type=string,JSONPath=`.status.conditions[?(@.type=="Converged")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MemgraphCluster is the Schema for the memgraphclusters API
type MemgraphCluster struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of MemgraphCluster
	// +required
	Spec MemgraphClusterSpec `json:"spec"`

	// status defines the observed state of MemgraphCluster
	// +optional
	Status MemgraphClusterStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// MemgraphClusterList contains a list of MemgraphCluster
type MemgraphClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []MemgraphCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &MemgraphCluster{}, &MemgraphClusterList{})
		return nil
	})
}
