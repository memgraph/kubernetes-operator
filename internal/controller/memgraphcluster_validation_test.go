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

package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
)

// These specs exercise the CRD schema itself — defaults, creation-time
// validation and the CEL transition rules that pin the topology counts. They
// never reconcile: the API server is the unit under test, which is exactly the
// v1 contract that no admission webhook is involved.
// defaultRoleStorage is one role's storage block as the CRD schema defaults
// materialize it.
func defaultRoleStorage() memgraphcomv1alpha1.RoleStorageSpec {
	return memgraphcomv1alpha1.RoleStorageSpec{
		LibPVCSize:            ptr.To(resource.MustParse(memgraphcomv1alpha1.DefaultLibPVCSize)),
		LibStorageAccessMode:  memgraphcomv1alpha1.DefaultStorageAccessMode,
		CreateLogStorageClaim: ptr.To(memgraphcomv1alpha1.DefaultCreateLogStorageClaim),
		LogPVCSize:            ptr.To(resource.MustParse(memgraphcomv1alpha1.DefaultLogPVCSize)),
		LogStorageAccessMode:  memgraphcomv1alpha1.DefaultStorageAccessMode,
	}
}

// defaultRoleCoreDumps is one role's core dumps block as the CRD schema
// defaults materialize it: off, but with the size it would ask for.
func defaultRoleCoreDumps() memgraphcomv1alpha1.RoleCoreDumpsSpec {
	return memgraphcomv1alpha1.RoleCoreDumpsSpec{
		Size: ptr.To(resource.MustParse(memgraphcomv1alpha1.DefaultCoreDumpsSize)),
	}
}

// defaultCoreDumps is the whole core dumps block as the CRD schema defaults
// materialize it.
func defaultCoreDumps() memgraphcomv1alpha1.CoreDumpsSpec {
	return memgraphcomv1alpha1.CoreDumpsSpec{
		Coordinators:         defaultRoleCoreDumps(),
		Data:                 defaultRoleCoreDumps(),
		ConfigureCorePattern: ptr.To(memgraphcomv1alpha1.DefaultConfigureCorePattern),
	}
}

// defaultPorts are the internal ports as the CRD schema defaults materialize
// them.
func defaultPorts() memgraphcomv1alpha1.PortsSpec {
	return memgraphcomv1alpha1.PortsSpec{
		BoltPort:        ptr.To(memgraphcomv1alpha1.DefaultBoltPort),
		ManagementPort:  ptr.To(memgraphcomv1alpha1.DefaultManagementPort),
		ReplicationPort: ptr.To(memgraphcomv1alpha1.DefaultReplicationPort),
		CoordinatorPort: ptr.To(memgraphcomv1alpha1.DefaultCoordinatorPort),
	}
}

