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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
			}), "the CRD schema defaults must match the Go constants the builders fall back to")
		})

		It("should default the fields a partially specified block leaves out", func() {
			stored := createAccepted("valid-partial-blocks", memgraphcomv1alpha1.MemgraphClusterSpec{
				Image:   memgraphcomv1alpha1.ImageSpec{Tag: customImageTag},
				Secrets: memgraphcomv1alpha1.SecretsSpec{Name: customSecretName},
			})

			Expect(stored.Spec.Image.Tag).To(Equal(customImageTag))
			Expect(stored.Spec.Image.Repository).To(Equal(memgraphcomv1alpha1.DefaultImageRepository))
			Expect(stored.Spec.Secrets.LicenseKey).To(Equal(memgraphcomv1alpha1.DefaultLicenseSecretKey))
			Expect(stored.Spec.Secrets.OrganizationKey).To(Equal(memgraphcomv1alpha1.DefaultOrganizationSecretKey))
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
		)

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
