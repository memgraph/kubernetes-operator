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

	DefaultMaxMapCount int64 = 524288

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

	// The vmagent the vmagentRemote block runs: the image the HA chart
	// defaults to, and the chart's scrape interval.
	DefaultVMAgentImageRepository = "docker.io/victoriametrics/vmagent"
	DefaultVMAgentImageTag        = "v1.139.0"
	DefaultVMAgentScrapeInterval  = "15s"

	// VMAgentPort is where vmagent serves its own HTTP endpoints: health,
	// its metrics and target status. It is fixed, like the Memgraph ports,
	// because nothing outside the pod dials it.
	VMAgentPort int32 = 8429

	// The keys of the basic-auth Secrets vmagentRemote.remoteWrite.basicAuth
	// and vectorRemote.auth name: those of a kubernetes.io/basic-auth Secret,
	// which are also what the HA chart's usernameKey and passwordKey default
	// to.
	BasicAuthUsernameKey = "username"
	BasicAuthPasswordKey = "password"

	// The Vector the vectorRemote block runs beside every instance: the image
	// the HA chart defaults to.
	DefaultVectorImageRepository = "docker.io/timberio/vector"
	DefaultVectorImageTag        = "0.49.0-debian"

	// MonitoringPort is where every instance serves its monitoring websocket,
	// the stream of its own log lines the Vector sidecar reads. It is
	// Memgraph's default and the operator neither sets nor exposes it: the
	// sidecar dials it inside the pod.
	MonitoringPort int32 = 7444
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

	// ReasonSettingsPending is set while a run-time flag could not yet be
	// applied to every instance because a ready pod did not answer SHOW
	// DATABASE SETTINGS: the Bolt endpoint lagging readiness, or a pod the
	// roll is replacing. The message names the pods. The pass retries on a
	// delay; the flag file already carries the value for the pod's next start.
	ReasonSettingsPending = "SettingsPending"

	// ReasonSettingsRejected is set when an instance refused a SET DATABASE
	// SETTING the flags block asks for: a value its validator does not accept,
	// or a setting the running Memgraph version does not know at run time. The
	// message names the pod, the setting and Memgraph's error verbatim, for the
	// reason RegistrationFailed does: the command is retried forever, and only
	// a changed flag clears it. The operator never escalates to a restart on
	// its own — a value an instance rejects at run time it would reject at
	// startup too, and crash-loop on.
	ReasonSettingsRejected = "SettingsRejected"

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
// between the roles: whether they collect at all, which is the block being
// present, and how much room a dump needs. Everything else — the storage
// class, the kernel setup, the uploader — is the same decision for both and
// lives on CoreDumpsSpec.
//
// The block's presence is pinned by a transition rule on CoreDumpsSpec, not
// here: a rule on a block never fires when the block is added or removed
// whole. The rule below fires only while the block exists in both versions,
// which is exactly "while the role collects dumps", and pins the one field
// that lands in the claim template. The has() guard keeps it evaluable
// against a bare {} the API server checks before the field default applies.
//
// +kubebuilder:validation:XValidation:rule="has(self.size) == has(oldSelf.size) && (!has(self.size) || quantity(string(self.size)).compareTo(quantity(string(oldSelf.size))) == 0)",message="coreDumps size cannot be changed on a live cluster while the role collects dumps: it is part of a StatefulSet volumeClaimTemplate, which Kubernetes forbids changing in place. Delete the MemgraphCluster (its claims are retained under the default retention policy) and recreate it with the new value"
type RoleCoreDumpsSpec struct {
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
// A role collects dumps when its block is present, like every other optional
// block of this resource: there is no enabled knob. The
// memgraph-high-availability Helm chart spreads the same feature across
// storage.<role>.coreDumps* and a separate top-level coreDumpUploader block
// that silently does nothing unless the per-role claim is enabled too. Here
// the dependency is structural: an uploader with no role collecting dumps is
// rejected, not ignored.
//
// A role's block is pinned by a transition rule because the volume it
// provisions is a StatefulSet volumeClaimTemplate, which Kubernetes forbids
// adding to or removing from a live StatefulSet. Without the rule the change is
// accepted and then rejected on every reconcile as ApplyFailed, so the
// resource says yes and the cluster never changes. Rebuilding the StatefulSet
// around its pods is not an answer either: the StatefulSet controller cannot
// reconcile adopted pods whose volumes no longer match its templates and only
// recovers when pods are deleted in ascending ordinal order, MAIN and the Raft
// leader first — the reverse of the order a Memgraph cluster survives
// (kubernetes/kubernetes#141876). The rules live here rather than on the role
// block because a rule on a block never fires when the block itself is added
// or removed.
//
// Dumps are for debugging a crash, not for the cluster to run: nothing in the
// operator reads them, and the claims follow the same storage.retentionPolicy
// as the rest of the cluster's volumes.
//
// The has() guards keep the rules evaluable against the block's empty object
// default, which the API server checks before nested field defaults apply.
//
// +kubebuilder:validation:XValidation:rule="has(self.coordinators) == has(oldSelf.coordinators)",message="coreDumps.coordinators cannot be added or removed on a live cluster: the core dumps volume is a StatefulSet volumeClaimTemplate, which Kubernetes forbids adding or removing in place. Delete the MemgraphCluster (its claims are retained under the default retention policy) and recreate it with the new setting"
// +kubebuilder:validation:XValidation:rule="has(self.data) == has(oldSelf.data)",message="coreDumps.data cannot be added or removed on a live cluster: the core dumps volume is a StatefulSet volumeClaimTemplate, which Kubernetes forbids adding or removing in place. Delete the MemgraphCluster (its claims are retained under the default retention policy) and recreate it with the new setting"
// +kubebuilder:validation:XValidation:rule="!has(self.uploader) || has(self.coordinators) || has(self.data)",message="uploader requires core dumps for at least one role — there would be no volume for it to read"
// +kubebuilder:validation:XValidation:rule="!(has(self.coordinators) || has(self.data)) || (has(self.storageClassName) == has(oldSelf.storageClassName) && (!has(self.storageClassName) || self.storageClassName == oldSelf.storageClassName))",message="coreDumps storageClassName cannot be changed on a live cluster while a role collects dumps: it is part of a StatefulSet volumeClaimTemplate, which Kubernetes forbids changing in place. Delete the MemgraphCluster (its claims are retained under the default retention policy) and recreate it with the new value"
type CoreDumpsSpec struct {
	// coordinators, when present, makes every coordinator pod collect dumps
	// onto a volume of the size it names. It is a create-time choice: once the
	// cluster exists the block cannot be added or removed, and an edit that
	// tries is rejected at admission with the procedure that works — delete the
	// MemgraphCluster, whose claims the default Retain policy keeps, and
	// recreate it.
	// +optional
	Coordinators *RoleCoreDumpsSpec `json:"coordinators,omitempty"`

	// data, when present, makes every data instance pod collect dumps onto a
	// volume of the size it names. Like coordinators it is a create-time
	// choice.
	// +optional
	Data *RoleCoreDumpsSpec `json:"data,omitempty"`

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

// SysctlInitContainerSpec is the memgraph-high-availability Helm chart's
// sysctlInitContainer block: a privileged init container, run first in every
// pod of both roles, that raises the node's vm.max_map_count to what Memgraph
// needs. Memgraph checks the value at startup and prints "Max virtual memory
// areas vm.max_map_count ... is too low" below its floor; under load the
// symptom is a crash on bad_alloc or munmap once the process has more memory
// mappings than the kernel allows, and the kernel's own default of 65530 is
// far below what a graph in memory takes.
//
// The block is presence-based like every other optional block of the
// resource, so there is no enabled knob: present, the container runs; absent,
// the pods start with whatever the node has. That is the one place this
// departs from the chart, which runs the container unless told not to, and it
// is why the sample and the quickstart example carry the block written out.
// Leave it out in a namespace that forbids privileged containers (PodSecurity
// "restricted") or on nodes the platform already tunes.
//
// vm.max_map_count is a property of the node, not the pod: it is not one of
// the namespaced sysctls a pod's securityContext.sysctls can set, so the only
// way to set it from inside a pod is a privileged root container, exactly as
// the chart does. The chart's image knobs are dropped: the container runs the
// cluster's own Memgraph image, already on the node, and writes /proc/sys
// directly so no sysctl binary is needed.
type SysctlInitContainerSpec struct {
	// maxMapCount is the vm.max_map_count the node is raised to. It is a floor:
	// a node already at or above it is left alone, so the operator never lowers
	// a value an administrator set higher for something else on the node. The
	// default is the value Memgraph itself checks for and the one its docs
	// recommend for up to 64 GB of RAM; larger nodes want about one map area
	// per 128 KB of memory, see
	// https://memgraph.com/docs/database-management/system-configuration.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=524288
	// +optional
	MaxMapCount int64 `json:"maxMapCount,omitempty"`
}

// PodSecurityContextSpec is the identity every pod of both roles runs under:
// the three fields of a core/v1 PodSecurityContext that decide who owns the
// process and the volumes. It is the memgraph-high-availability Helm chart's
// memgraphUserId and memgraphGroupId, reshaped so that a platform assigning
// the identity itself can be told to.
//
// The block is presence-based, and presence means the user owns these three
// fields. Absent, the operator writes the identity baked into the Memgraph
// images: uid 101, gid 103 and fsGroup 103, which is what makes a claim
// writable to the non-root process on a driver that honors fsGroup. Present,
// exactly the fields written land on the pod and no other: an empty block
// writes none of the three, which is what OpenShift's restricted-v2 SCC
// wants, since it assigns runAsUser and fsGroup from the namespace's range at
// admission and rejects a pod naming values outside it. Naming values covers
// a uid inside that range or a service account granted anyuid. Memgraph
// itself needs no particular uid: its one check is that the process owns the
// data directory, which it creates.
//
// runAsNonRoot and the seccomp profile are not here and stay set: every Pod
// Security Standard and SCC the operator targets requires both.
//
// Changing runAsUser on a cluster that already has data makes the next roll
// fail Memgraph's ownership check, because the data directory still belongs
// to the old uid; fixOwnershipInitContainer is what moves it. The field is
// deliberately not pinned, so that move stays possible.
type PodSecurityContextSpec struct {
	// runAsUser is the uid every container of the pod runs as. Absent, the
	// pod names none and the image's user (or the platform's assignment)
	// decides.
	// +kubebuilder:validation:Minimum=0
	// +optional
	RunAsUser *int64 `json:"runAsUser,omitempty"`

	// runAsGroup is the primary gid every container of the pod runs as.
	// Absent, the pod names none.
	// +kubebuilder:validation:Minimum=0
	// +optional
	RunAsGroup *int64 `json:"runAsGroup,omitempty"`

	// fsGroup is the group the pod's volumes are made owned by and writable
	// to. Absent, the pod names none, and a driver that honors fsGroup does
	// nothing to the volume's ownership.
	// +kubebuilder:validation:Minimum=0
	// +optional
	FSGroup *int64 `json:"fsGroup,omitempty"`
}

// FixOwnershipInitContainerSpec is the memgraph-high-availability Helm chart's
// fixOwnershipInitContainer block: an init container, run as root after the
// node-tuning ones in every pod of both roles, that chowns the pod's volume
// mount points to the memgraph user before Memgraph starts. Every pod sets
// fsGroup to the memgraph group, which is how a volume normally arrives
// writable, but some storage drivers (rancher.io/local-path among them) do
// not honor it and hand over a volume root owned by root:root. Memgraph runs
// as the non-root memgraph user and cannot create its data directory or its
// log file there; and a data directory that does exist but is owned by
// another user fails its startup check, "The process is running as user
// memgraph, but '...' is owned by user ...". The container chowns the lib
// mount, the log mount when the role has a log claim, and the core dumps
// mount when the role collects dumps, recursively, to the uid and gid the
// pods run as: the images' 101:103 unless securityContext names others, in
// which case runAsUser and runAsGroup (or fsGroup when no runAsGroup is
// named) are the target. That is where the chart's memgraphUserId and
// memgraphGroupId live; the block itself has no knobs.
//
// The block is presence-based like every other optional block of the
// resource, so there is no enabled knob and it has no fields: present, the
// container runs; absent, which matches the chart's default, the pods trust
// fsGroup. The chart's image knobs are dropped as they are for the sysctl
// container: this runs the cluster's own Memgraph image, already on the
// node. It is root but not privileged, holding only CAP_CHOWN, so a
// namespace enforcing the baseline Pod Security Standard admits it while
// one enforcing "restricted" does not (that forbids running as root); there
// the driver has to honor fsGroup. The same goes for a platform that assigns
// the pod's identity at admission, such as OpenShift under restricted-v2:
// the container can only chown to a uid the cluster names, so it is rejected
// beside a securityContext block that leaves runAsUser out.
type FixOwnershipInitContainerSpec struct{}

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
// When setting Memgraph's own --memory-limit through flags, keep it below
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

// PodAntiAffinityType is how hard the operator's anti-affinity rule is.
// +kubebuilder:validation:Enum=preferred;required
type PodAntiAffinityType string

const (
	// PodAntiAffinityPreferred asks the scheduler to keep the pods apart and
	// lets it co-locate them when nothing else fits: a cluster larger than the
	// node pool still comes up.
	PodAntiAffinityPreferred PodAntiAffinityType = "preferred"
	// PodAntiAffinityRequired refuses to co-locate: a pod with no node of its
	// own stays Pending.
	PodAntiAffinityRequired PodAntiAffinityType = "required"
)

// PodAntiAffinityScope is which pods the operator's anti-affinity rule keeps
// apart.
// +kubebuilder:validation:Enum=role;cluster
type PodAntiAffinityScope string

const (
	// PodAntiAffinityScopeRole keeps coordinators away from coordinators and
	// data instances away from data instances; a coordinator and a data
	// instance may share a node.
	PodAntiAffinityScopeRole PodAntiAffinityScope = "role"
	// PodAntiAffinityScopeCluster keeps every pod of the cluster away from
	// every other, whatever its role.
	PodAntiAffinityScopeCluster PodAntiAffinityScope = "cluster"
)

const (
	// DefaultPodAntiAffinityType is the hardness of the operator's rule when
	// the block names none.
	DefaultPodAntiAffinityType = PodAntiAffinityPreferred
	// DefaultPodAntiAffinityScope is the scope of the operator's rule when the
	// block names none.
	DefaultPodAntiAffinityScope = PodAntiAffinityScopeRole
	// DefaultPodAntiAffinityTopologyKey is the node label the operator's rule
	// spreads over when the block names none: distinct nodes.
	DefaultPodAntiAffinityTopologyKey = "kubernetes.io/hostname"
	// PodAntiAffinityWeight is the weight of the operator's preferred rule.
	// It is the maximum, so a user's own preferred terms are tie-breakers
	// unless they say otherwise.
	PodAntiAffinityWeight int32 = 100
)

// PodAntiAffinitySpec is the one pod anti-affinity rule the operator writes
// into both roles' pod templates. Present, the rule exists; the fields tune it.
// Absent, the operator writes none, and the per-role scheduling blocks are the
// whole of what the pods carry.
//
// The memgraph-high-availability Helm chart's three affinity modes are the
// three corners of this block: its default is type preferred with scope role,
// its parity is required with scope role, its unique is required with scope
// cluster.
type PodAntiAffinitySpec struct {
	// type is how hard the rule is. preferred, the default, asks the scheduler
	// to keep the pods apart and lets it co-locate them when nothing else
	// fits, so a cluster larger than the node pool still comes up. required
	// refuses to co-locate: with too few nodes the surplus pods stay Pending,
	// so use it on clusters sized for it and not on a single-node kind.
	// +kubebuilder:default=preferred
	// +optional
	Type PodAntiAffinityType `json:"type,omitempty"`

	// scope is which pods the rule keeps apart. role, the default, keeps
	// coordinators away from coordinators and data instances away from data
	// instances; with type required that is one coordinator and one data
	// instance per node at most, the chart's parity. cluster keeps every pod
	// of the cluster away from every other; with type required that is one
	// pod per node, the chart's unique, which needs at least coordinators +
	// dataInstances nodes.
	// +kubebuilder:default=role
	// +optional
	Scope PodAntiAffinityScope `json:"scope,omitempty"`

	// topologyKey is the node label the rule spreads over. The default,
	// kubernetes.io/hostname, means distinct nodes. A zone label such as
	// topology.kubernetes.io/zone means distinct zones, which for more pods
	// than zones is better said with a topologySpreadConstraint per role: an
	// anti-affinity forbids or discourages sharing a zone at all, a spread
	// constraint balances the pods across the zones there are.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=317
	// +kubebuilder:validation:Pattern=`^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`
	// +kubebuilder:default="kubernetes.io/hostname"
	// +optional
	TopologyKey string `json:"topologyKey,omitempty"`
}

// RoleSchedulingSpec is the scheduling surface of one role's pods, in the
// core/v1 vocabulary and passed through as written. The operator sets the pod
// labels a selector here would match: app.kubernetes.io/instance carries the
// cluster's name and app.kubernetes.io/component the role (coordinator or
// data).
//
// A change to any of it only lands when a pod is recreated. Both StatefulSets
// restart nothing on their own, so the operator rolls the cluster for it, data
// instances before coordinators, exactly as for any other pod-template change;
// the Updated condition reports the progress.
type RoleSchedulingSpec struct {
	// nodeSelector pins the role's pods to nodes carrying every one of these
	// labels. The chart's nodeSelection mode is a nodeSelector of
	// role: coordinator-node on the coordinators and role: data-node on the
	// data instances.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// tolerations let the role's pods schedule onto tainted nodes.
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=64
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// topologySpreadConstraints balance the role's pods across a topology,
	// typically zones. A constraint with no labelSelector is given the role's
	// own pod selector, so the usual one-liner of maxSkew, topologyKey and
	// whenUnsatisfiable is enough.
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=16
	// +optional
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`

	// podAntiAffinity is the role's own pod anti-affinity, appended to the
	// operator's rule when spec.scheduling.podAntiAffinity is present and the
	// whole rule when it is not — never a replacement for it.
	// +optional
	PodAntiAffinity *corev1.PodAntiAffinity `json:"podAntiAffinity,omitempty"`

	// priorityClassName names the PriorityClass the role's pods run under.
	// +kubebuilder:validation:MaxLength=253
	// +optional
	PriorityClassName string `json:"priorityClassName,omitempty"`
}

// SchedulingSpec decides where the pods land, mirroring the
// memgraph-high-availability Helm chart's affinity block. The operator's own
// rule is one decision for the cluster and sits at the top; what genuinely
// differs by role is per role, in the core/v1 vocabulary.
type SchedulingSpec struct {
	// podAntiAffinity is the anti-affinity rule the operator writes into both
	// roles' pod templates. Present, the pods of a role are kept apart, softly
	// unless told otherwise; an empty block is the chart's default. Absent,
	// the operator writes no rule at all and the scheduler places the pods by
	// free capacity alone, which is right on a single-node kind or when the
	// per-role blocks carry a hand-written rule instead.
	// +optional
	PodAntiAffinity *PodAntiAffinitySpec `json:"podAntiAffinity,omitempty"`

	// coordinators is the scheduling surface of the coordinator pods.
	// +optional
	Coordinators RoleSchedulingSpec `json:"coordinators,omitzero"`

	// data is the scheduling surface of the data instance pods.
	// +optional
	Data RoleSchedulingSpec `json:"data,omitzero"`
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

// UserContainersSpec adds containers of the user's own to a role's pods
// beside the Memgraph container, mirroring the memgraph-high-availability
// Helm chart's userContainers block: a debugger, a log shipper, a metrics
// exporter, whatever should share the pod's network namespace and volumes.
// Each entry is a core/v1 Container appended after the operator's own, so
// `kubectl logs` without -c keeps showing the database, and a container may
// mount any volume the pod has, including the ones from extraVolumes.
//
// The entries are deliberately schemaless, for the same reason extraVolumes
// is: a core/v1 Container carries every probe, env source and security field
// Kubernetes has, and inlining that schema twice grows this CRD past the size
// a client-side kubectl apply can carry. So the field accepts the same
// arbitrary container YAML the Helm chart does, and the API server keeps it
// verbatim without validating its contents. What that costs: kubectl explain
// says nothing about the entries, and a malformed container — a missing name
// or image, a name the pod already has (memgraph, core-dumps-uploader, the
// init containers) — is caught when the operator applies the StatefulSet,
// surfacing on this resource as the ApplyFailed condition rather than as an
// admission error.
//
// One thing is filled in: a container that names no securityContext gets the
// same locked-down one as the Memgraph container (non-root, read-only root
// filesystem, no privilege escalation, all capabilities dropped), so the
// chart's own example runs in a namespace enforcing the restricted Pod
// Security Standard. A container that names one keeps it as written; that is
// how a container that must write to its root filesystem, or run as another
// user, says so.
//
// A user container counts toward pod readiness like any other, so one that
// crash-loops keeps the pod from ever being registered. A change to this
// block is a pod-template change the rolling restart carries.
type UserContainersSpec struct {
	// coordinators are added to every coordinator pod.
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	Coordinators []corev1.Container `json:"coordinators,omitempty"`

	// data are added to every data instance pod.
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	Data []corev1.Container `json:"data,omitempty"`
}

// InitContainersSpec adds init containers of the user's own to a role's
// pods, mirroring the memgraph-high-availability Helm chart's initContainers
// block: seeding a volume, fetching a query module, waiting on a dependency,
// whatever must finish before Memgraph starts. Each entry is a core/v1
// Container appended after the operator's own init containers (init-sysctl,
// init-core-pattern, init-fix-perms, whichever the spec asked for), in the
// chart's order, so a user container sees volumes the ownership container has
// already fixed and may mount any volume the pod has, extraVolumes included.
//
// The entries are schemaless for the reason userContainers and extraVolumes
// are: a core/v1 Container schema inlined per role grows this CRD past the
// size a client-side kubectl apply can carry. The API server keeps the YAML
// verbatim without validating it, so a malformed container, or one named like
// a container the pod already has, is caught when the operator applies the
// StatefulSet and surfaces on this resource as the ApplyFailed condition
// rather than as an admission error.
//
// As with userContainers, a container that names no securityContext gets the
// same locked-down one as the Memgraph container, so the chart's own example
// runs in a namespace enforcing the restricted Pod Security Standard; one that
// names its own keeps it as written, which is how a container that must run
// as root or write to its root filesystem says so.
//
// An init container that fails keeps the pod from ever starting Memgraph, and
// so from ever being registered. A change to this block is a pod-template
// change the rolling restart carries.
type InitContainersSpec struct {
	// coordinators are run by every coordinator pod before Memgraph.
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	Coordinators []corev1.Container `json:"coordinators,omitempty"`

	// data are run by every data instance pod before Memgraph.
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	Data []corev1.Container `json:"data,omitempty"`
}

// FlagsSpec passes Memgraph flags to a role as a map from flag name to value,
// so any flag is usable without waiting for a typed field. Keys are flag names
// without their leading dashes, in either spelling gflags accepts (log-level
// or log_level); values are strings, so a boolean is written "true" or
// "false", which is also the only form Memgraph accepts for one at run time.
//
// The flags do not travel on the command line. The operator writes them into
// a per-role ConfigMap as a gflags flag file, which the Memgraph container
// loads with --flag-file ahead of the few flags the operator pins on the
// command line, so a pinned flag wins any repeat. A flag Memgraph can change
// on a running instance (log-level, query-execution-timeout-sec,
// storage-snapshot-interval and the other run-time settings) is applied to
// every instance with SET DATABASE SETTING the moment it changes and restarts
// nothing: the flag file carries it for the next start, whenever that is.
// Every other flag is read at startup only, so a change to one rolls both
// roles' pods in the usual order. Removing a flag issues no SET: a run-time
// setting keeps its value until the instance next restarts without the flag,
// and the few Memgraph persists across restarts keep it even then.
//
// The ports, the addresses the pods listen on, the coordinator identity, the
// data directory, the log file, the TLS files and the metrics format are
// excluded: they must stay consistent with the addresses the operator
// registers, the ports it declares and the files it mounts. So are the two
// AWS credential flags, because the CR carries no secret material; set those
// with SET DATABASE SETTING by hand.
//
// Values are checked for shape only — one line, at most 4096 characters —
// with one exception: log-level is checked against Memgraph's levels, because
// it is the flag everyone touches first and a bad level is otherwise found
// only when an instance refuses it.
type FlagsSpec struct {
	// coordinators are the flags every coordinator pod starts with.
	// +kubebuilder:validation:MaxProperties=64
	// +kubebuilder:validation:XValidation:rule="self.all(k, k.matches('^[A-Za-z][A-Za-z0-9_-]*$'))",message="flags keys are flag names without leading dashes, such as log-level"
	// +kubebuilder:validation:XValidation:rule="self.all(k, !k.replace('-', '_').matches('^(bolt_port|management_port|coordinator_port|coordinator_id|coordinator_hostname|data_directory|log_file|bolt_cert_file|bolt_key_file|cluster_cert_file|cluster_key_file|cluster_ca_file|metrics_format|metrics_port|monitoring_port|bolt_address|monitoring_address|aws_access_key|aws_secret_key)$'))",message="flags must not set a port, a listen address, the coordinator identity, the data directory, the log file, a TLS file, the metrics format or an AWS credential: the operator derives the former, and the latter is secret material to set with SET DATABASE SETTING"
	// +kubebuilder:validation:XValidation:rule="self.all(k, self.all(j, k == j || k.replace('-', '_') != j.replace('-', '_')))",message="two keys spell the same flag"
	// +kubebuilder:validation:XValidation:rule="self.all(k, k.replace('-', '_') != 'log_level' || self[k] in ['TRACE', 'DEBUG', 'INFO', 'WARNING', 'ERROR', 'CRITICAL'])",message="log-level must be one of TRACE, DEBUG, INFO, WARNING, ERROR, CRITICAL"
	// +optional
	Coordinators map[string]FlagValue `json:"coordinators,omitempty"`

	// data are the flags every data instance pod starts with.
	// +kubebuilder:validation:MaxProperties=64
	// +kubebuilder:validation:XValidation:rule="self.all(k, k.matches('^[A-Za-z][A-Za-z0-9_-]*$'))",message="flags keys are flag names without leading dashes, such as log-level"
	// +kubebuilder:validation:XValidation:rule="self.all(k, !k.replace('-', '_').matches('^(bolt_port|management_port|coordinator_port|coordinator_id|coordinator_hostname|data_directory|log_file|bolt_cert_file|bolt_key_file|cluster_cert_file|cluster_key_file|cluster_ca_file|metrics_format|metrics_port|monitoring_port|bolt_address|monitoring_address|aws_access_key|aws_secret_key)$'))",message="flags must not set a port, a listen address, the coordinator identity, the data directory, the log file, a TLS file, the metrics format or an AWS credential: the operator derives the former, and the latter is secret material to set with SET DATABASE SETTING"
	// +kubebuilder:validation:XValidation:rule="self.all(k, self.all(j, k == j || k.replace('-', '_') != j.replace('-', '_')))",message="two keys spell the same flag"
	// +kubebuilder:validation:XValidation:rule="self.all(k, k.replace('-', '_') != 'log_level' || self[k] in ['TRACE', 'DEBUG', 'INFO', 'WARNING', 'ERROR', 'CRITICAL'])",message="log-level must be one of TRACE, DEBUG, INFO, WARNING, ERROR, CRITICAL"
	// +optional
	Data map[string]FlagValue `json:"data,omitempty"`
}

// FlagValue is one flag's value, verbatim: one line of the flag file, so it
// may not contain a line break, which would otherwise start a second flag
// nobody declared. Memgraph reads every flag as text, so a number or a
// boolean is written as the string "120" or "true".
// +kubebuilder:validation:MaxLength=4096
// +kubebuilder:validation:Pattern=`^[^\n\r]*$`
type FlagValue string

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
// at this one with its serviceMonitorNamespaceSelector. The scheme is not a
// knob either: it follows spec.tls.bolt, because Memgraph serves metrics from
// the Bolt server context, so the endpoint is http until that block is set
// and https with verification skipped from then on.
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

// VMAgentImageSpec selects the vmagent image the vmagentRemote block runs. It
// has the shape of the Memgraph image block with vmagent's defaults: the
// chart's victoriametrics/vmagent at the tag the chart pins.
type VMAgentImageSpec struct {
	// repository is the vmagent container image repository, with the optional
	// registry host and the image path only; the version belongs in tag.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	// +kubebuilder:validation:XValidation:rule="!self.contains('@')",message="repository must not contain a digest; pin the image with tag instead"
	// +kubebuilder:validation:XValidation:rule="!self.substring(self.lastIndexOf('/') + 1).contains(':')",message="repository must not contain a tag; set image.tag instead"
	// +kubebuilder:default="docker.io/victoriametrics/vmagent"
	// +optional
	Repository string `json:"repository,omitempty"`

	// tag is the vmagent container image tag.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9_][a-zA-Z0-9._-]*$`
	// +kubebuilder:default="v1.139.0"
	// +optional
	Tag string `json:"tag,omitempty"`

	// pullPolicy is the image pull policy of the vmagent pod.
	// +kubebuilder:validation:Enum=Always;IfNotPresent;Never
	// +kubebuilder:default=IfNotPresent
	// +optional
	PullPolicy corev1.PullPolicy `json:"pullPolicy,omitempty"`
}

// BasicAuthSecretSpec names the Secret holding the credentials a remote
// monitoring agent authenticates to its endpoint with. The keys are not knobs:
// they are "username" and "password", the keys of a kubernetes.io/basic-auth
// Secret and what the HA chart's usernameKey and passwordKey default to, for
// the reason the TLS Secrets have fixed keys too. The operator never reads the
// Secret; the kubelet hands its keys to the agent's container. For vmagent the
// password is a mounted file re-read every second, so a rotated password takes
// effect with no restart and is never on the command line, and the username
// reaches it through an environment variable; for the Vector sidecar both
// reach it through environment variables, which Vector interpolates into its
// configuration.
type BasicAuthSecretSpec struct {
	// secretName is the name of the Secret in the cluster's namespace holding
	// the keys "username" and "password".
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	// +required
	SecretName string `json:"secretName"`
}

// RemoteWriteSpec is where vmagent ships what it scrapes.
type RemoteWriteSpec struct {
	// url is the Prometheus remote-write endpoint vmagent writes to, for
	// example "http://vmsingle.monitoring.svc.cluster.local:8428/api/v1/write".
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^https?://`
	// +required
	URL string `json:"url"`

	// basicAuth names the Secret vmagent authenticates to the endpoint with.
	// Absent, the endpoint is written to unauthenticated.
	// +optional
	BasicAuth *BasicAuthSecretSpec `json:"basicAuth,omitempty"`
}

// VectorImageSpec selects the Vector image the vectorRemote sidecar runs. It
// has the shape of the Memgraph image block with Vector's defaults: the
// chart's timberio/vector at the tag the chart pins.
type VectorImageSpec struct {
	// repository is the Vector container image repository, with the optional
	// registry host and the image path only; the version belongs in tag.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	// +kubebuilder:validation:XValidation:rule="!self.contains('@')",message="repository must not contain a digest; pin the image with tag instead"
	// +kubebuilder:validation:XValidation:rule="!self.substring(self.lastIndexOf('/') + 1).contains(':')",message="repository must not contain a tag; set image.tag instead"
	// +kubebuilder:default="docker.io/timberio/vector"
	// +optional
	Repository string `json:"repository,omitempty"`

	// tag is the Vector container image tag.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9_][a-zA-Z0-9._-]*$`
	// +kubebuilder:default="0.49.0-debian"
	// +optional
	Tag string `json:"tag,omitempty"`

	// pullPolicy is the image pull policy of the sidecar.
	// +kubebuilder:validation:Enum=Always;IfNotPresent;Never
	// +kubebuilder:default=IfNotPresent
	// +optional
	PullPolicy corev1.PullPolicy `json:"pullPolicy,omitempty"`
}

// VectorRemoteSpec asks the operator to run the HA chart's vectorRemote: a
// Vector sidecar in every pod of both roles that reads the instance's log
// stream from Memgraph's monitoring websocket and pushes it to a Loki-
// compatible endpoint, such as the VictoriaLogs Memgraph runs. It is the logs
// half of how Memgraph monitors a customer's cluster: Memgraph cannot reach
// into the customer's network, so the cluster pushes, outbound only, at the
// endpoint and with the credentials Memgraph gives the customer.
//
// Memgraph serves the websocket on every instance, both roles, at the default
// --monitoring-port 7444, with no switch to turn it off; the sidecar dials it
// inside the pod and needs no Service, no port on one and no API access. The
// websocket shares the Bolt TLS context, so on a cluster with spec.tls.bolt
// the sidecar dials wss without verifying the certificate, for the reason the
// operator's own dials do not. Each line is pushed with the labels the HA
// chart's sidecar sets — app and job "memgraph", role, namespace, pod, level —
// plus extraLabels, so the dashboards Memgraph keeps for chart users work for
// operator users unchanged. Adding or removing the block is a pod-template
// change the roll carries. Nothing new lands in status.
type VectorRemoteSpec struct {
	// image selects the Vector image. Left out, the chart's default image at
	// the tag the operator pins is run.
	// +kubebuilder:default={}
	// +optional
	Image VectorImageSpec `json:"image,omitzero"`

	// logsEndpoint is the base URL of the Loki-compatible push API Vector
	// writes to; Vector appends /loki/api/v1/push itself. For VictoriaLogs
	// that is its insert path, for example
	// "http://victoria-logs.monitoring.svc.cluster.local:9428/insert".
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^https?://`
	// +required
	LogsEndpoint string `json:"logsEndpoint"`

	// auth names the Secret Vector authenticates to the endpoint with. Absent,
	// the endpoint is pushed to unauthenticated.
	// +optional
	Auth *BasicAuthSecretSpec `json:"auth,omitempty"`

	// extraLabels are added to every log line pushed, beside the labels the
	// operator sets; for example cluster_id: production. Keys must be Loki
	// label names; a key the operator sets itself is the operator's.
	// +kubebuilder:validation:MaxProperties=64
	// +kubebuilder:validation:XValidation:rule="self.all(k, k.matches('^[a-zA-Z_][a-zA-Z0-9_]*$'))",message="extraLabels keys must be label names: letters, digits and underscores, not starting with a digit"
	// +optional
	ExtraLabels map[string]string `json:"extraLabels,omitempty"`

	// resources sets the sidecar's compute resources. Left out, it runs
	// without requests or limits.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitzero"`
}

// VMAgentRemoteSpec asks the operator to run the HA chart's vmagentRemote: one
// vmagent Deployment in the cluster's namespace that scrapes every instance's
// OpenMetrics endpoint over pod DNS and remote-writes the samples to a
// Prometheus remote-write endpoint. It is how Memgraph monitors a customer's
// cluster: Memgraph cannot reach into the customer's network to scrape it, so
// the cluster pushes its metrics, outbound only, to the VictoriaMetrics
// Memgraph runs and gives the customer the URL and credentials of. The scrape
// configuration is an operator-
// owned ConfigMap vmagent re-reads when it changes, so a count change reaches
// the running vmagent without a restart; a change to the url, the image or the
// credentials Secret is a pod-template change the Deployment rolls itself.
//
// The scrape scheme is not a knob: it follows spec.tls.bolt the way the
// ServiceMonitor's does, http until that block is set and https with
// verification skipped from then on. There is no namespace knob, for the
// reason the ServiceMonitor has none. The chart's kubernetes block, which
// scrapes kube-state-metrics, node-exporter and the kubelet through the API
// server, is deliberately absent: those targets are not this cluster's, and
// the kubelet job needs a cluster-scoped ClusterRole on nodes and nodes/proxy
// that no owner reference garbage-collects; a cluster-wide agent is the tool
// for them. The vmagent pod runs as uid 65534 under the restricted security
// context every container the operator builds runs under, because the image
// names no user of its own.
type VMAgentRemoteSpec struct {
	// image selects the vmagent image. Left out, the chart's default image at
	// the tag the operator pins is run.
	// +kubebuilder:default={}
	// +optional
	Image VMAgentImageSpec `json:"image,omitzero"`

	// remoteWrite is the endpoint vmagent writes to and how it authenticates.
	// +required
	RemoteWrite RemoteWriteSpec `json:"remoteWrite"`

	// scrapeInterval is how often vmagent scrapes every instance, as a
	// Prometheus duration such as "15s" or "1m".
	// +kubebuilder:validation:Pattern=`^(0|(([0-9]+)y)?(([0-9]+)w)?(([0-9]+)d)?(([0-9]+)h)?(([0-9]+)m)?(([0-9]+)s)?(([0-9]+)ms)?)$`
	// +kubebuilder:default="15s"
	// +optional
	ScrapeInterval string `json:"scrapeInterval,omitempty"`

	// externalLabels are added to every sample vmagent writes, which is how a
	// monitoring cluster receiving from many Memgraph clusters tells them
	// apart; for example cluster: production. Keys must be Prometheus label
	// names.
	// +kubebuilder:validation:MaxProperties=64
	// +kubebuilder:validation:XValidation:rule="self.all(k, k.matches('^[a-zA-Z_][a-zA-Z0-9_]*$'))",message="externalLabels keys must be Prometheus label names: letters, digits and underscores, not starting with a digit"
	// +optional
	ExternalLabels map[string]string `json:"externalLabels,omitempty"`

	// resources sets the vmagent container's compute resources. Left out, the
	// pod schedules without requests or limits.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitzero"`
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

	// vmagentRemote runs one vmagent that scrapes every instance of the
	// cluster and remote-writes the samples to a Prometheus remote-write
	// endpoint, such as a VictoriaMetrics in another cluster.
	// +optional
	VMAgentRemote *VMAgentRemoteSpec `json:"vmagentRemote,omitempty"`

	// vectorRemote adds a Vector sidecar to every pod that pushes the
	// instance's logs to a Loki-compatible endpoint, such as the VictoriaLogs
	// Memgraph runs to monitor the cluster.
	// +optional
	VectorRemote *VectorRemoteSpec `json:"vectorRemote,omitempty"`
}

// BoltTLSSpec makes both roles serve Bolt — and with it the metrics endpoint,
// which Memgraph serves from the same server context — over TLS from a
// certificate the user supplies. It is one-way, server-authenticated TLS:
// Memgraph presents the certificate and never asks a client for one, so what
// a client gets is confidentiality of every Bolt byte and, if it verifies,
// proof it reached the real cluster. Verification is the client's business
// and needs the CA on the client's side, never on Memgraph's.
//
// One Secret serves every pod of both roles. A StatefulSet has one pod
// template, so per-pod Secrets cannot be expressed, and per-pod certificates
// would buy nothing: a Bolt certificate carries as many SANs as its clients
// dial, and no client tells one member from another by certificate. The SANs
// must therefore cover every address a verifying client dials: the external
// address the operator announces as bolt_server on an exposed cluster, and
// for clients inside Kubernetes the pod DNS names, which one wildcard per
// role covers (*.<cluster>-coordinator.<namespace>.svc.<clusterDomain> and
// *.<cluster>-data.<namespace>.svc.<clusterDomain>).
//
// Three things follow from the block, all derived and none a knob. The
// operator dials the coordinators over TLS without verifying the certificate:
// verifying would force every Bolt certificate to carry a CA and pod-DNS SANs,
// and buys nothing while Bolt is unauthenticated. The ServiceMonitor, when
// asked for, scrapes over https with verification skipped, for the same
// reason. And the block may be added to or removed from a live cluster: it is
// an ordinary pod-template change, rolled one pod at a time, during which the
// operator dials whichever of the two modes a coordinator still speaks.
//
// Rotation is not the operator's job. The Secret is mounted without subPath,
// so an in-place update reaches every pod's files within the kubelet's sync
// period; Memgraph then picks them up on RELOAD BOLT SERVER TLS, issued on
// every instance. Pointing at a differently named Secret is a pod-template
// change and rolls the cluster instead.
type BoltTLSSpec struct {
	// secretName names a Secret in the cluster's namespace holding the
	// certificate under tls.crt and the private key under tls.key — the shape
	// of a kubernetes.io/tls Secret, which kubectl create secret tls and a
	// cert-manager Certificate both produce. Both roles mount it read-only and
	// serve Bolt and metrics with it.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	// +required
	SecretName string `json:"secretName"`
}

// IntraClusterTLSSpec makes the members of the cluster talk to each other
// over mutual TLS: replication, the coordinator-to-instance management RPC
// and Raft between coordinators. On every such connection both sides present
// a certificate and verify the other's against the CA, and the server side
// refuses a peer presenting none. What that proves is membership — the peer
// holds a certificate the cluster's CA signed — not identity: Memgraph checks
// no member hostname on this path, so every pod of both roles shares one
// certificate and per-pod certificates would add nothing it could check. A
// wildcard SAN per role (*.<cluster>-coordinator.<namespace>.svc.<clusterDomain>
// and *.<cluster>-data...) is recommended regardless, so a later hostname check
// in Memgraph costs nothing.
//
// The mode is all-or-nothing per process, and a member with it cannot talk
// to a member without it. The operator replaces pods one at a time and gates
// every step on replication lag, so turning the mode on or off on a live
// cluster would deadlock at the first step: the restarted replica speaks TLS,
// the still-plaintext MAIN can no longer replicate to it, and lag never
// converges. Adding or removing the block on a live cluster is therefore
// refused at admission (the rule lives on the spec, where both the old and
// the new tls block are in view). Changing secretName inside the block is
// allowed and rolls the cluster; both Secrets must then chain to a CA the
// other side trusts, so a CA cut-over needs ca.crt in both to be a bundle of
// the old and the new CA.
//
// Rotation is as for bolt: the Secret is mounted without subPath, an
// in-place update reaches every pod's files, Raft picks them up on its own
// and the rest on RELOAD INTRA_CLUSTER TLS, issued on every instance.
type IntraClusterTLSSpec struct {
	// secretName names a Secret in the cluster's namespace holding the
	// certificate under tls.crt, the private key under tls.key and the CA to
	// verify peers against under ca.crt — what a cert-manager Certificate
	// issued by a private CA writes. Every pod of both roles mounts it
	// read-only.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	// +required
	SecretName string `json:"secretName"`
}

// TLSSpec holds the cluster's TLS modes, each an optional presence-based block
// like externalAccess and monitoring: present, the mode is on; absent, off.
// The two are independent: Bolt TLS faces clients, intra-cluster TLS faces
// the members, and either works without the other.
type TLSSpec struct {
	// bolt serves Bolt and the metrics endpoint over TLS on both roles, from
	// the certificate in the named Secret.
	// +optional
	Bolt *BoltTLSSpec `json:"bolt,omitempty"`

	// intraCluster makes the members talk to each other over mutual TLS from
	// the certificate and CA in the named Secret. It cannot be added to or
	// removed from a live cluster; see IntraClusterTLSSpec.
	// +optional
	IntraCluster *IntraClusterTLSSpec `json:"intraCluster,omitempty"`
}

// MemgraphClusterSpec defines the desired state of MemgraphCluster.
//
// The one rule here spans two blocks: with type Gateway every data instance
// gets a listener on gateway.dataPortBase + ordinal, so the declared count
// decides whether the range fits in the port space. The has() guards keep the
// rule evaluable before the nested defaults apply.
//
// The transition rule pins the presence of tls.intraCluster. It lives here
// rather than on TLSSpec because a rule on the block only fires when the
// block existed before: adding tls with intraCluster inside to a cluster that
// had no tls block, or removing the whole block, would slip past it.
//
// +kubebuilder:validation:XValidation:rule="!has(self.externalAccess) || !has(self.externalAccess.gateway) || !has(self.externalAccess.gateway.dataPortBase) || !has(self.dataInstances) || self.externalAccess.gateway.dataPortBase + self.dataInstances <= 65536",message="externalAccess.gateway.dataPortBase + dataInstances must not exceed 65536: every data instance listens on dataPortBase + its ordinal"
// +kubebuilder:validation:XValidation:rule="(has(self.tls) && has(self.tls.intraCluster)) == (has(oldSelf.tls) && has(oldSelf.tls.intraCluster))",message="tls.intraCluster cannot be added or removed on a live cluster: a member with intra-cluster TLS cannot talk to one without it, so restarting pods one at a time would deadlock waiting for replication that can no longer happen. Delete the MemgraphCluster (its claims are retained under the default retention policy) and recreate it with the new setting"
// +kubebuilder:validation:XValidation:rule="!has(self.fixOwnershipInitContainer) || !has(self.securityContext) || (has(self.securityContext.runAsUser) && (has(self.securityContext.runAsGroup) || has(self.securityContext.fsGroup)))",message="fixOwnershipInitContainer needs securityContext to name runAsUser and one of runAsGroup or fsGroup: the container can only chown the volumes to an identity the cluster names, not to one the platform assigns at admission"
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

	// coreDumps collects crash dumps of the roles whose blocks are present onto
	// a volume of their own.
	// +kubebuilder:default={}
	// +optional
	CoreDumps CoreDumpsSpec `json:"coreDumps,omitzero"`

	// sysctlInitContainer raises the node's vm.max_map_count from a privileged
	// init container in every pod, as the memgraph-high-availability Helm
	// chart does. Absent, no such container runs and Memgraph warns at startup
	// if the node's value is below what it needs; leave it out where privileged
	// containers are not allowed.
	// +optional
	SysctlInitContainer *SysctlInitContainerSpec `json:"sysctlInitContainer,omitempty"`

	// securityContext is the uid, gid and fsGroup every pod of both roles runs
	// under. Absent, the operator writes the Memgraph images' 101, 103 and 103;
	// present, exactly the fields written and no other, so an empty block
	// leaves all three to the platform, which is what OpenShift's restricted-v2
	// SCC requires.
	// +optional
	SecurityContext *PodSecurityContextSpec `json:"securityContext,omitempty"`

	// fixOwnershipInitContainer chowns every pod's volume mount points to the
	// memgraph user from a root init container before Memgraph starts, as the
	// memgraph-high-availability Helm chart does, for storage drivers that do
	// not honor the pod's fsGroup. Absent, no such container runs.
	// +optional
	FixOwnershipInitContainer *FixOwnershipInitContainerSpec `json:"fixOwnershipInitContainer,omitempty"`

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

	// scheduling decides where both roles' pods land: the operator's own
	// anti-affinity rule, and per role the node selector, tolerations,
	// topology spread constraints, extra anti-affinity and priority class.
	// +optional
	Scheduling SchedulingSpec `json:"scheduling,omitzero"`

	// extraEnv passes additional non-secret environment variables to both
	// roles' Memgraph containers.
	// +optional
	ExtraEnv ExtraEnvSpec `json:"extraEnv,omitzero"`

	// flags passes Memgraph flags to both roles, by name. A flag Memgraph can
	// change at run time is applied to every instance without a restart; any
	// other flag change rolls the pods.
	// +optional
	Flags FlagsSpec `json:"flags,omitzero"`

	// extraVolumes adds pod volumes to both roles beyond the ones the operator
	// provisions.
	// +optional
	ExtraVolumes ExtraVolumesSpec `json:"extraVolumes,omitzero"`

	// extraVolumeMounts mounts volumes into both roles' Memgraph containers
	// beyond the ones the operator mounts.
	// +optional
	ExtraVolumeMounts ExtraVolumeMountsSpec `json:"extraVolumeMounts,omitzero"`

	// userContainers adds containers of your own to both roles' pods beside
	// the Memgraph container.
	// +optional
	UserContainers UserContainersSpec `json:"userContainers,omitzero"`

	// initContainers adds init containers of your own to both roles' pods,
	// run after the operator's own and before Memgraph.
	// +optional
	InitContainers InitContainersSpec `json:"initContainers,omitzero"`

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

	// tls turns on the cluster's TLS modes from certificates in Secrets the
	// user supplies. Absent, every port speaks plaintext. bolt may be added or
	// removed on a live cluster; intraCluster may not.
	// +optional
	TLS *TLSSpec `json:"tls,omitempty"`
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
