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
	DefaultImageTag        = "3.13.0-relwithdebinfo"
	DefaultImageReference  = DefaultImageRepository + ":" + DefaultImageTag
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

	// The internal Memgraph ports are fixed so every workload, Service and
	// advertised registration address always agrees.
	BoltPort        int32 = 7687
	ManagementPort  int32 = 10000
	ReplicationPort int32 = 20000
	CoordinatorPort int32 = 12000

	// MetricsPort is where every instance serves its OpenMetrics endpoint.
	// Memgraph's enterprise build starts the metrics server on both roles
	// whether or not anything scrapes it, so the operator does not switch it
	// on or off: it declares the port on the containers and the headless
	// Services and pins the format, and a scraper of the user's own works
	// with no spec at all.
	MetricsPort int32 = 9091

	// DefaultGrafanaDashboardLabel and its value are the label the Grafana
	// sidecar in kube-prometheus-stack selects dashboard ConfigMaps by, and
	// the default labels of a grafanaDashboard block that names none.
	DefaultGrafanaDashboardLabel = "grafana_dashboard"
	DefaultGrafanaDashboardValue = "1"
)

// Readiness probe timing defaults, Go constants mirrored by the doc comments
// on ReadinessProbeSpec. A readiness probe that has not succeeded yet only keeps
// the pod unready, so the failure threshold is not a budget anything has to fit
// in.
const (
	DefaultProbeFailureThreshold int32 = 20
	DefaultProbeTimeoutSeconds   int32 = 10
	DefaultProbePeriodSeconds    int32 = 5
)

// Names of the environment variables the operator itself sets on the Memgraph
// container, which extraEnv therefore may not carry.
const (
	// EnvLicense holds the enterprise license, wired from the secrets block.
	EnvLicense = "MEMGRAPH_ENTERPRISE_LICENSE"

	// EnvOrganization holds the organization name, wired from the secrets block.
	EnvOrganization = "MEMGRAPH_ORGANIZATION_NAME"

	// EnvPodName carries the pod's own name, from which a coordinator derives
	// its zero-based ordinal identity and stable hostname at startup.
	EnvPodName = "POD_NAME"

	// EnvCoreDumpsDir carries the core dumps mount path into the uploader
	// sidecar, which therefore may not set it itself. It is the only variable
	// the operator sets on a container other than Memgraph's.
	EnvCoreDumpsDir = "CORE_DUMPS_DIR"
)

// StorageRetentionPolicy decides what happens to a PersistentVolumeClaim of
// this cluster once nothing runs on it any more: either because the
// MemgraphCluster was deleted, or because a lowered replica count retired the
// pod that used it.
// +kubebuilder:validation:Enum=Retain;Delete
type StorageRetentionPolicy string

