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

	DefaultLibPVCSize        = "1Gi"
	DefaultLogPVCSize        = "1Gi"
	DefaultStorageAccessMode = corev1.ReadWriteOnce
	DefaultStorageRetention  = RetentionPolicyRetain
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
// (a MAIN is elected and reachable); Converged answers "does registration
// match the declared topology" (every coordinator and data instance is
// registered). A cluster can be Ready but not Converged — a MAIN still serves
// while a lost replica registration is being restored.
const (
	// ConditionReady is True when a MAIN data instance is elected and the
	// coordinator leader is reachable.
	ConditionReady = "Ready"

	// ConditionConverged is True when the observed cluster matches the declared
	// topology and no registration commands are pending.
	ConditionConverged = "Converged"
)

// Condition reasons reported on MemgraphCluster status. Reasons are CamelCase
// per Kubernetes API conventions and are stable enough for tooling to gate on.
const (
	// ReasonWorkloadsNotReady is set while not every workload pod is ready, so
	// registration has not been attempted.
	ReasonWorkloadsNotReady = "WorkloadsNotReady"

	// ReasonCoordinatorUnreachable is set when no coordinator answered
	// SHOW INSTANCES, so the cluster state cannot be observed.
	ReasonCoordinatorUnreachable = "CoordinatorUnreachable"

	// ReasonRegistrationInProgress is set while registration commands are being
	// issued to converge the cluster toward the declared topology.
	ReasonRegistrationInProgress = "RegistrationInProgress"

	// ReasonAllInstancesRegistered is set when the observed cluster matches the
	// declared topology.
	ReasonAllInstancesRegistered = "AllInstancesRegistered"

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

// MemgraphClusterSpec defines the desired state of MemgraphCluster.
//
// Port and pod-tuning fields land in subsequent slices of the operator MVP
// (see specs/operator-mvp/PRD.md).
type MemgraphClusterSpec struct {
	// coordinators is the number of Raft coordinator instances. It must be odd
	// so the Raft quorum cannot split, and it is immutable: scaling is not
	// supported in v1alpha1.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:XValidation:rule="self % 2 == 1",message="coordinators must be an odd number so the Raft quorum cannot split"
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="coordinators is immutable: changing the coordinator count of an existing MemgraphCluster is not supported in v1alpha1"
	// +kubebuilder:default=3
	// +optional
	Coordinators *int32 `json:"coordinators,omitempty"`

	// dataInstances is the number of data instances. It is immutable: scaling
	// is not supported in v1alpha1.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="dataInstances is immutable: changing the data instance count of an existing MemgraphCluster is not supported in v1alpha1"
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