var _ = Describe("MemgraphCluster CRD validation", func() {
	const resourceNamespace = "default"

	ctx := context.Background()

	// create posts a cluster and returns the admission error, if any.
	create := func(name string, spec memgraphcomv1alpha1.MemgraphClusterSpec) error {
		return k8sClient.Create(ctx, &memgraphcomv1alpha1.MemgraphCluster{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: resourceNamespace},
			Spec:       spec,
		})
	}

	// createAccepted posts a cluster, asserts admission accepted it, registers
	// its cleanup and returns the stored object with defaults applied.
	createAccepted := func(name string, spec memgraphcomv1alpha1.MemgraphClusterSpec) *memgraphcomv1alpha1.MemgraphCluster {
		GinkgoHelper()
		Expect(create(name, spec)).To(Succeed())

		stored := &memgraphcomv1alpha1.MemgraphCluster{}
		Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: name, Namespace: resourceNamespace}, stored)).To(Succeed())
		DeferCleanup(func() {
			Expect(k8sClient.Delete(ctx, stored)).To(Succeed())
		})
		return stored
	}

	// expectRejected asserts that admission rejected the spec as invalid and
	// that the message actually tells the user what to change.
	expectRejected := func(name string, spec memgraphcomv1alpha1.MemgraphClusterSpec, wantMessage string) {
		GinkgoHelper()
		err := create(name, spec)
		Expect(err).To(HaveOccurred(), "expected admission to reject the spec")
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected an Invalid error, got %v", err)
		Expect(err.Error()).To(ContainSubstring(wantMessage))
	}

	Context("when creating a cluster", func() {
		It("should accept the minimal spec of counts, image and secrets", func() {
			stored := createAccepted("valid-minimal-explicit", memgraphcomv1alpha1.MemgraphClusterSpec{
				Coordinators:  ptr.To(int32(3)),
				DataInstances: ptr.To(int32(2)),
				Image: memgraphcomv1alpha1.ImageSpec{
					Repository: "docker.io/memgraph/memgraph",
					Tag:        "3.12.0-relwithdebinfo",
				},
				Secrets: memgraphcomv1alpha1.SecretsSpec{Name: customSecretName},
			})

			Expect(stored.Spec.Secrets.Name).To(Equal(customSecretName))
		})

		It("should accept an empty spec and materialize every documented default", func() {
			stored := createAccepted("valid-empty-spec", memgraphcomv1alpha1.MemgraphClusterSpec{})

			Expect(stored.Spec).To(Equal(memgraphcomv1alpha1.MemgraphClusterSpec{
				Coordinators:  ptr.To(memgraphcomv1alpha1.DefaultCoordinatorCount),
				DataInstances: ptr.To(memgraphcomv1alpha1.DefaultDataInstanceCount),
				Image: memgraphcomv1alpha1.ImageSpec{
					Repository: memgraphcomv1alpha1.DefaultImageRepository,
					Tag:        memgraphcomv1alpha1.DefaultImageTag,
					PullPolicy: memgraphcomv1alpha1.DefaultImagePullPolicy,
				},
				Secrets: memgraphcomv1alpha1.SecretsSpec{
					Name:            memgraphcomv1alpha1.DefaultSecretName,
					LicenseKey:      memgraphcomv1alpha1.DefaultLicenseSecretKey,
					OrganizationKey: memgraphcomv1alpha1.DefaultOrganizationSecretKey,
				},
				Storage: memgraphcomv1alpha1.StorageSpec{
					RetentionPolicy: memgraphcomv1alpha1.DefaultStorageRetention,
					Coordinators:    defaultRoleStorage(),
					Data:            defaultRoleStorage(),
				},
				CoreDumps:     defaultCoreDumps(),
				ClusterDomain: memgraphcomv1alpha1.DefaultClusterDomain,
				Ports:         defaultPorts(),
				// Probes, resources, labels and the env/args passthrough have no
				// schema defaults: the probe timings' defaults depend on the role
				// and the rest default to "nothing added".
			}), "the CRD schema defaults must match the Go constants the builders fall back to")
		})

		It("should default the fields a partially specified block leaves out", func() {
			stored := createAccepted("valid-partial-blocks", memgraphcomv1alpha1.MemgraphClusterSpec{
				Image:   memgraphcomv1alpha1.ImageSpec{Tag: customImageTag},
				Secrets: memgraphcomv1alpha1.SecretsSpec{Name: customSecretName},
				Storage: memgraphcomv1alpha1.StorageSpec{
					Data: memgraphcomv1alpha1.RoleStorageSpec{LibPVCSize: ptr.To(resource.MustParse("100Gi"))},
				},
			})

			Expect(stored.Spec.Image.Tag).To(Equal(customImageTag))
			Expect(stored.Spec.Image.Repository).To(Equal(memgraphcomv1alpha1.DefaultImageRepository))
			Expect(stored.Spec.Secrets.LicenseKey).To(Equal(memgraphcomv1alpha1.DefaultLicenseSecretKey))
			Expect(stored.Spec.Secrets.OrganizationKey).To(Equal(memgraphcomv1alpha1.DefaultOrganizationSecretKey))
			Expect(stored.Spec.Storage.Data.LibPVCSize).To(Equal(ptr.To(resource.MustParse("100Gi"))))
			Expect(stored.Spec.Storage.Data.LogPVCSize).To(
				Equal(ptr.To(resource.MustParse(memgraphcomv1alpha1.DefaultLogPVCSize))))
			Expect(stored.Spec.Storage.RetentionPolicy).To(Equal(memgraphcomv1alpha1.DefaultStorageRetention))
			Expect(stored.Spec.Storage.Coordinators).To(Equal(defaultRoleStorage()))
		})

		It("should accept an explicit Delete retention policy", func() {
			stored := createAccepted("valid-retention-delete", memgraphcomv1alpha1.MemgraphClusterSpec{
				Storage: memgraphcomv1alpha1.StorageSpec{
					RetentionPolicy: memgraphcomv1alpha1.RetentionPolicyDelete,
				},
			})

			Expect(stored.Spec.Storage.RetentionPolicy).To(Equal(memgraphcomv1alpha1.RetentionPolicyDelete))
		})

		// An unset storage class means "cluster default" and an empty one means
		// "no dynamic provisioning"; both must survive a round trip through the
		// API server as distinct values.
		It("should preserve the difference between an unset and an empty storage class", func() {
			raw := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": memgraphcomv1alpha1.SchemeGroupVersion.String(),
				"kind":       "MemgraphCluster",
				"metadata":   map[string]any{"name": "valid-empty-storage-class", "namespace": resourceNamespace},
				"spec": map[string]any{
					"storage": map[string]any{
						"data": map[string]any{"libStorageClassName": ""},
					},
				},
			}}
			Expect(k8sClient.Create(ctx, raw)).To(Succeed())
			DeferCleanup(func() { Expect(k8sClient.Delete(ctx, raw)).To(Succeed()) })

			stored := &memgraphcomv1alpha1.MemgraphCluster{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "valid-empty-storage-class", Namespace: resourceNamespace}, stored)).To(Succeed())

			Expect(stored.Spec.Storage.Data.LibStorageClassName).To(Equal(ptr.To("")))
			Expect(stored.Spec.Storage.Data.LogStorageClassName).To(BeNil())
		})

		DescribeTable("should accept any odd coordinator count and any positive data instance count",
			func(name string, coordinators, dataInstances int32) {
				createAccepted(name, memgraphcomv1alpha1.MemgraphClusterSpec{
					Coordinators:  ptr.To(coordinators),
					DataInstances: ptr.To(dataInstances),
				})
			},
			Entry("single coordinator, single data instance", "valid-topology-min", int32(1), int32(1)),
			Entry("a quorum and replica count beyond the former upper bounds", "valid-topology-large",
				int32(9), int32(16)),
		)

		It("should accept a fully tuned pod configuration", func() {
			stored := createAccepted("valid-pod-tuning", memgraphcomv1alpha1.MemgraphClusterSpec{
				ClusterDomain: "k8s.example.com",
				Ports: memgraphcomv1alpha1.PortsSpec{
					BoltPort:        ptr.To(int32(7777)),
					ManagementPort:  ptr.To(int32(10001)),
					ReplicationPort: ptr.To(int32(20001)),
					CoordinatorPort: ptr.To(int32(12001)),
				},
				Probes: memgraphcomv1alpha1.ProbesSpec{
					Data: memgraphcomv1alpha1.RoleProbesSpec{
						StartupProbe: memgraphcomv1alpha1.ProbeSpec{FailureThreshold: ptr.To(int32(4320))},
					},
				},
				Resources: memgraphcomv1alpha1.ResourcesSpec{
					Data: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("4Gi")},
					},
				},
				Labels: memgraphcomv1alpha1.LabelsSpec{
					Data: memgraphcomv1alpha1.RoleLabelsSpec{PodLabels: map[string]string{"team": "data"}},
				},
				ExtraEnv: memgraphcomv1alpha1.ExtraEnvSpec{
					Data: []memgraphcomv1alpha1.EnvVar{{Name: "DATA_LABEL_ONE", Value: "one"}},
				},
				ExtraArgs: memgraphcomv1alpha1.ExtraArgsSpec{
					Data: []string{"--storage-snapshot-on-exit=true"},
				},
			})

			Expect(stored.Spec.ClusterDomain).To(Equal("k8s.example.com"))
			Expect(stored.Spec.Ports.BoltPort).To(HaveValue(Equal(int32(7777))))
			Expect(stored.Spec.Probes.Data.StartupProbe.FailureThreshold).To(HaveValue(Equal(int32(4320))))
			Expect(stored.Spec.Probes.Data.ReadinessProbe).To(Equal(memgraphcomv1alpha1.ProbeSpec{}),
				"an unset probe stays unset; its defaults are resolved by the builders, not the schema")
			Expect(stored.Spec.ExtraEnv.Data).To(HaveLen(1))
			Expect(stored.Spec.ExtraArgs.Data).To(ConsistOf("--storage-snapshot-on-exit=true"))
		})

		It("should default the ports a partially specified block leaves out", func() {
			stored := createAccepted("valid-partial-ports", memgraphcomv1alpha1.MemgraphClusterSpec{
				Ports: memgraphcomv1alpha1.PortsSpec{BoltPort: ptr.To(int32(7777))},
			})

			Expect(stored.Spec.Ports.BoltPort).To(HaveValue(Equal(int32(7777))))
			Expect(stored.Spec.Ports.ManagementPort).To(HaveValue(Equal(memgraphcomv1alpha1.DefaultManagementPort)))
			Expect(stored.Spec.Ports.ReplicationPort).To(HaveValue(Equal(memgraphcomv1alpha1.DefaultReplicationPort)))
			Expect(stored.Spec.Ports.CoordinatorPort).To(HaveValue(Equal(memgraphcomv1alpha1.DefaultCoordinatorPort)))
		})

		It("should accept core dumps with an uploader and default what it leaves out", func() {
			stored := createAccepted("valid-core-dumps-uploader", memgraphcomv1alpha1.MemgraphClusterSpec{
				CoreDumps: memgraphcomv1alpha1.CoreDumpsSpec{
					Data: memgraphcomv1alpha1.RoleCoreDumpsSpec{
						Enabled: true,
						Size:    ptr.To(resource.MustParse("200Gi")),
					},
					Uploader: &memgraphcomv1alpha1.CoreDumpsUploaderSpec{
						Image:          uploaderImage,
						Env:            []memgraphcomv1alpha1.EnvVar{{Name: "S3_BUCKET", Value: "dumps"}},
						EnvFromSecrets: []string{"aws-s3-credentials"},
					},
				},
			})

			dumps := stored.Spec.CoreDumps
			Expect(dumps.Data.Enabled).To(BeTrue())
			Expect(dumps.Data.Size).To(HaveValue(Equal(resource.MustParse("200Gi"))))
			Expect(dumps.ConfigureCorePattern).To(HaveValue(BeTrue()))
			Expect(dumps.Uploader.PullPolicy).To(Equal(memgraphcomv1alpha1.DefaultImagePullPolicy))
			// Whether a role collects at all, and how much room it needs, stays
			// its own decision: the coordinators asked for neither.
			Expect(dumps.Coordinators).To(Equal(defaultRoleCoreDumps()))
		})

		It("should accept a registry host carrying a port", func() {
			stored := createAccepted("valid-registry-port", memgraphcomv1alpha1.MemgraphClusterSpec{
				Image: memgraphcomv1alpha1.ImageSpec{Repository: "registry.example.com:5000/memgraph"},
			})

			Expect(stored.Spec.Image.Repository).To(Equal("registry.example.com:5000/memgraph"))
		})

		DescribeTable("should reject an invalid spec with an actionable message",
			func(name string, spec memgraphcomv1alpha1.MemgraphClusterSpec, wantMessage string) {
				expectRejected(name, spec, wantMessage)
			},
			Entry("zero coordinators", "invalid-coordinators-zero",
				memgraphcomv1alpha1.MemgraphClusterSpec{Coordinators: ptr.To(int32(0))},
				"should be greater than or equal to 1"),
			Entry("an even coordinator count", "invalid-coordinators-even",
				memgraphcomv1alpha1.MemgraphClusterSpec{Coordinators: ptr.To(int32(2))},
				"coordinators must be an odd number"),
			Entry("zero data instances", "invalid-data-zero",
				memgraphcomv1alpha1.MemgraphClusterSpec{DataInstances: ptr.To(int32(0))},
				"should be greater than or equal to 1"),
			Entry("a tag smuggled into the repository", "invalid-image-repository-tagged",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Image: memgraphcomv1alpha1.ImageSpec{Repository: "memgraph/memgraph:3.12.0"},
				},
				"repository must not contain a tag; set image.tag instead"),
			Entry("a digest smuggled into the repository", "invalid-image-repository-digest",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Image: memgraphcomv1alpha1.ImageSpec{
						Repository: "memgraph/memgraph@sha256:0000000000000000000000000000000000000000000000000000000000000000",
					},
				},
				"repository must not contain a digest"),
			Entry("an image tag that is not a valid OCI tag", "invalid-image-tag-chars",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Image: memgraphcomv1alpha1.ImageSpec{Tag: "3.12.0 relwithdebinfo"},
				},
				"in body should match"),
			Entry("a secret name that is not a DNS subdomain", "invalid-secret-name",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Secrets: memgraphcomv1alpha1.SecretsSpec{Name: "My_Secret"},
				},
				"in body should match"),
			Entry("one secret key serving both values", "invalid-secret-keys-collide",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Secrets: memgraphcomv1alpha1.SecretsSpec{
						LicenseKey:      "MEMGRAPH_LICENSE",
						OrganizationKey: "MEMGRAPH_LICENSE",
					},
				},
				"licenseKey and organizationKey must name different keys of the Secret"),
			Entry("a retention policy outside the enum", "invalid-retention-policy",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Storage: memgraphcomv1alpha1.StorageSpec{RetentionPolicy: "Purge"},
				},
				`Unsupported value: "Purge"`),
			Entry("an access mode outside the enum", "invalid-access-mode",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Storage: memgraphcomv1alpha1.StorageSpec{
						Data: memgraphcomv1alpha1.RoleStorageSpec{LibStorageAccessMode: "ReadWriteSometimes"},
					},
				},
				`Unsupported value: "ReadWriteSometimes"`),
			Entry("a port outside the valid range", "invalid-port-range",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Ports: memgraphcomv1alpha1.PortsSpec{BoltPort: ptr.To(int32(70000))},
				},
				"should be less than or equal to 65535"),
			Entry("a port of zero", "invalid-port-zero",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Ports: memgraphcomv1alpha1.PortsSpec{ManagementPort: ptr.To(int32(0))},
				},
				"should be greater than or equal to 1"),
			// Two roles sharing a port number would make the advertised
			// addresses ambiguous, so it is rejected instead of half-working.
			Entry("two ports colliding", "invalid-ports-collide",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Ports: memgraphcomv1alpha1.PortsSpec{ManagementPort: ptr.To(memgraphcomv1alpha1.DefaultBoltPort)},
				},
				"must all be different ports"),
			Entry("a cluster domain that is not a DNS name", "invalid-cluster-domain",
				memgraphcomv1alpha1.MemgraphClusterSpec{ClusterDomain: "Cluster_Local"},
				"in body should match"),
			// An uploader with no volume to read would poll an empty directory
			// forever, so the dependency the Helm chart leaves implicit between
			// its two blocks is enforced here.
			Entry("an uploader with no role collecting dumps", "invalid-uploader-without-dumps",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					CoreDumps: memgraphcomv1alpha1.CoreDumpsSpec{
						Uploader: &memgraphcomv1alpha1.CoreDumpsUploaderSpec{Image: uploaderImage},
					},
				},
				"uploader requires core dumps enabled for at least one role"),
			Entry("an uploader without an image", "invalid-uploader-no-image",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					CoreDumps: memgraphcomv1alpha1.CoreDumpsSpec{
						Data:     memgraphcomv1alpha1.RoleCoreDumpsSpec{Enabled: true},
						Uploader: &memgraphcomv1alpha1.CoreDumpsUploaderSpec{},
					},
				},
				"should be at least 1 chars long"),
			Entry("an uploader shadowing the core dumps path variable", "invalid-uploader-env",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					CoreDumps: memgraphcomv1alpha1.CoreDumpsSpec{
						Coordinators: memgraphcomv1alpha1.RoleCoreDumpsSpec{Enabled: true},
						Uploader: &memgraphcomv1alpha1.CoreDumpsUploaderSpec{
							Image: uploaderImage,
							Env: []memgraphcomv1alpha1.EnvVar{{
								Name: memgraphcomv1alpha1.EnvCoreDumpsDir, Value: "/elsewhere",
							}},
						},
					},
				},
				"env must not set CORE_DUMPS_DIR"),
			Entry("an env var name that is not a shell identifier", "invalid-env-name",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					ExtraEnv: memgraphcomv1alpha1.ExtraEnvSpec{
						Data: []memgraphcomv1alpha1.EnvVar{{Name: "not-an-identifier", Value: "x"}},
					},
				},
				"in body should match"),
			Entry("an env var shadowing the license the secrets block owns", "invalid-env-license",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					ExtraEnv: memgraphcomv1alpha1.ExtraEnvSpec{
						Data: []memgraphcomv1alpha1.EnvVar{{
							Name:  memgraphcomv1alpha1.EnvLicense,
							Value: "smuggled-license",
						}},
					},
				},
				"they come from the secrets block"),
			Entry("an env var shadowing the pod's own identity", "invalid-env-pod-name",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					ExtraEnv: memgraphcomv1alpha1.ExtraEnvSpec{
						Coordinators: []memgraphcomv1alpha1.EnvVar{{
							Name:  memgraphcomv1alpha1.EnvPodName,
							Value: "not-my-name",
						}},
					},
				},
				"it carries the pod's own identity"),
			// A port set through extraArgs would leave the pods listening
			// somewhere the registered addresses do not point.
			Entry("an extra arg overriding a port", "invalid-args-port",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					ExtraArgs: memgraphcomv1alpha1.ExtraArgsSpec{Data: []string{"--bolt-port=7777"}},
				},
				"configure ports through spec.ports"),
			Entry("an extra arg overriding the coordinator identity", "invalid-args-coordinator-id",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					ExtraArgs: memgraphcomv1alpha1.ExtraArgsSpec{Coordinators: []string{"--coordinator-id=9"}},
				},
				"the coordinator identity the operator derives"),
			Entry("a probe timing below one", "invalid-probe-period",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Probes: memgraphcomv1alpha1.ProbesSpec{
						Coordinators: memgraphcomv1alpha1.RoleProbesSpec{
							ReadinessProbe: memgraphcomv1alpha1.ProbeSpec{PeriodSeconds: ptr.To(int32(0))},
						},
					},
				},
				"should be greater than or equal to 1"),
		)

		// Two extra env vars of the same name would be an ambiguous
		// configuration, so the list is keyed by name.
		It("should reject a repeated env var name", func() {
			err := create("invalid-env-duplicate", memgraphcomv1alpha1.MemgraphClusterSpec{
				ExtraEnv: memgraphcomv1alpha1.ExtraEnvSpec{
					Data: []memgraphcomv1alpha1.EnvVar{
						{Name: "DATA_LABEL", Value: "one"},
						{Name: "DATA_LABEL", Value: "two"},
					},
				},
			})

			Expect(err).To(HaveOccurred(), "expected admission to reject the duplicate")
			Expect(err.Error()).To(ContainSubstring("Duplicate value"))
		})

		// The typed client drops empty strings before they reach the API
		// server (omitempty), so the fields a user can only blank out from
		// YAML are submitted as a raw manifest instead.
		DescribeTable("should reject a blanked-out field of a hand-written manifest",
			func(name, block, field string) {
				raw := &unstructured.Unstructured{Object: map[string]any{
					"apiVersion": memgraphcomv1alpha1.SchemeGroupVersion.String(),
					"kind":       "MemgraphCluster",
					"metadata":   map[string]any{"name": name, "namespace": resourceNamespace},
					"spec":       map[string]any{block: map[string]any{field: ""}},
				}}

				err := k8sClient.Create(ctx, raw)
				Expect(err).To(HaveOccurred(), "expected admission to reject the manifest")
				Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected an Invalid error, got %v", err)
				Expect(err.Error()).To(ContainSubstring("should be at least 1 chars long"))
			},
			Entry("an empty image repository", "invalid-raw-repository", "image", "repository"),
			Entry("an empty image tag", "invalid-raw-tag", "image", "tag"),
			Entry("an empty secret name", "invalid-raw-secret-name", "secrets", "name"),
			Entry("an empty license key", "invalid-raw-license-key", "secrets", "licenseKey"),
			Entry("an empty organization key", "invalid-raw-organization-key", "secrets", "organizationKey"),
		)
	})

	Context("when updating a live cluster", func() {
		// live creates a cluster with the default topology and returns a
		// mutate-and-update helper over the freshest stored copy.
		update := func(name string, mutate func(*memgraphcomv1alpha1.MemgraphCluster)) error {
			GinkgoHelper()
			stored := &memgraphcomv1alpha1.MemgraphCluster{}
			Expect(k8sClient.Get(ctx,
				types.NamespacedName{Name: name, Namespace: resourceNamespace}, stored)).To(Succeed())
			mutate(stored)
			return k8sClient.Update(ctx, stored)
		}

		expectImmutable := func(name string, mutate func(*memgraphcomv1alpha1.MemgraphCluster), field string) {
			GinkgoHelper()
			err := update(name, mutate)
			Expect(err).To(HaveOccurred(), "expected admission to reject the topology change")
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected an Invalid error, got %v", err)
			Expect(err.Error()).To(ContainSubstring(field + " is immutable"))
			Expect(err.Error()).To(ContainSubstring("not supported in v1alpha1"))
		}

		It("should reject growing or shrinking the coordinator count", func() {
			createAccepted("immutable-coordinators", memgraphcomv1alpha1.MemgraphClusterSpec{
				Coordinators: ptr.To(int32(3)),
			})

			expectImmutable("immutable-coordinators", func(c *memgraphcomv1alpha1.MemgraphCluster) {
				c.Spec.Coordinators = ptr.To(int32(5))
			}, "coordinators")
			expectImmutable("immutable-coordinators", func(c *memgraphcomv1alpha1.MemgraphCluster) {
				c.Spec.Coordinators = ptr.To(int32(1))
			}, "coordinators")
		})

		It("should reject growing or shrinking the data instance count", func() {
			createAccepted("immutable-data", memgraphcomv1alpha1.MemgraphClusterSpec{
				DataInstances: ptr.To(int32(2)),
			})

			expectImmutable("immutable-data", func(c *memgraphcomv1alpha1.MemgraphCluster) {
				c.Spec.DataInstances = ptr.To(int32(3))
			}, "dataInstances")
			expectImmutable("immutable-data", func(c *memgraphcomv1alpha1.MemgraphCluster) {
				c.Spec.DataInstances = ptr.To(int32(1))
			}, "dataInstances")
		})

		It("should reject a count change that arrives as a field removal", func() {
			// Dropping a non-default count from the manifest re-defaults it,
			// which is a topology change dressed up as a deletion.
			createAccepted("immutable-omitted", memgraphcomv1alpha1.MemgraphClusterSpec{
				Coordinators: ptr.To(int32(5)),
			})

			expectImmutable("immutable-omitted", func(c *memgraphcomv1alpha1.MemgraphCluster) {
				c.Spec.Coordinators = nil
			}, "coordinators")
		})

		It("should accept an update that leaves the counts alone", func() {
			createAccepted("immutable-unchanged", memgraphcomv1alpha1.MemgraphClusterSpec{
				Coordinators:  ptr.To(int32(3)),
				DataInstances: ptr.To(int32(2)),
			})

			Expect(update("immutable-unchanged", func(c *memgraphcomv1alpha1.MemgraphCluster) {
				c.Spec.Image.Tag = customImageTag
				c.Spec.Secrets.Name = "another-license"
			})).To(Succeed())

			Expect(update("immutable-unchanged", func(c *memgraphcomv1alpha1.MemgraphCluster) {
				c.Spec.Coordinators = ptr.To(int32(3))
				c.Spec.DataInstances = ptr.To(int32(2))
			})).To(Succeed(), "re-applying the same counts is not a topology change")
		})

		It("should accept an update that omits a count matching the default", func() {
			createAccepted("immutable-omitted-default", memgraphcomv1alpha1.MemgraphClusterSpec{
				Coordinators: ptr.To(memgraphcomv1alpha1.DefaultCoordinatorCount),
			})

			Expect(update("immutable-omitted-default", func(c *memgraphcomv1alpha1.MemgraphCluster) {
				c.Spec.Coordinators = nil
			})).To(Succeed(), "defaulting restores the same count, so the topology is unchanged")
		})
	})
})