const (
	// RetentionPolicyRetain leaves the PVCs behind, so the data survives both an
	// accidental deletion of the MemgraphCluster and an accidental scale-down —
	// raising the count again reattaches the retained volume.
	RetentionPolicyRetain StorageRetentionPolicy = "Retain"

	// RetentionPolicyDelete lets the StatefulSet controller garbage-collect the
	// PVCs: all of them when the MemgraphCluster is deleted, and a retiring
	// pod's when a replica count is lowered. The data is not recoverable.
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

	// ConditionUpdated is True when every workload pod runs the pod template the
	// spec currently describes. Because both StatefulSets use updateStrategy
	// OnDelete, Kubernetes replaces no pod on its own: the operator restarts them
	// one at a time, data instances before coordinators, MAIN and the Raft leader
	// last. It is kept apart from Converged deliberately — Converged answers
	// "does the cluster have the declared members", this one answers "do they run
	// the declared template", and a user looking at a False condition needs to
	// know which of the two is happening.
	ConditionUpdated = "Updated"
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

	// ReasonRegistrationFailed is set when the coordinator leader rejected a
	// registration command, so the cluster does not have the declared topology.
	// The condition message carries the command and the rejection verbatim, for
	// the reason ApplyFailed does: the command is retried forever, and nothing the
	// operator can do will clear a rejection it does not understand, so the
	// resource has to name it rather than leaving it in the operator's log.
	ReasonRegistrationFailed = "RegistrationFailed"

	// ReasonAllInstancesRegistered is set when the observed cluster matches the
	// declared topology.
	ReasonAllInstancesRegistered = "AllInstancesRegistered"

	// ReasonExternalAddressPending is set when every declared member is
	// registered but an exposed one still announces its in-cluster address,
	// because the LoadBalancer in front of it has not been given an external
	// address yet. The message names the Service being waited on. Registration
	// itself is complete and in-cluster clients are served, so Ready is
	// unaffected; what is missing is the cloud provisioning the operator cannot
	// hurry. It persisting means the LoadBalancer is not being provisioned at
	// all — no cloud controller or no address pool answers the Service.
	ReasonExternalAddressPending = "ExternalAddressPending"

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

	// ReasonRollingRestartInProgress is set while the operator is restarting pods
	// to bring them onto the pod template the spec currently describes. The
	// message names the pod being restarted and why it is that one's turn, because
	// the order is the whole safety argument: every data instance except MAIN
	// first, then MAIN, then the coordinators with the Raft leader last.
	ReasonRollingRestartInProgress = "RollingRestartInProgress"

	// ReasonWaitingForCatchUp is set while a rolling restart waits for the data
	// instance it restarted last to hold every transaction the MAIN has committed
	// again. Until it does, restarting the next pod would leave recent writes on
	// the MAIN alone.
	ReasonWaitingForCatchUp = "WaitingForCatchUp"

	// ReasonAllPodsUpdated is set when every workload pod runs the pod template
	// the spec currently describes.
	ReasonAllPodsUpdated = "AllPodsUpdated"

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
	// +kubebuilder:default="3.13.0-relwithdebinfo"
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
// treats as immutable, so every one of them is pinned by a transition rule:
// changing it on a live MemgraphCluster is refused at admission with the
// procedure that works, rather than accepted and then rejected by the API
// server on every apply forever. Recreating the StatefulSet around its pods is
// not an option the operator offers — the StatefulSet controller only readopts
// pods in ascending ordinal order, MAIN first, which deadlocks the rolling
// restart. The log claim's knobs are pinned only while the claim exists;
// nothing reads them otherwise. A size given in other units is not a change:
// quantities are compared as quantities.
//
// The has() guards keep every rule evaluable against the block's empty object
// default, which the API server checks before nested field defaults apply.
//
// +kubebuilder:validation:XValidation:rule="has(self.libStorageClassName) == has(oldSelf.libStorageClassName) && (!has(self.libStorageClassName) || self.libStorageClassName == oldSelf.libStorageClassName)",message="libStorageClassName cannot be changed on a live cluster: it is part of a StatefulSet volumeClaimTemplate, which Kubernetes forbids changing in place. Delete the MemgraphCluster (its claims are retained under the default retention policy) and recreate it with the new value"
// +kubebuilder:validation:XValidation:rule="has(self.libPVCSize) == has(oldSelf.libPVCSize) && (!has(self.libPVCSize) || quantity(string(self.libPVCSize)).compareTo(quantity(string(oldSelf.libPVCSize))) == 0)",message="libPVCSize cannot be changed on a live cluster: it is part of a StatefulSet volumeClaimTemplate, which Kubernetes forbids changing in place. Delete the MemgraphCluster (its claims are retained under the default retention policy) and recreate it with the new value"
// +kubebuilder:validation:XValidation:rule="has(self.libStorageAccessMode) == has(oldSelf.libStorageAccessMode) && (!has(self.libStorageAccessMode) || self.libStorageAccessMode == oldSelf.libStorageAccessMode)",message="libStorageAccessMode cannot be changed on a live cluster: it is part of a StatefulSet volumeClaimTemplate, which Kubernetes forbids changing in place. Delete the MemgraphCluster (its claims are retained under the default retention policy) and recreate it with the new value"
// +kubebuilder:validation:XValidation:rule="(has(self.createLogStorageClaim) ? self.createLogStorageClaim : true) == (has(oldSelf.createLogStorageClaim) ? oldSelf.createLogStorageClaim : true)",message="createLogStorageClaim cannot be changed on a live cluster: it adds or removes a StatefulSet volumeClaimTemplate, which Kubernetes forbids in place. Delete the MemgraphCluster (its claims are retained under the default retention policy) and recreate it with the new setting"
// +kubebuilder:validation:XValidation:rule="!(has(self.createLogStorageClaim) ? self.createLogStorageClaim : true) || (has(self.logStorageClassName) == has(oldSelf.logStorageClassName) && (!has(self.logStorageClassName) || self.logStorageClassName == oldSelf.logStorageClassName))",message="logStorageClassName cannot be changed on a live cluster while the log claim exists: it is part of a StatefulSet volumeClaimTemplate, which Kubernetes forbids changing in place. Delete the MemgraphCluster (its claims are retained under the default retention policy) and recreate it with the new value"
// +kubebuilder:validation:XValidation:rule="!(has(self.createLogStorageClaim) ? self.createLogStorageClaim : true) || (has(self.logPVCSize) == has(oldSelf.logPVCSize) && (!has(self.logPVCSize) || quantity(string(self.logPVCSize)).compareTo(quantity(string(oldSelf.logPVCSize))) == 0))",message="logPVCSize cannot be changed on a live cluster while the log claim exists: it is part of a StatefulSet volumeClaimTemplate, which Kubernetes forbids changing in place. Delete the MemgraphCluster (its claims are retained under the default retention policy) and recreate it with the new value"
// +kubebuilder:validation:XValidation:rule="!(has(self.createLogStorageClaim) ? self.createLogStorageClaim : true) || (has(self.logStorageAccessMode) == has(oldSelf.logStorageAccessMode) && (!has(self.logStorageAccessMode) || self.logStorageAccessMode == oldSelf.logStorageAccessMode))",message="logStorageAccessMode cannot be changed on a live cluster while the log claim exists: it is part of a StatefulSet volumeClaimTemplate, which Kubernetes forbids changing in place. Delete the MemgraphCluster (its claims are retained under the default retention policy) and recreate it with the new value"
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
	// Like the sizes and classes around it this is a create-time choice:
	// flipping it adds or removes a volumeClaimTemplate, which Kubernetes
	// forbids on a live StatefulSet, so admission refuses the flip.
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

// StorageSpec configures persistence for both roles plus what happens to a
// claim once nothing runs on it any more.
type StorageSpec struct {
	// retentionPolicy decides whether a PersistentVolumeClaim of this cluster
	// survives being orphaned, which happens two ways: the MemgraphCluster is
	// deleted, or a lowered replica count retires the pod that used it. Both are
	// the same question — keep this cluster's data, or do not — so the policy
	// maps onto both halves of the StatefulSets'
	// persistentVolumeClaimRetentionPolicy, whenDeleted and whenScaled. That
	// makes the StatefulSet controller the only thing that ever deletes storage;
	// the operator owns no finalizer and runs no cleanup of its own. The default
	// keeps production data safe from an accidental delete or shrink; dev
	// clusters can opt into self-cleanup.
	//
	// Delete therefore makes lowering spec.coordinators or spec.dataInstances
	// destructive: the retiring pods' claims go with them.
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
//
// enabled is pinned by a transition rule because the volume it provisions is a
// StatefulSet volumeClaimTemplate, which Kubernetes forbids adding to or
// removing from a live StatefulSet. Without the rule the flip is accepted and
// then rejected on every reconcile as ApplyFailed, so the resource says yes and
// the cluster never changes. Rebuilding the StatefulSet around its pods is not an
// answer either: the StatefulSet controller cannot reconcile adopted pods whose
// volumes no longer match its templates and only recovers when pods are
// deleted in ascending ordinal order, MAIN and the Raft leader first — the
// reverse of the order a Memgraph cluster survives (kubernetes/kubernetes#141876).
// The has() guards keep the rule evaluable against the block's empty object
// default, which the API server checks before the field default applies.
//
// +kubebuilder:validation:XValidation:rule="(has(self.enabled) && self.enabled) == (has(oldSelf.enabled) && oldSelf.enabled)",message="coreDumps enabled cannot be changed on a live cluster: the core dumps volume is a StatefulSet volumeClaimTemplate, which Kubernetes forbids adding or removing in place. Delete the MemgraphCluster (its claims are retained under the default retention policy) and recreate it with the new setting"
// +kubebuilder:validation:XValidation:rule="!(has(self.enabled) && self.enabled) || (has(self.size) == has(oldSelf.size) && (!has(self.size) || quantity(string(self.size)).compareTo(quantity(string(oldSelf.size))) == 0))",message="coreDumps size cannot be changed on a live cluster while the role collects dumps: it is part of a StatefulSet volumeClaimTemplate, which Kubernetes forbids changing in place. Delete the MemgraphCluster (its claims are retained under the default retention policy) and recreate it with the new value"
type RoleCoreDumpsSpec struct {
	// enabled provisions a core dumps volume for every pod of the role and
	// mounts it at /var/core/memgraph. It is off by default: a crashing Memgraph
	// is not the normal case, and the volume costs a third
	// PersistentVolumeClaim per pod.
	//
	// It is a create-time choice: once the cluster exists it cannot be switched
	// on or off, and an edit that tries is rejected at admission with the
	// procedure that works — delete the MemgraphCluster, whose claims the
	// default Retain policy keeps, and recreate it.
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
// +kubebuilder:validation:XValidation:rule="!((has(self.coordinators) && has(self.coordinators.enabled) && self.coordinators.enabled) || (has(self.data) && has(self.data.enabled) && self.data.enabled)) || (has(self.storageClassName) == has(oldSelf.storageClassName) && (!has(self.storageClassName) || self.storageClassName == oldSelf.storageClassName))",message="coreDumps storageClassName cannot be changed on a live cluster while a role collects dumps: it is part of a StatefulSet volumeClaimTemplate, which Kubernetes forbids changing in place. Delete the MemgraphCluster (its claims are retained under the default retention policy) and recreate it with the new value"
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

// ReadinessProbeSpec tunes the timings of the one probe every pod of the
// cluster carries, its readiness probe. The probe type itself is not
// configurable: it is a TCP-socket check against the role's own port (the
// coordinator port for coordinators, the Bolt port for data instances), which
// is the memgraph-high-availability Helm chart's established convention. One
// block serves both roles: nothing about readiness differs between them.
//
// Until the probe first succeeds — which for a data instance is after every
// database has been recovered — the pod is unready and nothing more, so none of
// these timings is a budget a recovery has to fit in. Every field defaults to
// the value named in its doc comment.
//
// There is deliberately no liveness and no startup probe. Memgraph recovers its
// databases before it opens any port, so during recovery nothing distinguishes
// an instance that is loading a large snapshot from one that is hung, and a
// TCP-socket liveness check can only kill the former — the only budget it can
// be given is a guess that grows with the dataset. A recovery longer than the
// guess then never finishes, because every kill starts it over. What the
// liveness check could catch, a Bolt listener that went away, already ends the
// container by itself: the process is gone. With no liveness there is nothing
// for a startup probe to hold off, and readiness alone gives the right
// behaviour for free: a recovering pod is unready, receives no traffic and is
// waited on by the operator, and it is restarted by nothing but its own exit.
// Restarts of a running instance belong to the operator's rolling restart,
// which knows the cluster's state, not to the kubelet, which does not.
type ReadinessProbeSpec struct {
	// failureThreshold is how many consecutive failures flip a pod that was
	// ready to unready. Defaults to 20.
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
// must stay consistent with the fixed advertised addresses the operator
// registers with the cluster.
type ExtraArgsSpec struct {
	// coordinators are appended to every coordinator pod's Memgraph flags.
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=4096
	// +kubebuilder:validation:XValidation:rule="self.all(a, !a.replace('-', '_').matches('^_{1,2}(bolt_port|management_port|replication_port|coordinator_id|coordinator_hostname|coordinator_port)($|[= ])'))",message="extraArgs must not set a fixed port or the coordinator identity the operator derives (bolt-port, management-port, replication-port, coordinator-id, coordinator-hostname, coordinator-port), in any spelling gflags accepts"
	// +optional
	Coordinators []string `json:"coordinators,omitempty"`

	// data are appended to every data instance pod's Memgraph flags.
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=4096
	// +kubebuilder:validation:XValidation:rule="self.all(a, !a.replace('-', '_').matches('^_{1,2}(bolt_port|management_port|replication_port|coordinator_id|coordinator_hostname|coordinator_port)($|[= ])'))",message="extraArgs must not set a fixed port or the coordinator identity the operator derives (bolt-port, management-port, replication-port, coordinator-id, coordinator-hostname, coordinator-port), in any spelling gflags accepts"
	// +optional
	Data []string `json:"data,omitempty"`
}

// ExternalAccessType is how the cluster is reached from outside Kubernetes.
// +kubebuilder:validation:Enum=LoadBalancer;Gateway
type ExternalAccessType string

const (
	// ExternalAccessLoadBalancer exposes the cluster through Services of type
	// LoadBalancer: one shared by all coordinators, and one per data instance.
	ExternalAccessLoadBalancer ExternalAccessType = "LoadBalancer"

	// ExternalAccessGateway exposes the cluster through one Gateway API
	// Gateway the operator owns: a TCP listener shared by all coordinators on
	// the bolt port, and one listener per data instance on its own port, each
	// with a TCPRoute to the instance behind it.
	ExternalAccessGateway ExternalAccessType = "Gateway"

	// DefaultGatewayDataPortBase is the first data instance's Gateway listener
	// port; instance N listens on DefaultGatewayDataPortBase + N. It mirrors the
	// memgraph-high-availability Helm chart's gateway.dataPortBase default.
	DefaultGatewayDataPortBase int32 = 9000
)

// ExternalAccessGatewaySpec configures the Gateway the operator creates when
// type is Gateway. TCPRoute has no host matching, so every data instance needs
// a listener of its own on its own port, and the listener list is a function
// of dataInstances: raising the count adds a listener, lowering it removes one.
// That is why the operator owns the Gateway rather than attaching routes to
// one somebody else runs.
type ExternalAccessGatewaySpec struct {
	// gatewayClassName names the GatewayClass the Gateway is created with,
	// which is what picks the controller (Envoy Gateway, Cilium, Istio, ...)
	// that programs it. It is required when type is Gateway.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +optional
	GatewayClassName string `json:"gatewayClassName,omitempty"`

	// dataPortBase is the listener port of the first data instance; instance
	// N is exposed on dataPortBase + N. These are the ports clients open on
	// their firewalls, so they are a knob rather than a constant, but the
	// default works out of the box. The coordinators share one listener on
	// the bolt port 7687, so the base must lie above it, and the range must
	// stay within the valid port space for the declared dataInstances.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=9000
	// +optional
	DataPortBase *int32 `json:"dataPortBase,omitempty"`

	// labels are added to the Gateway object.
	// +kubebuilder:validation:MaxProperties=64
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// annotations are added to the Gateway object.
	// +kubebuilder:validation:MaxProperties=64
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

const (
	// ExternalDNSHostnameAnnotation is the annotation external-dns reads to
	// publish a DNS record for a Service. It is the one third-party annotation
	// the operator knows: when a user sets it, that hostname is what the
	// operator announces as the exposed instance's bolt address, because
	// external-dns writes the record at the DNS provider and never back into
	// the Service status, so nothing else could tell the operator the name
	// exists.
	ExternalDNSHostnameAnnotation = "external-dns.alpha.kubernetes.io/hostname"

	// OrdinalPlaceholder is replaced with the pod ordinal in every annotation
	// value copied onto a per-instance external object, so one annotation map
	// can name a distinct hostname for every data instance.
	OrdinalPlaceholder = "{ordinal}"
)

// ExternalAccessRoleSpec decorates one role's external objects. The role's
// serviceLabels from the labels block also land on its external Services;
// these are the labels and annotations that mark only the external objects.
type ExternalAccessRoleSpec struct {
	// labels are added to the role's external objects: its Services, and with
	// type Gateway its TCPRoutes too. The operator's own identity labels win a
	// key collision, as they do everywhere else.
	// +kubebuilder:validation:MaxProperties=64
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// annotations are added to the role's external objects — with type
	// LoadBalancer the Services, with type Gateway the TCPRoutes, which is
	// where external-dns reads a route's hostname from. Cloud load balancer
	// tuning, external-dns hostnames, whatever the controllers in front of the
	// cluster read. On a per-instance object every value has "{ordinal}"
	// replaced with the pod ordinal.
	// +kubebuilder:validation:MaxProperties=64
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// ExternalAccessSpec exposes the cluster outside Kubernetes. Clients outside
// the cluster need two things: a way in, and a routing table whose addresses
// they can reach — the coordinators hand every client the bolt address each
// instance was registered with. The operator therefore owns both halves. It
// creates the external objects, and it registers every exposed instance with
// the external address those objects acquire: the external-dns hostname
// annotation when one is set, otherwise the hostname or IP the LoadBalancer
// reports in its status, and the in-cluster pod address until either exists.
// The registered address follows the external one whenever it changes, and
// reverts to the in-cluster one when this block is removed.
//
// Both roles are exposed together: a routing table pointing at data instances
// clients cannot reach is a configuration that can only be a mistake.
//
// The data instances each get their own external object and therefore each
// need their own hostname, so a hostname annotation on the data block must
// carry the "{ordinal}" placeholder; the coordinators share one object and one
// hostname, so theirs must not.
//
// +kubebuilder:validation:XValidation:rule="!has(self.data) || !has(self.data.annotations) || !('external-dns.alpha.kubernetes.io/hostname' in self.data.annotations) || self.data.annotations['external-dns.alpha.kubernetes.io/hostname'].contains('{ordinal}')",message="data.annotations external-dns.alpha.kubernetes.io/hostname must contain {ordinal}: every data instance has its own external address, and one hostname for all of them would register the same routing address for every instance"
// +kubebuilder:validation:XValidation:rule="!has(self.coordinators) || !has(self.coordinators.annotations) || !('external-dns.alpha.kubernetes.io/hostname' in self.coordinators.annotations) || !self.coordinators.annotations['external-dns.alpha.kubernetes.io/hostname'].contains('{ordinal}')",message="coordinators.annotations external-dns.alpha.kubernetes.io/hostname must not contain {ordinal}: all coordinators share one external address"
// +kubebuilder:validation:XValidation:rule="self.type != 'Gateway' || (has(self.gateway) && has(self.gateway.gatewayClassName))",message="gateway.gatewayClassName is required when type is Gateway: it names the GatewayClass whose controller programs the Gateway"
// +kubebuilder:validation:XValidation:rule="self.type == 'Gateway' || !has(self.gateway)",message="gateway is only used when type is Gateway; remove it or change the type"
// +kubebuilder:validation:XValidation:rule="!has(self.gateway) || !has(self.gateway.dataPortBase) || self.gateway.dataPortBase > 7687",message="gateway.dataPortBase must be above 7687, the port of the coordinators' shared listener, so no data listener can land on it"
type ExternalAccessSpec struct {
	// type selects how the cluster is exposed. LoadBalancer creates one
	// Service of type LoadBalancer shared by all coordinators and one per data
	// instance, each publishing the bolt port. Gateway creates one Gateway API
	// Gateway with a TCP listener shared by all coordinators on the bolt port
	// and one per data instance on gateway.dataPortBase + ordinal, each fed by
	// a TCPRoute; the Gateway API CRDs and a Gateway controller must already be
	// installed on the cluster.
	// +required
	Type ExternalAccessType `json:"type"`

	// coordinators decorates the coordinators' shared external object.
	// +optional
	Coordinators ExternalAccessRoleSpec `json:"coordinators,omitzero"`

	// data decorates every data instance's external object, with "{ordinal}"
	// in annotation values replaced by the instance's pod ordinal.
	// +optional
	Data ExternalAccessRoleSpec `json:"data,omitzero"`

	// gateway configures the Gateway created when type is Gateway.
	// +optional
	Gateway ExternalAccessGatewaySpec `json:"gateway,omitzero"`
}

// ServiceMonitorSpec asks the operator for the one object a Prometheus
// Operator needs to scrape the cluster: a ServiceMonitor in the cluster's
// namespace, selecting both headless Services by the cluster's identity
// labels, with one endpoint on the metrics port. The endpoint itself needs no
// asking: every instance serves OpenMetrics on port 9091 regardless, and a
// user running their own ServiceMonitor, PodMonitor or scrape config needs
// nothing from this block.
//
// The object always lives in the cluster's namespace. Owner references cannot
// cross namespaces, and the operator garbage-collects and prunes through them,
// so there is no namespace knob; a Prometheus in another namespace is pointed
// at this one with its serviceMonitorNamespaceSelector. Scheme and TLS
// settings arrive with TLS support, driven by the same spec that turns it on.
type ServiceMonitorSpec struct {
	// labels are added to the ServiceMonitor. They are what a Prometheus
	// selects ServiceMonitors by — with kube-prometheus-stack, the release
	// label, for example "release: kube-prometheus-stack". The operator's own
	// identity labels win a key collision, as they do everywhere else.
	// +kubebuilder:validation:MaxProperties=64
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// annotations are added to the ServiceMonitor.
	// +kubebuilder:validation:MaxProperties=64
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// interval is how often Prometheus scrapes every instance, as a Prometheus
	// duration such as "15s" or "1m". Omitted, the ServiceMonitor names no
	// interval and Prometheus's global scrape interval applies.
	// +kubebuilder:validation:Pattern=`^(0|(([0-9]+)y)?(([0-9]+)w)?(([0-9]+)d)?(([0-9]+)h)?(([0-9]+)m)?(([0-9]+)s)?(([0-9]+)ms)?)$`
	// +optional
	Interval string `json:"interval,omitempty"`
}

// GrafanaDashboardSpec asks the operator to provision the "Memgraph
// OpenMetrics" Grafana dashboard the way the memgraph-high-availability chart
// does: as a ConfigMap in the cluster's namespace holding the dashboard JSON,
// for a Grafana sidecar to discover by label. The dashboard binds to a
// datasource template variable, so it needs no per-cluster edit. The JSON is
// compiled into the operator, copied from the chart; it changes with operator
// releases, not with the spec.
//
// The ConfigMap always lives in the cluster's namespace, for the reason the
// ServiceMonitor does; a sidecar watching another namespace is told to look
// here (kube-prometheus-stack: sidecar.dashboards.searchNamespace).
type GrafanaDashboardSpec struct {
	// labels are the labels the Grafana sidecar selects dashboard ConfigMaps
	// by. They default to grafana_dashboard: "1", the kube-prometheus-stack
	// convention, so an empty block works out of the box; a sidecar that
	// selects on something else gets that instead. The operator's own identity
	// labels win a key collision, as they do everywhere else.
	// +kubebuilder:validation:MaxProperties=64
	// +kubebuilder:default={grafana_dashboard: "1"}
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// annotations are added to the ConfigMap. The Grafana sidecar reads
	// grafana_folder from here to file the dashboard in a folder.
	// +kubebuilder:validation:MaxProperties=64
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// MonitoringSpec is what the operator creates for a monitoring stack the user
// already runs. Each block is optional and presence-based, like externalAccess:
// present, the object is created and kept; removed, it is deleted again. What
// every instance serves — OpenMetrics on port 9091 — is not configured here,
// because it is served whether or not this block exists.
type MonitoringSpec struct {
	// serviceMonitor creates a Prometheus Operator ServiceMonitor scraping
	// every instance of the cluster. The ServiceMonitor CRD
	// (monitoring.coreos.com/v1) must already be installed on the cluster: it
	// belongs to whoever installs Prometheus Operator and is never bundled
	// with the operator. Asking for one on a cluster without it is reported
	// on the resource as ApplyFailed.
	// +optional
	ServiceMonitor *ServiceMonitorSpec `json:"serviceMonitor,omitempty"`

	// grafanaDashboard provisions the Memgraph OpenMetrics Grafana dashboard
	// as a ConfigMap a Grafana sidecar loads.
	// +optional
	GrafanaDashboard *GrafanaDashboardSpec `json:"grafanaDashboard,omitempty"`
}

// MemgraphClusterSpec defines the desired state of MemgraphCluster.
//
// The one rule here spans two blocks: with type Gateway every data instance
// gets a listener on gateway.dataPortBase + ordinal, so the declared count
// decides whether the range fits in the port space. The has() guards keep the
// rule evaluable before the nested defaults apply.
//
// +kubebuilder:validation:XValidation:rule="!has(self.externalAccess) || !has(self.externalAccess.gateway) || !has(self.externalAccess.gateway.dataPortBase) || !has(self.dataInstances) || self.externalAccess.gateway.dataPortBase + self.dataInstances <= 65536",message="externalAccess.gateway.dataPortBase + dataInstances must not exceed 65536: every data instance listens on dataPortBase + its ordinal"
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

	// readinessProbe tunes the readiness probe timings of every pod, the one
	// probe the pods carry.
	// +optional
	ReadinessProbe ReadinessProbeSpec `json:"readinessProbe,omitzero"`

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

	// externalAccess exposes the cluster outside Kubernetes and registers the
	// exposed instances with the addresses clients reach them at. Absent, the
	// cluster is reachable in-cluster only; removing it takes the external
	// objects away again and reverts the registered addresses.
	// +optional
	ExternalAccess *ExternalAccessSpec `json:"externalAccess,omitempty"`

	// monitoring creates the objects a monitoring stack the user already runs
	// discovers the cluster by. Every instance serves OpenMetrics on port 9091
	// whether or not this block is set; the block only adds the objects that
	// point a stack at it.
	// +optional
	Monitoring *MonitoringSpec `json:"monitoring,omitempty"`
}

// ExternalAddress is the external address one member, or the coordinators
// together, are announced at.
type ExternalAddress struct {
	// name is the member the address belongs to, as SHOW INSTANCES names it.
	// +required
	Name string `json:"name"`

	// address is the "host:port" clients outside the cluster reach the member
	// at. It is absent while the LoadBalancer in front of the member has not
	// been given an address yet.
	// +optional
	Address string `json:"address,omitempty"`
}

// ExternalAccessStatus reports the external addresses the operator announces
// to clients: what a client outside the cluster connects to, and what the
// coordinators hand back in the routing table.
type ExternalAccessStatus struct {
	// coordinators is the one "host:port" every coordinator is announced at. It
	// is absent while the coordinators' LoadBalancer has no address yet.
	// +optional
	Coordinators string `json:"coordinators,omitempty"`

	// data is every declared data instance with the external address it is
	// announced at, in ordinal order.
	// +listType=map
	// +listMapKey=name
	// +optional
	Data []ExternalAddress `json:"data,omitempty"`
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

	// externalAccess is the external addresses the cluster is announced at,
	// present only while spec.externalAccess is set. An address listed here is
	// what the operator drives the registered bolt address toward; the
	// Converged condition says whether it has landed.
	// +optional
	ExternalAccess *ExternalAccessStatus `json:"externalAccess,omitempty"`

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
