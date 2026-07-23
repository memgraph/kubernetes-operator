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
	corev1 "k8s.io/api/core/v1"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
)

// Internal Memgraph ports. These mirror the memgraph-high-availability Helm
// chart's defaults and become spec knobs in a later slice.
const (
	BoltPort        int32 = 7687
	ManagementPort  int32 = 10000
	ReplicationPort int32 = 20000
	CoordinatorPort int32 = 12000
)

const (
	// clusterDomain is the Kubernetes cluster domain used in advertised FQDN
	// addresses. It becomes a spec knob in a later slice.
	clusterDomain = "cluster.local"

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

// labels returns the full label set stamped on all objects of a role.
func labels(cluster *memgraphcomv1alpha1.MemgraphCluster, component string) map[string]string {
	l := selectorLabels(cluster, component)
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
// to its CRD schema default, so builders behave correctly on specs that never
// passed admission.
type normalizedSpec struct {
	coordinators    int32
	dataInstances   int32
	image           string
	pullPolicy      corev1.PullPolicy
	secretName      string
	licenseKey      string
	organizationKey string
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
	}
	if spec.Coordinators != nil {
		n.coordinators = *spec.Coordinators
	}
	if spec.DataInstances != nil {
		n.dataInstances = *spec.DataInstances
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
