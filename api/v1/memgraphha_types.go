/*
Copyright 2024 Memgraph Ltd.

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

/*
In some way this file simulates types.go from k8s.io/api/apps/v1 to define new resources
we are using.
*/

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// MemgraphHASpec defines the desired state of MemgraphHA
type MemgraphHASpec struct {
	Coordinators []Coordinator  `json:"coordinators"`
	Data         []DataItem     `json:"data"`
	Memgraph     MemgraphConfig `json:"memgraph"`
}

type Coordinator struct {
	ID              string   `json:"id"`
	BoltPort        int      `json:"boltPort"`
	ManagementPort  int      `json:"managementPort"`
	CoordinatorPort int      `json:"coordinatorPort"`
	Args            []string `json:"args"`
}

type DataItem struct {
	ID              string   `json:"id"`
	BoltPort        int      `json:"boltPort"`
	ManagementPort  int      `json:"managementPort"`
	ReplicationPort int      `json:"replicationPort"`
	Args            []string `json:"args"`
}

type MemgraphConfig struct {
	Data         MemgraphDataConfig         `json:"data"`
	Coordinators MemgraphCoordinatorsConfig `json:"coordinators"`
	Env          map[string]string          `json:"env"`
	Image        ImageConfig                `json:"image"`
	Probes       MemgraphProbesConfig       `json:"probes"`
}

type MemgraphDataConfig struct {
	VolumeClaim VolumeClaimConfig `json:"volumeClaim"`
}

type MemgraphCoordinatorsConfig struct {
	VolumeClaim VolumeClaimConfig `json:"volumeClaim"`
}

type VolumeClaimConfig struct {
	StoragePVCClassName string `json:"storagePVCClassName"`
	StoragePVC          bool   `json:"storagePVC"`
	StoragePVCSize      string `json:"storagePVCSize"`
	LogPVCClassName     string `json:"logPVCClassName"`
	LogPVC              bool   `json:"logPVC"`
	LogPVCSize          string `json:"logPVCSize"`
}

type ImageConfig struct {
	PullPolicy string `json:"pullPolicy"`
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
}

type MemgraphProbesConfig struct {
	Liveness  ProbeConfig `json:"liveness"`
	Readiness ProbeConfig `json:"readiness"`
	Startup   ProbeConfig `json:"startup"`
}

// ProbeConfig configures individual probes
type ProbeConfig struct {
	InitialDelaySeconds int `json:"initialDelaySeconds"`
	PeriodSeconds       int `json:"periodSeconds"`
	FailureThreshold    int `json:"failureThreshold,omitempty"`
}

// MemgraphHAStatus defines the observed state of MemgraphHA
type MemgraphHAStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// MemgraphHA is the Schema for the memgraphhas API
/*
Every Kind needs to have two structures: metav1.TypeMeta and metav1.ObjectMeta.
TypeMeta structure contains information about the GVK of the Kind.
ObjectMeta contains metadata for the Kind.
*/
type MemgraphHA struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MemgraphHASpec   `json:"spec,omitempty"`
	Status MemgraphHAStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// MemgraphHAList contains a list of MemgraphHA
type MemgraphHAList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MemgraphHA `json:"items"`
}

func init() {
	// A Scheme is an abstraction used to register the API objects
	// as Group-Version-Kinds, convert between API Objects of various
	// versions and serialize/deserialize API Objects
	SchemeBuilder.Register(&MemgraphHA{}, &MemgraphHAList{})
}
