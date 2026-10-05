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

// externalDNSAnnotation is the one third-party annotation the operator reads a
// hostname off, spelled out rather than shared with the API package: pinning
// the key external-dns actually watches is part of what these specs are for.
const externalDNSAnnotation = "external-dns.alpha.kubernetes.io/hostname"

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

// defaultCoreDumps is the whole core dumps block as the CRD schema defaults
// materialize it: the role blocks are presence-based and stay absent, so only
// the cluster-wide toggle is filled in.
func defaultCoreDumps() memgraphcomv1alpha1.CoreDumpsSpec {
	return memgraphcomv1alpha1.CoreDumpsSpec{
		ConfigureCorePattern: ptr.To(memgraphcomv1alpha1.DefaultConfigureCorePattern),
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
					Repository: memgraphcomv1alpha1.DefaultImageRepository,
					Tag:        memgraphcomv1alpha1.DefaultImageTag,
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
				// The readiness probe, resources, labels and the env/args passthrough
				// have no schema defaults: the probe timings are resolved by the
				// builders and the rest default to "nothing added".
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

		DescribeTable("should accept any odd coordinator count from three up and any positive data instance count",
			func(name string, coordinators, dataInstances int32) {
				createAccepted(name, memgraphcomv1alpha1.MemgraphClusterSpec{
					Coordinators:  ptr.To(coordinators),
					DataInstances: ptr.To(dataInstances),
				})
			},
			Entry("the smallest quorum and a single data instance", "valid-topology-min", int32(3), int32(1)),
			Entry("a quorum and replica count beyond the former upper bounds", "valid-topology-large",
				int32(9), int32(16)),
		)

		It("should accept a fully tuned pod configuration", func() {
			stored := createAccepted("valid-pod-tuning", memgraphcomv1alpha1.MemgraphClusterSpec{
				ClusterDomain:  "k8s.example.com",
				ReadinessProbe: memgraphcomv1alpha1.ReadinessProbeSpec{FailureThreshold: ptr.To(int32(6))},
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
				Flags: memgraphcomv1alpha1.FlagsSpec{
					// Coordinator settings live beside the coordinators' flags, and
					// a name the operator does not know passes through for the
					// coordinators to judge.
					Coordinators: map[string]memgraphcomv1alpha1.FlagValue{
						downTimeoutSetting: "7", globalReadOnly: settingOff, futureSetting: "x",
					},
					// The second one shares a prefix with the reserved bolt-port
					// without being it: the guard matches whole flag names, so a
					// legitimate neighbour is not caught by it. The third is a
					// run-time flag in the underscore spelling, which is as
					// legitimate as the dashed one.
					Data: map[string]memgraphcomv1alpha1.FlagValue{
						snapshotOnExitFlag: flagOn,
						"bolt-num-workers": "8",
						logLevelUnderscore: infoLevel,
					},
				},
			})

			Expect(stored.Spec.ClusterDomain).To(Equal("k8s.example.com"))
			Expect(stored.Spec.ReadinessProbe.FailureThreshold).To(HaveValue(Equal(int32(6))))
			Expect(stored.Spec.ReadinessProbe.PeriodSeconds).To(BeNil(),
				"an unset timing stays unset; its default is resolved by the builders, not the schema")
			Expect(stored.Spec.ExtraEnv.Data).To(HaveLen(1))
			Expect(stored.Spec.Flags.Coordinators).To(Equal(map[string]memgraphcomv1alpha1.FlagValue{
				downTimeoutSetting: "7", globalReadOnly: settingOff, futureSetting: "x",
			}))
			Expect(stored.Spec.Flags.Data).To(Equal(map[string]memgraphcomv1alpha1.FlagValue{
				snapshotOnExitFlag: flagOn,
				"bolt-num-workers": "8",
				logLevelUnderscore: infoLevel,
			}))
		})

		It("should accept a scheduling block and default the rule it leaves out", func() {
			stored := createAccepted("valid-scheduling", memgraphcomv1alpha1.MemgraphClusterSpec{
				Scheduling: memgraphcomv1alpha1.SchedulingSpec{
					// The empty block is the HA chart's default rule; every
					// knob inside is a schema default.
					PodAntiAffinity: &memgraphcomv1alpha1.PodAntiAffinitySpec{},
					Coordinators: memgraphcomv1alpha1.RoleSchedulingSpec{
						NodeSelector:      map[string]string{"role": "coordinator-node"},
						PriorityClassName: "system-cluster-critical",
						Tolerations: []corev1.Toleration{{
							Key: "memgraph", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule,
						}},
					},
					Data: memgraphcomv1alpha1.RoleSchedulingSpec{
						TopologySpreadConstraints: []corev1.TopologySpreadConstraint{{
							MaxSkew: 1, TopologyKey: "topology.kubernetes.io/zone", WhenUnsatisfiable: corev1.DoNotSchedule,
						}},
						PodAntiAffinity: &corev1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
								LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "batch"}},
								TopologyKey:   "kubernetes.io/hostname",
							}},
						},
					},
				},
			})

			rule := stored.Spec.Scheduling.PodAntiAffinity
			Expect(rule).NotTo(BeNil())
			Expect(rule.Type).To(Equal(memgraphcomv1alpha1.PodAntiAffinityPreferred))
			Expect(rule.Scope).To(Equal(memgraphcomv1alpha1.PodAntiAffinityScopeRole))
			Expect(rule.TopologyKey).To(Equal("kubernetes.io/hostname"))
			Expect(stored.Spec.Scheduling.Coordinators.NodeSelector).To(HaveKeyWithValue("role", "coordinator-node"))
			Expect(stored.Spec.Scheduling.Coordinators.Tolerations).To(HaveLen(1))
			Expect(stored.Spec.Scheduling.Data.TopologySpreadConstraints).To(HaveLen(1))
			Expect(stored.Spec.Scheduling.Data.TopologySpreadConstraints[0].LabelSelector).To(BeNil(),
				"the role's selector is filled in by the builder, not stored on the resource")
			Expect(stored.Spec.Scheduling.Data.PodAntiAffinity).NotTo(BeNil())
		})

		It("should leave an absent scheduling block absent", func() {
			stored := createAccepted("no-scheduling", memgraphcomv1alpha1.MemgraphClusterSpec{})
			Expect(stored.Spec.Scheduling.PodAntiAffinity).To(BeNil(),
				"the operator's rule is presence-based: no schema default may conjure it")
		})

		It("should reject an anti-affinity type or scope outside the enum", func() {
			expectRejected("bad-anti-affinity-type", memgraphcomv1alpha1.MemgraphClusterSpec{
				Scheduling: memgraphcomv1alpha1.SchedulingSpec{
					PodAntiAffinity: &memgraphcomv1alpha1.PodAntiAffinitySpec{Type: "hard"},
				},
			}, `Unsupported value: "hard"`)
			expectRejected("bad-anti-affinity-scope", memgraphcomv1alpha1.MemgraphClusterSpec{
				Scheduling: memgraphcomv1alpha1.SchedulingSpec{
					PodAntiAffinity: &memgraphcomv1alpha1.PodAntiAffinitySpec{Scope: "everywhere"},
				},
			}, `Unsupported value: "everywhere"`)
		})

		It("should reject a topology key that is not a label key", func() {
			expectRejected("bad-topology-key", memgraphcomv1alpha1.MemgraphClusterSpec{
				Scheduling: memgraphcomv1alpha1.SchedulingSpec{
					PodAntiAffinity: &memgraphcomv1alpha1.PodAntiAffinitySpec{TopologyKey: "not a label"},
				},
			}, "topologyKey")
		})

		// The sysctl block is presence-based: an empty spec must not grow it, and
		// an empty block must be filled with the floor Memgraph checks for.
		It("should leave the sysctl init container block absent and default its floor when present", func() {
			absent := createAccepted("no-sysctl", memgraphcomv1alpha1.MemgraphClusterSpec{})
			Expect(absent.Spec.SysctlInitContainer).To(BeNil(),
				"the block is presence-based: no schema default may conjure it")

			present := createAccepted("empty-sysctl", memgraphcomv1alpha1.MemgraphClusterSpec{
				SysctlInitContainer: &memgraphcomv1alpha1.SysctlInitContainerSpec{},
			})
			Expect(present.Spec.SysctlInitContainer).To(HaveValue(Equal(memgraphcomv1alpha1.SysctlInitContainerSpec{
				MaxMapCount: memgraphcomv1alpha1.DefaultMaxMapCount,
			})))
		})

		// The ownership block is presence-based and has no fields: an empty spec
		// must not grow it, and an empty block must survive the round trip as
		// present, since presence is the whole signal.
		It("should leave the ownership init container block absent and keep it when present", func() {
			absent := createAccepted("no-fix-ownership", memgraphcomv1alpha1.MemgraphClusterSpec{})
			Expect(absent.Spec.FixOwnershipInitContainer).To(BeNil(),
				"the block is presence-based: no schema default may conjure it")

			present := createAccepted("empty-fix-ownership", memgraphcomv1alpha1.MemgraphClusterSpec{
				FixOwnershipInitContainer: &memgraphcomv1alpha1.FixOwnershipInitContainerSpec{},
			})
			Expect(present.Spec.FixOwnershipInitContainer).NotTo(BeNil(),
				"an empty block must round-trip as present, or the container can never be asked for")
		})

		// The identity block is presence-based and every field in it optional
		// without a default: an empty block must round-trip as present and
		// empty, since that is the OpenShift shape.
		It("should keep an empty security context block present and empty", func() {
			absent := createAccepted("no-security-context", memgraphcomv1alpha1.MemgraphClusterSpec{})
			Expect(absent.Spec.SecurityContext).To(BeNil(),
				"the block is presence-based: no schema default may conjure it")

			present := createAccepted("empty-security-context", memgraphcomv1alpha1.MemgraphClusterSpec{
				SecurityContext: &memgraphcomv1alpha1.PodSecurityContextSpec{},
			})
			Expect(present.Spec.SecurityContext).To(HaveValue(Equal(memgraphcomv1alpha1.PodSecurityContextSpec{})),
				"no field of the block may be defaulted, or the platform can never be left to assign it")
		})

		It("should accept the ownership container beside a security context naming the identity", func() {
			stored := createAccepted("fix-ownership-named-identity", memgraphcomv1alpha1.MemgraphClusterSpec{
				FixOwnershipInitContainer: &memgraphcomv1alpha1.FixOwnershipInitContainerSpec{},
				SecurityContext: &memgraphcomv1alpha1.PodSecurityContextSpec{
					RunAsUser: ptr.To(int64(1000)),
					FSGroup:   ptr.To(int64(1000)),
				},
			})
			Expect(stored.Spec.SecurityContext.RunAsUser).To(HaveValue(Equal(int64(1000))))
		})

		It("should accept core dumps with an uploader and default what it leaves out", func() {
			stored := createAccepted("valid-core-dumps-uploader", memgraphcomv1alpha1.MemgraphClusterSpec{
				CoreDumps: memgraphcomv1alpha1.CoreDumpsSpec{
					Data: &memgraphcomv1alpha1.RoleCoreDumpsSpec{
						Size: ptr.To(resource.MustParse("200Gi")),
					},
					Uploader: &memgraphcomv1alpha1.CoreDumpsUploaderSpec{
						Image:          uploaderImage,
						Env:            []memgraphcomv1alpha1.EnvVar{{Name: "S3_BUCKET", Value: "dumps"}},
						EnvFromSecrets: []string{"aws-s3-credentials"},
					},
				},
			})

			dumps := stored.Spec.CoreDumps
			Expect(dumps.Data).To(HaveValue(Equal(memgraphcomv1alpha1.RoleCoreDumpsSpec{
				Size: ptr.To(resource.MustParse("200Gi")),
			})))
			Expect(dumps.ConfigureCorePattern).To(HaveValue(BeTrue()))
			Expect(dumps.Uploader.PullPolicy).To(Equal(memgraphcomv1alpha1.DefaultImagePullPolicy))
			// Whether a role collects at all is its block being present: the
			// coordinators wrote none, and no schema default may conjure one.
			Expect(dumps.Coordinators).To(BeNil())

			empty := createAccepted("valid-core-dumps-empty-block", memgraphcomv1alpha1.MemgraphClusterSpec{
				CoreDumps: memgraphcomv1alpha1.CoreDumpsSpec{Coordinators: &memgraphcomv1alpha1.RoleCoreDumpsSpec{}},
			})
			Expect(empty.Spec.CoreDumps.Coordinators).To(HaveValue(Equal(memgraphcomv1alpha1.RoleCoreDumpsSpec{
				Size: ptr.To(resource.MustParse(memgraphcomv1alpha1.DefaultCoreDumpsSize)),
			})), "an empty role block must be filled with the default size")
		})

		// The userContainers entries are schemaless like extraVolumes, so this is
		// what proves the API server keeps a container's nested fields intact
		// instead of pruning what it has no schema for.
		It("should preserve a schemaless user container through a round trip", func() {
			container := corev1.Container{
				Name:    "my-debugger",
				Image:   "docker.io/library/busybox:1.37.0",
				Command: []string{"sh", "-c", "echo hi; sleep 10000"},
				Env:     []corev1.EnvVar{{Name: "LEVEL", Value: "debug"}},
				VolumeMounts: []corev1.VolumeMount{{
					Name: "log-storage", MountPath: "/var/log/memgraph", ReadOnly: true,
				}},
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")},
				},
				SecurityContext: &corev1.SecurityContext{RunAsUser: ptr.To(int64(1000))},
			}
			stored := createAccepted("valid-user-containers", memgraphcomv1alpha1.MemgraphClusterSpec{
				UserContainers: memgraphcomv1alpha1.UserContainersSpec{
					Coordinators: []corev1.Container{container},
				},
			})

			Expect(stored.Spec.UserContainers.Coordinators).To(Equal([]corev1.Container{container}))
			Expect(stored.Spec.UserContainers.Data).To(BeEmpty())
		})

		// The initContainers entries are schemaless like userContainers; the
		// same round trip proves the nested fields survive.
		It("should preserve a schemaless init container through a round trip", func() {
			container := corev1.Container{
				Name:    "seed-modules",
				Image:   "docker.io/library/busybox:1.37.0",
				Command: []string{"sh", "-c", "echo hello world"},
				Env:     []corev1.EnvVar{{Name: "SOURCE", Value: "s3://modules"}},
				VolumeMounts: []corev1.VolumeMount{{
					Name: "lib-storage", MountPath: "/var/lib/memgraph",
				}},
				SecurityContext: &corev1.SecurityContext{RunAsUser: ptr.To(int64(0))},
			}
			stored := createAccepted("valid-init-containers", memgraphcomv1alpha1.MemgraphClusterSpec{
				InitContainers: memgraphcomv1alpha1.InitContainersSpec{
					Data: []corev1.Container{container},
				},
			})

			Expect(stored.Spec.InitContainers.Data).To(Equal([]corev1.Container{container}))
			Expect(stored.Spec.InitContainers.Coordinators).To(BeEmpty())
		})

		// The extraVolumes entries are schemaless, so nothing but this spec
		// proves the API server keeps an arbitrary volume source intact instead
		// of pruning the fields it has no schema for.
		It("should preserve a schemaless extra volume through a round trip", func() {
			stored := createAccepted("valid-extra-volumes", memgraphcomv1alpha1.MemgraphClusterSpec{
				ExtraVolumes: memgraphcomv1alpha1.ExtraVolumesSpec{
					Data: []corev1.Volume{{
						Name: "bolt-certs",
						VolumeSource: corev1.VolumeSource{
							Secret: &corev1.SecretVolumeSource{
								SecretName:  "bolt-tls",
								DefaultMode: ptr.To(int32(0o400)),
								Items:       []corev1.KeyToPath{{Key: "tls.crt", Path: "cert.pem"}},
							},
						},
					}},
					Coordinators: []corev1.Volume{{
						Name: "vault",
						VolumeSource: corev1.VolumeSource{
							CSI: &corev1.CSIVolumeSource{
								Driver:           "secrets-store.csi.k8s.io",
								ReadOnly:         ptr.To(true),
								VolumeAttributes: map[string]string{"secretProviderClass": memgraphDbName},
							},
						},
					}},
				},
				ExtraVolumeMounts: memgraphcomv1alpha1.ExtraVolumeMountsSpec{
					Data: []corev1.VolumeMount{{
						Name: "bolt-certs", MountPath: "/etc/memgraph/ssl", ReadOnly: true,
					}},
				},
			})

			volume := stored.Spec.ExtraVolumes.Data[0]
			Expect(volume.Name).To(Equal("bolt-certs"))
			Expect(volume.Secret).NotTo(BeNil(), "the secret source must survive a schemaless round trip")
			Expect(volume.Secret.SecretName).To(Equal("bolt-tls"))
			Expect(volume.Secret.DefaultMode).To(HaveValue(Equal(int32(0o400))))
			Expect(volume.Secret.Items).To(ConsistOf(corev1.KeyToPath{Key: "tls.crt", Path: "cert.pem"}))

			csi := stored.Spec.ExtraVolumes.Coordinators[0].CSI
			Expect(csi).NotTo(BeNil())
			Expect(csi.Driver).To(Equal("secrets-store.csi.k8s.io"))
			Expect(csi.VolumeAttributes).To(HaveKeyWithValue("secretProviderClass", memgraphDbName))

			Expect(stored.Spec.ExtraVolumeMounts.Data[0].MountPath).To(Equal("/etc/memgraph/ssl"))
			Expect(stored.Spec.ExtraVolumeMounts.Coordinators).To(BeEmpty())
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
				"should be greater than or equal to 3"),
			// A single coordinator is a quorum of one: it cannot survive losing
			// itself, which is the whole point of running HA.
			Entry("a coordinator count below the HA floor", "invalid-coordinators-below-floor",
				memgraphcomv1alpha1.MemgraphClusterSpec{Coordinators: ptr.To(int32(1))},
				"should be greater than or equal to 3"),
			Entry("an even coordinator count", "invalid-coordinators-even",
				memgraphcomv1alpha1.MemgraphClusterSpec{Coordinators: ptr.To(int32(4))},
				"coordinators must be an odd number"),
			Entry("zero data instances", "invalid-data-zero",
				memgraphcomv1alpha1.MemgraphClusterSpec{DataInstances: ptr.To(int32(0))},
				"should be greater than or equal to 1"),
			Entry("a tag smuggled into the repository", "invalid-image-repository-tagged",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Image: memgraphcomv1alpha1.ImageSpec{Repository: "memgraph/memgraph:3.13.0"},
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
					Image: memgraphcomv1alpha1.ImageSpec{Tag: "3.13.0 relwithdebinfo"},
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
			Entry("a vmagent without a remote-write endpoint", "invalid-vmagent-no-url",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Monitoring: &memgraphcomv1alpha1.MonitoringSpec{
						VMAgentRemote: &memgraphcomv1alpha1.VMAgentRemoteSpec{},
					},
				},
				"spec.monitoring.vmagentRemote.remoteWrite.url"),
			Entry("a remote-write endpoint that is not an http URL", "invalid-vmagent-url-scheme",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Monitoring: &memgraphcomv1alpha1.MonitoringSpec{
						VMAgentRemote: &memgraphcomv1alpha1.VMAgentRemoteSpec{
							RemoteWrite: memgraphcomv1alpha1.RemoteWriteSpec{URL: "vmsingle:8428/api/v1/write"},
						},
					},
				},
				"in body should match"),
			Entry("a basic-auth block naming no Secret", "invalid-vmagent-basic-auth-no-secret",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Monitoring: &memgraphcomv1alpha1.MonitoringSpec{
						VMAgentRemote: &memgraphcomv1alpha1.VMAgentRemoteSpec{
							RemoteWrite: memgraphcomv1alpha1.RemoteWriteSpec{
								URL:       "http://vmsingle:8428/api/v1/write",
								BasicAuth: &memgraphcomv1alpha1.BasicAuthSecretSpec{},
							},
						},
					},
				},
				"spec.monitoring.vmagentRemote.remoteWrite.basicAuth.secretName"),
			Entry("an external label that is not a Prometheus label name", "invalid-vmagent-external-label",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Monitoring: &memgraphcomv1alpha1.MonitoringSpec{
						VMAgentRemote: &memgraphcomv1alpha1.VMAgentRemoteSpec{
							RemoteWrite:    memgraphcomv1alpha1.RemoteWriteSpec{URL: "http://vmsingle:8428/api/v1/write"},
							ExternalLabels: map[string]string{"cluster-name": "production"},
						},
					},
				},
				"externalLabels keys must be Prometheus label names"),
			Entry("a Vector sidecar without a logs endpoint", "invalid-vector-no-endpoint",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Monitoring: &memgraphcomv1alpha1.MonitoringSpec{
						VectorRemote: &memgraphcomv1alpha1.VectorRemoteSpec{},
					},
				},
				"spec.monitoring.vectorRemote.logsEndpoint"),
			Entry("a logs endpoint that is not an http URL", "invalid-vector-endpoint-scheme",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Monitoring: &memgraphcomv1alpha1.MonitoringSpec{
						VectorRemote: &memgraphcomv1alpha1.VectorRemoteSpec{LogsEndpoint: "victoria-logs:9428/insert"},
					},
				},
				"in body should match"),
			Entry("a Vector auth block naming no Secret", "invalid-vector-auth-no-secret",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Monitoring: &memgraphcomv1alpha1.MonitoringSpec{
						VectorRemote: &memgraphcomv1alpha1.VectorRemoteSpec{
							LogsEndpoint: "http://victoria-logs:9428/insert",
							Auth:         &memgraphcomv1alpha1.BasicAuthSecretSpec{},
						},
					},
				},
				"spec.monitoring.vectorRemote.auth.secretName"),
			Entry("a Vector extra label that is not a label name", "invalid-vector-extra-label",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Monitoring: &memgraphcomv1alpha1.MonitoringSpec{
						VectorRemote: &memgraphcomv1alpha1.VectorRemoteSpec{
							LogsEndpoint: "http://victoria-logs:9428/insert",
							ExtraLabels:  map[string]string{"cluster-id": "production"},
						},
					},
				},
				"extraLabels keys must be label names"),
			Entry("a zero vm.max_map_count floor", "invalid-max-map-count-zero",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					SysctlInitContainer: &memgraphcomv1alpha1.SysctlInitContainerSpec{MaxMapCount: -1},
				},
				"should be greater than or equal to 1"),
			Entry("a negative uid", "invalid-negative-uid",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					SecurityContext: &memgraphcomv1alpha1.PodSecurityContextSpec{RunAsUser: ptr.To(int64(-1))},
				},
				"should be greater than or equal to 0"),
			// The chown target has to be named: a platform-assigned uid is not
			// known when the pod template is built.
			Entry("the ownership container beside an identity left to the platform", "invalid-fix-ownership-no-uid",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					FixOwnershipInitContainer: &memgraphcomv1alpha1.FixOwnershipInitContainerSpec{},
					SecurityContext:           &memgraphcomv1alpha1.PodSecurityContextSpec{},
				},
				"fixOwnershipInitContainer needs securityContext to name runAsUser"),
			Entry("the ownership container beside a uid without a group", "invalid-fix-ownership-no-gid",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					FixOwnershipInitContainer: &memgraphcomv1alpha1.FixOwnershipInitContainerSpec{},
					SecurityContext:           &memgraphcomv1alpha1.PodSecurityContextSpec{RunAsUser: ptr.To(int64(1000))},
				},
				"fixOwnershipInitContainer needs securityContext to name runAsUser"),
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
			// Two mounts cannot share a path, and mounting over the data or log
			// directory would hide Memgraph's own storage behind another volume.
			Entry("an extra mount over the data directory", "invalid-extra-mount-lib",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					ExtraVolumeMounts: memgraphcomv1alpha1.ExtraVolumeMountsSpec{
						Data: []corev1.VolumeMount{{Name: "shadow", MountPath: "/var/lib/memgraph"}},
					},
				},
				"extraVolumeMounts must not mount over a path the operator already mounts"),
			Entry("an extra mount over the scratch directory", "invalid-extra-mount-tmp",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					ExtraVolumeMounts: memgraphcomv1alpha1.ExtraVolumeMountsSpec{
						Coordinators: []corev1.VolumeMount{{Name: "shadow", MountPath: "/tmp"}},
					},
				},
				"extraVolumeMounts must not mount over a path the operator already mounts"),
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
				"uploader requires core dumps for at least one role"),
			Entry("an uploader without an image", "invalid-uploader-no-image",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					CoreDumps: memgraphcomv1alpha1.CoreDumpsSpec{
						Data:     &memgraphcomv1alpha1.RoleCoreDumpsSpec{},
						Uploader: &memgraphcomv1alpha1.CoreDumpsUploaderSpec{},
					},
				},
				"should be at least 1 chars long"),
			Entry("an uploader shadowing the core dumps path variable", "invalid-uploader-env",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					CoreDumps: memgraphcomv1alpha1.CoreDumpsSpec{
						Coordinators: &memgraphcomv1alpha1.RoleCoreDumpsSpec{},
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
				"MEMGRAPH_ENTERPRISE_LICENSE, MEMGRAPH_ORGANIZATION_NAME"),
			Entry("an env var shadowing an AWS credential the awsCredentials block owns", "invalid-env-aws",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					ExtraEnv: memgraphcomv1alpha1.ExtraEnvSpec{
						Data: []memgraphcomv1alpha1.EnvVar{{
							Name:  memgraphcomv1alpha1.EnvAWSSecretKey,
							Value: "smuggled-secret",
						}},
					},
				},
				"they come from the secrets and awsCredentials blocks"),
			Entry("an awsCredentials block naming no Secret", "invalid-aws-no-secret",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					AWSCredentials: &memgraphcomv1alpha1.AWSCredentialsSpec{},
				},
				"spec.awsCredentials.secretName"),
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
			// A port set through flags would leave the pods listening somewhere
			// the registered addresses do not point.
			Entry("a flag overriding a port", "invalid-flags-port",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Flags: memgraphcomv1alpha1.FlagsSpec{Data: map[string]memgraphcomv1alpha1.FlagValue{"bolt-port": "7777"}},
				},
				"flags must not set a port"),
			Entry("a flag overriding the coordinator identity", "invalid-flags-coordinator-id",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Flags: memgraphcomv1alpha1.FlagsSpec{Coordinators: map[string]memgraphcomv1alpha1.FlagValue{"coordinator-id": "9"}},
				},
				"flags must not set a port"),
			// Memgraph's flags are gflags, which treats a hyphen as an underscore —
			// the flags are declared bolt_port, coordinator_id and so on, and the
			// operator's own --bolt-port only works because of that. Both spellings
			// reach the same flag, so the guard has to reject both or it rejects
			// neither.
			Entry("a reserved flag spelled with underscores", "invalid-flags-underscores",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Flags: memgraphcomv1alpha1.FlagsSpec{Data: map[string]memgraphcomv1alpha1.FlagValue{"bolt_port": "7777"}},
				},
				"flags must not set a port"),
			Entry("a flag overriding the log file", "invalid-flags-log-file",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Flags: memgraphcomv1alpha1.FlagsSpec{Data: map[string]memgraphcomv1alpha1.FlagValue{"log-file": "/elsewhere/memgraph.log"}},
				},
				"flags must not set a port"),
			Entry("a flag overriding the monitoring websocket port the Vector sidecar dials", "invalid-flags-monitoring-port",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Flags: memgraphcomv1alpha1.FlagsSpec{Coordinators: map[string]memgraphcomv1alpha1.FlagValue{"monitoring-port": "7445"}},
				},
				"flags must not set a port"),
			// The CR carries no secret material, and the two AWS credential flags
			// are the only secret-shaped ones in Memgraph's flag surface.
			Entry("a flag carrying an AWS credential", "invalid-flags-aws-secret",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Flags: memgraphcomv1alpha1.FlagsSpec{Data: map[string]memgraphcomv1alpha1.FlagValue{"aws-secret-key": "hunter2"}},
				},
				"the AWS configuration"),
			// The region and endpoint are not secret, but they come from the
			// awsCredentials Secret with the keys, so two sources cannot fight.
			Entry("a flag setting the AWS region", "invalid-flags-aws-region",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Flags: memgraphcomv1alpha1.FlagsSpec{Data: map[string]memgraphcomv1alpha1.FlagValue{"aws_region": "eu-west-1"}},
				},
				"come from the secrets and awsCredentials blocks"),
			Entry("a flag setting the AWS endpoint URL", "invalid-flags-aws-endpoint",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Flags: memgraphcomv1alpha1.FlagsSpec{Data: map[string]memgraphcomv1alpha1.FlagValue{"aws-endpoint-url": "http://minio:9000"}},
				},
				"come from the secrets and awsCredentials blocks"),
			// Keys are flag names, not command-line arguments: the dashes belong
			// to the flag file the operator writes.
			Entry("a flag key with leading dashes", "invalid-flags-leading-dashes",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Flags: memgraphcomv1alpha1.FlagsSpec{Data: map[string]memgraphcomv1alpha1.FlagValue{"--log-level": infoLevel}},
				},
				"without leading dashes"),
			// A newline in a value would start a second line in the flag file,
			// and so a second flag nobody declared.
			Entry("a flag value spanning lines", "invalid-flags-multiline",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Flags: memgraphcomv1alpha1.FlagsSpec{Data: map[string]memgraphcomv1alpha1.FlagValue{logLevelFlag: "INFO\n--bolt-port=7777"}},
				},
				"should match"),
			// Memgraph accepts exactly its six levels, upper case; anything else
			// is refused by the instance, so it is refused at admission instead.
			Entry("a log level Memgraph does not have", "invalid-flags-log-level",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Flags: memgraphcomv1alpha1.FlagsSpec{Data: map[string]memgraphcomv1alpha1.FlagValue{logLevelFlag: "VERBOSE"}},
				},
				"log-level must be one of"),
			Entry("a log level in lower case", "invalid-flags-log-level-case",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Flags: memgraphcomv1alpha1.FlagsSpec{Coordinators: map[string]memgraphcomv1alpha1.FlagValue{logLevelUnderscore: "info"}},
				},
				"log-level must be one of"),
			// The coordinator settings the operator knows by name are checked for
			// the value shape Memgraph parses, so a typo is caught here and not as
			// a SET the coordinators refuse on every pass; and they belong to the
			// coordinators, so the data map refuses them.
			Entry("a boolean coordinator setting with a non-boolean value", "invalid-coordinator-setting-bool",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Flags: memgraphcomv1alpha1.FlagsSpec{Coordinators: map[string]memgraphcomv1alpha1.FlagValue{readsOnMainSetting: "yes"}},
				},
				`take "true" or "false"`),
			Entry("a numeric coordinator setting with a non-numeric value", "invalid-coordinator-setting-number",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Flags: memgraphcomv1alpha1.FlagsSpec{Coordinators: map[string]memgraphcomv1alpha1.FlagValue{"instance-down-timeout-sec": "5s"}},
				},
				"take a non-negative integer"),
			Entry("a coordinator setting under the data role", "invalid-coordinator-setting-on-data",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Flags: memgraphcomv1alpha1.FlagsSpec{Data: map[string]memgraphcomv1alpha1.FlagValue{downTimeoutSetting: "5"}},
				},
				"set under flags.coordinators"),
			// The license flags are secret material like the AWS keys; the license
			// comes from the secrets block.
			Entry("a flag carrying the license", "invalid-flags-license",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Flags: memgraphcomv1alpha1.FlagsSpec{Coordinators: map[string]memgraphcomv1alpha1.FlagValue{"license-key": "hunter2"}},
				},
				"the license (license-key, organization-name)"),
			// Two spellings of one flag would be two lines for one flag, with
			// gflags silently taking whichever came last.
			Entry("two keys spelling the same flag", "invalid-flags-duplicate",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					Flags: memgraphcomv1alpha1.FlagsSpec{Data: map[string]memgraphcomv1alpha1.FlagValue{logLevelFlag: infoLevel, logLevelUnderscore: "DEBUG"}},
				},
				"spell the same flag"),
			Entry("a probe timing below one", "invalid-probe-period",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					ReadinessProbe: memgraphcomv1alpha1.ReadinessProbeSpec{PeriodSeconds: ptr.To(int32(0))},
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

		It("should accept external access with decorations on both roles", func() {
			stored := createAccepted("valid-external-access", memgraphcomv1alpha1.MemgraphClusterSpec{
				ExternalAccess: &memgraphcomv1alpha1.ExternalAccessSpec{
					Type: memgraphcomv1alpha1.ExternalAccessLoadBalancer,
					Coordinators: memgraphcomv1alpha1.ExternalAccessRoleSpec{
						Labels:      map[string]string{"tier": "coordinators"},
						Annotations: map[string]string{externalDNSAnnotation: "memgraph.example.com"},
					},
					Data: memgraphcomv1alpha1.ExternalAccessRoleSpec{
						Annotations: map[string]string{externalDNSAnnotation: dataHostnamePattern},
					},
				},
			})

			Expect(stored.Spec.ExternalAccess).NotTo(BeNil())
			Expect(stored.Spec.ExternalAccess.Type).To(Equal(memgraphcomv1alpha1.ExternalAccessLoadBalancer))
			Expect(stored.Spec.ExternalAccess.Data.Annotations).To(
				HaveKeyWithValue(externalDNSAnnotation, "data-{ordinal}.memgraph.example.com"))
		})

		// The external block's guards are about the routing table: every data
		// instance is announced at its own address, so one hostname for all of
		// them registers the same address for every instance and breaks client
		// routing, while the coordinators share one address and a placeholder in
		// theirs would announce a hostname nothing publishes.
		DescribeTable("should reject an external access block that would break client routing",
			func(name string, spec memgraphcomv1alpha1.MemgraphClusterSpec, wantMessage string) {
				expectRejected(name, spec, wantMessage)
			},
			Entry("a data hostname without the ordinal placeholder", "invalid-external-data-hostname",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					ExternalAccess: &memgraphcomv1alpha1.ExternalAccessSpec{
						Type: memgraphcomv1alpha1.ExternalAccessLoadBalancer,
						Data: memgraphcomv1alpha1.ExternalAccessRoleSpec{
							Annotations: map[string]string{externalDNSAnnotation: "data.memgraph.example.com"},
						},
					},
				},
				"must contain {ordinal}"),
			Entry("a coordinators hostname with the ordinal placeholder", "invalid-external-coordinators-hostname",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					ExternalAccess: &memgraphcomv1alpha1.ExternalAccessSpec{
						Type: memgraphcomv1alpha1.ExternalAccessLoadBalancer,
						Coordinators: memgraphcomv1alpha1.ExternalAccessRoleSpec{
							Annotations: map[string]string{externalDNSAnnotation: "coordinator-{ordinal}.memgraph.example.com"},
						},
					},
				},
				"must not contain {ordinal}"),
			Entry("an exposure type the operator does not implement", "invalid-external-type",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					ExternalAccess: &memgraphcomv1alpha1.ExternalAccessSpec{Type: "NodePort"},
				},
				`Unsupported value: "NodePort"`),
			Entry("an external access block without a type", "invalid-external-no-type",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					ExternalAccess: &memgraphcomv1alpha1.ExternalAccessSpec{},
				},
				"Unsupported value"),
		)

		It("should accept a Gateway exposure and default its port base", func() {
			stored := createAccepted("valid-external-gateway", memgraphcomv1alpha1.MemgraphClusterSpec{
				ExternalAccess: &memgraphcomv1alpha1.ExternalAccessSpec{
					Type:    memgraphcomv1alpha1.ExternalAccessGateway,
					Gateway: memgraphcomv1alpha1.ExternalAccessGatewaySpec{GatewayClassName: "eg"},
				},
			})

			Expect(stored.Spec.ExternalAccess.Gateway.DataPortBase).To(
				HaveValue(Equal(memgraphcomv1alpha1.DefaultGatewayDataPortBase)))
		})

		// The Gateway block's guards: the class is what makes a Gateway get
		// programmed at all, the port base must clear the coordinators' listener,
		// and the per-instance ports must fit — dataInstances decides how many.
		DescribeTable("should reject a Gateway exposure that cannot be programmed",
			func(name string, spec memgraphcomv1alpha1.MemgraphClusterSpec, wantMessage string) {
				expectRejected(name, spec, wantMessage)
			},
			Entry("a Gateway without a class", "invalid-gateway-no-class",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					ExternalAccess: &memgraphcomv1alpha1.ExternalAccessSpec{Type: memgraphcomv1alpha1.ExternalAccessGateway},
				},
				"gateway.gatewayClassName is required when type is Gateway"),
			Entry("a Gateway block on a LoadBalancer exposure", "invalid-gateway-block-on-lb",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					ExternalAccess: &memgraphcomv1alpha1.ExternalAccessSpec{
						Type:    memgraphcomv1alpha1.ExternalAccessLoadBalancer,
						Gateway: memgraphcomv1alpha1.ExternalAccessGatewaySpec{GatewayClassName: "eg"},
					},
				},
				"gateway is only used when type is Gateway"),
			Entry("a data port base on the coordinators' listener", "invalid-gateway-port-base",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					ExternalAccess: &memgraphcomv1alpha1.ExternalAccessSpec{
						Type: memgraphcomv1alpha1.ExternalAccessGateway,
						Gateway: memgraphcomv1alpha1.ExternalAccessGatewaySpec{
							GatewayClassName: "eg", DataPortBase: ptr.To(memgraphcomv1alpha1.BoltPort),
						},
					},
				},
				"gateway.dataPortBase must be above 7687"),
			Entry("a data port range past the end of the port space", "invalid-gateway-port-range",
				memgraphcomv1alpha1.MemgraphClusterSpec{
					DataInstances: ptr.To(int32(3)),
					ExternalAccess: &memgraphcomv1alpha1.ExternalAccessSpec{
						Type: memgraphcomv1alpha1.ExternalAccessGateway,
						Gateway: memgraphcomv1alpha1.ExternalAccessGatewaySpec{
							GatewayClassName: "eg", DataPortBase: ptr.To(int32(65534)),
						},
					},
				},
				"dataPortBase + dataInstances must not exceed 65536"),
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

		// expectRejectedUpdate asserts that admission refused a topology change
		// and said what is wrong with the new value.
		expectRejectedUpdate := func(
			name string,
			mutate func(*memgraphcomv1alpha1.MemgraphCluster),
			wantMessage string,
		) {
			GinkgoHelper()
			err := update(name, mutate)
			Expect(err).To(HaveOccurred(), "expected admission to reject the topology change")
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected an Invalid error, got %v", err)
			Expect(err.Error()).To(ContainSubstring(wantMessage))
		}

		// Intra-cluster TLS is all-or-nothing per process, and the operator's
		// one-pod-at-a-time roll cannot carry a cluster across the line: the
		// restarted replica and the not-yet-restarted MAIN can no longer talk,
		// so the roll waits on replication forever. Its presence is therefore
		// pinned at admission, in both directions and whether or not the tls
		// block itself existed before; the Secret it names stays free, since
		// a renamed Secret is an ordinary roll with every member still on TLS.
		Context("when the cluster has intra-cluster TLS", func() {
			const intraSecret = "intra-cluster-tls"
			const boltSecret = "bolt-tls"
			const pinMessage = "tls.intraCluster cannot be added or removed on a live cluster"

			withIntraCluster := memgraphcomv1alpha1.MemgraphClusterSpec{
				TLS: &memgraphcomv1alpha1.TLSSpec{
					IntraCluster: &memgraphcomv1alpha1.IntraClusterTLSSpec{SecretName: intraSecret},
				},
			}

			It("should reject adding the block to a cluster without a tls block", func() {
				createAccepted("intra-add-no-tls", memgraphcomv1alpha1.MemgraphClusterSpec{})
				expectRejectedUpdate("intra-add-no-tls", func(c *memgraphcomv1alpha1.MemgraphCluster) {
					c.Spec.TLS = &memgraphcomv1alpha1.TLSSpec{
						IntraCluster: &memgraphcomv1alpha1.IntraClusterTLSSpec{SecretName: intraSecret},
					}
				}, pinMessage)
			})

			It("should reject adding the block beside an existing bolt block", func() {
				createAccepted("intra-add-beside-bolt", memgraphcomv1alpha1.MemgraphClusterSpec{
					TLS: &memgraphcomv1alpha1.TLSSpec{
						Bolt: &memgraphcomv1alpha1.BoltTLSSpec{SecretName: boltSecret},
					},
				})
				expectRejectedUpdate("intra-add-beside-bolt", func(c *memgraphcomv1alpha1.MemgraphCluster) {
					c.Spec.TLS.IntraCluster = &memgraphcomv1alpha1.IntraClusterTLSSpec{SecretName: intraSecret}
				}, pinMessage)
			})

			It("should reject removing the block", func() {
				createAccepted("intra-remove", withIntraCluster)
				expectRejectedUpdate("intra-remove", func(c *memgraphcomv1alpha1.MemgraphCluster) {
					c.Spec.TLS.IntraCluster = nil
				}, pinMessage)
			})

			It("should reject removing the whole tls block", func() {
				createAccepted("intra-remove-tls", withIntraCluster)
				expectRejectedUpdate("intra-remove-tls", func(c *memgraphcomv1alpha1.MemgraphCluster) {
					c.Spec.TLS = nil
				}, pinMessage)
			})

			It("should accept a different Secret, and bolt coming and going", func() {
				createAccepted("intra-secret-change", withIntraCluster)
				Expect(update("intra-secret-change", func(c *memgraphcomv1alpha1.MemgraphCluster) {
					c.Spec.TLS.IntraCluster.SecretName = "intra-cluster-tls-2027"
					c.Spec.TLS.Bolt = &memgraphcomv1alpha1.BoltTLSSpec{SecretName: boltSecret}
				})).To(Succeed())
				Expect(update("intra-secret-change", func(c *memgraphcomv1alpha1.MemgraphCluster) {
					c.Spec.TLS.Bolt = nil
				})).To(Succeed())
			})
		})

		It("should accept growing both counts in one edit", func() {
			createAccepted("scale-up-both", memgraphcomv1alpha1.MemgraphClusterSpec{
				Coordinators:  ptr.To(int32(3)),
				DataInstances: ptr.To(int32(2)),
			})

			Expect(update("scale-up-both", func(c *memgraphcomv1alpha1.MemgraphCluster) {
				c.Spec.Coordinators = ptr.To(int32(5))
				c.Spec.DataInstances = ptr.To(int32(3))
			})).To(Succeed(), "both counts are mutable, in any step size, in one edit")

			stored := &memgraphcomv1alpha1.MemgraphCluster{}
			Expect(k8sClient.Get(ctx,
				types.NamespacedName{Name: "scale-up-both", Namespace: resourceNamespace}, stored)).To(Succeed())
			Expect(stored.Spec.Coordinators).To(HaveValue(Equal(int32(5))))
			Expect(stored.Spec.DataInstances).To(HaveValue(Equal(int32(3))))
		})

		It("should accept lowering either count", func() {
			createAccepted("scale-down-both", memgraphcomv1alpha1.MemgraphClusterSpec{
				Coordinators:  ptr.To(int32(5)),
				DataInstances: ptr.To(int32(3)),
			})

			Expect(update("scale-down-both", func(c *memgraphcomv1alpha1.MemgraphCluster) {
				c.Spec.Coordinators = ptr.To(int32(3))
				c.Spec.DataInstances = ptr.To(int32(1))
			})).To(Succeed(), "admission constrains the target counts, nothing about the direction")
		})

		It("should accept a count change that arrives as a field removal", func() {
			// Dropping a non-default count from the manifest re-defaults it,
			// which is a real topology change and no longer refused.
			createAccepted("scale-omitted", memgraphcomv1alpha1.MemgraphClusterSpec{
				Coordinators: ptr.To(int32(5)),
			})

			Expect(update("scale-omitted", func(c *memgraphcomv1alpha1.MemgraphCluster) {
				c.Spec.Coordinators = nil
			})).To(Succeed())

			stored := &memgraphcomv1alpha1.MemgraphCluster{}
			Expect(k8sClient.Get(ctx,
				types.NamespacedName{Name: "scale-omitted", Namespace: resourceNamespace}, stored)).To(Succeed())
			Expect(stored.Spec.Coordinators).To(HaveValue(Equal(memgraphcomv1alpha1.DefaultCoordinatorCount)))
		})

		It("should accept an update that leaves the counts alone", func() {
			createAccepted("scale-unchanged", memgraphcomv1alpha1.MemgraphClusterSpec{
				Coordinators:  ptr.To(int32(3)),
				DataInstances: ptr.To(int32(2)),
			})

			Expect(update("scale-unchanged", func(c *memgraphcomv1alpha1.MemgraphCluster) {
				c.Spec.Image.Tag = customImageTag
				c.Spec.Secrets.Name = "another-license"
			})).To(Succeed())
		})

		// Whether a role collects core dumps — its block being present — decides
		// its StatefulSet's volume claim templates, which Kubernetes forbids
		// changing in place, so adding or removing the block is refused at
		// admission with the procedure that works instead of being accepted and
		// rejected forever by the apply. The rule has to sit on coreDumps: one on
		// the role block would never fire for the block appearing or vanishing.
		DescribeTable("should reject adding or removing a role's core dumps block",
			func(name string, presentAtCreation bool, mutate func(*memgraphcomv1alpha1.MemgraphCluster)) {
				var role *memgraphcomv1alpha1.RoleCoreDumpsSpec
				if presentAtCreation {
					role = &memgraphcomv1alpha1.RoleCoreDumpsSpec{}
				}
				createAccepted(name, memgraphcomv1alpha1.MemgraphClusterSpec{
					CoreDumps: memgraphcomv1alpha1.CoreDumpsSpec{Coordinators: role, Data: role},
				})

				expectRejectedUpdate(name, mutate, "cannot be added or removed on a live cluster")
			},
			Entry("adding coordinator dumps", "core-dumps-on-coordinators", false,
				func(c *memgraphcomv1alpha1.MemgraphCluster) {
					c.Spec.CoreDumps.Coordinators = &memgraphcomv1alpha1.RoleCoreDumpsSpec{}
				}),
			Entry("adding data instance dumps", "core-dumps-on-data", false,
				func(c *memgraphcomv1alpha1.MemgraphCluster) {
					c.Spec.CoreDumps.Data = &memgraphcomv1alpha1.RoleCoreDumpsSpec{}
				}),
			Entry("removing coordinator dumps", "core-dumps-off-coordinators", true,
				func(c *memgraphcomv1alpha1.MemgraphCluster) { c.Spec.CoreDumps.Coordinators = nil }),
			Entry("removing data instance dumps", "core-dumps-off-data", true,
				func(c *memgraphcomv1alpha1.MemgraphCluster) { c.Spec.CoreDumps.Data = nil }),
		)

		// The rules pin the blocks and what backs their claims, nothing around
		// them: the rest stays editable, and an update that does not touch core
		// dumps at all must not trip over the defaulted empty block.
		It("should accept an update that leaves core dumps as they are", func() {
			createAccepted("core-dumps-unchanged", memgraphcomv1alpha1.MemgraphClusterSpec{
				CoreDumps: memgraphcomv1alpha1.CoreDumpsSpec{
					Data: &memgraphcomv1alpha1.RoleCoreDumpsSpec{},
				},
			})

			Expect(update("core-dumps-unchanged", func(c *memgraphcomv1alpha1.MemgraphCluster) {
				c.Spec.Image.Tag = customImageTag
				c.Spec.CoreDumps.ConfigureCorePattern = ptr.To(false)
			})).To(Succeed())
		})

		// Every field that lands in a StatefulSet volumeClaimTemplate is pinned:
		// Kubernetes forbids changing a template in place, so the change is
		// refused at admission with the procedure that works instead of being
		// accepted and rejected by the apply forever.
		DescribeTable("should reject changing a claim template field",
			func(name string, spec memgraphcomv1alpha1.MemgraphClusterSpec,
				mutate func(*memgraphcomv1alpha1.MemgraphCluster), wantMessage string) {
				createAccepted(name, spec)
				expectRejectedUpdate(name, mutate, wantMessage)
			},
			Entry("the lib storage class", "claims-lib-class",
				memgraphcomv1alpha1.MemgraphClusterSpec{},
				func(c *memgraphcomv1alpha1.MemgraphCluster) {
					c.Spec.Storage.Data.LibStorageClassName = ptr.To(customStorageClassName)
				}, "libStorageClassName cannot be changed on a live cluster"),
			Entry("the lib storage class, dropped", "claims-lib-class-dropped",
				memgraphcomv1alpha1.MemgraphClusterSpec{Storage: memgraphcomv1alpha1.StorageSpec{
					Coordinators: memgraphcomv1alpha1.RoleStorageSpec{LibStorageClassName: ptr.To(customStorageClassName)},
				}},
				func(c *memgraphcomv1alpha1.MemgraphCluster) {
					c.Spec.Storage.Coordinators.LibStorageClassName = nil
				}, "libStorageClassName cannot be changed on a live cluster"),
			Entry("the lib claim size", "claims-lib-size",
				memgraphcomv1alpha1.MemgraphClusterSpec{},
				func(c *memgraphcomv1alpha1.MemgraphCluster) {
					c.Spec.Storage.Data.LibPVCSize = ptr.To(resource.MustParse("10Gi"))
				}, "libPVCSize cannot be changed on a live cluster"),
			Entry("the lib access mode", "claims-lib-access-mode",
				memgraphcomv1alpha1.MemgraphClusterSpec{},
				func(c *memgraphcomv1alpha1.MemgraphCluster) {
					c.Spec.Storage.Data.LibStorageAccessMode = corev1.ReadWriteMany
				}, "libStorageAccessMode cannot be changed on a live cluster"),
			Entry("dropping the log claim", "claims-log-dropped",
				memgraphcomv1alpha1.MemgraphClusterSpec{},
				func(c *memgraphcomv1alpha1.MemgraphCluster) {
					c.Spec.Storage.Data.CreateLogStorageClaim = ptr.To(false)
				}, "createLogStorageClaim cannot be changed on a live cluster"),
			Entry("adding the log claim", "claims-log-added",
				memgraphcomv1alpha1.MemgraphClusterSpec{Storage: memgraphcomv1alpha1.StorageSpec{
					Data: memgraphcomv1alpha1.RoleStorageSpec{CreateLogStorageClaim: ptr.To(false)},
				}},
				func(c *memgraphcomv1alpha1.MemgraphCluster) {
					c.Spec.Storage.Data.CreateLogStorageClaim = ptr.To(true)
				}, "createLogStorageClaim cannot be changed on a live cluster"),
			Entry("the log storage class while the claim exists", "claims-log-class",
				memgraphcomv1alpha1.MemgraphClusterSpec{},
				func(c *memgraphcomv1alpha1.MemgraphCluster) {
					c.Spec.Storage.Coordinators.LogStorageClassName = ptr.To(customStorageClassName)
				}, "logStorageClassName cannot be changed on a live cluster while the log claim exists"),
			Entry("the log claim size while the claim exists", "claims-log-size",
				memgraphcomv1alpha1.MemgraphClusterSpec{},
				func(c *memgraphcomv1alpha1.MemgraphCluster) {
					c.Spec.Storage.Coordinators.LogPVCSize = ptr.To(resource.MustParse("2Gi"))
				}, "logPVCSize cannot be changed on a live cluster while the log claim exists"),
			Entry("the core dumps size while the role collects dumps", "claims-core-dumps-size",
				memgraphcomv1alpha1.MemgraphClusterSpec{CoreDumps: memgraphcomv1alpha1.CoreDumpsSpec{
					Data: &memgraphcomv1alpha1.RoleCoreDumpsSpec{},
				}},
				func(c *memgraphcomv1alpha1.MemgraphCluster) {
					c.Spec.CoreDumps.Data.Size = ptr.To(resource.MustParse("20Gi"))
				}, "coreDumps size cannot be changed on a live cluster while the role collects dumps"),
			Entry("the core dumps storage class while a role collects dumps", "claims-core-dumps-class",
				memgraphcomv1alpha1.MemgraphClusterSpec{CoreDumps: memgraphcomv1alpha1.CoreDumpsSpec{
					Coordinators: &memgraphcomv1alpha1.RoleCoreDumpsSpec{},
				}},
				func(c *memgraphcomv1alpha1.MemgraphCluster) {
					c.Spec.CoreDumps.StorageClassName = ptr.To(customStorageClassName)
				}, "coreDumps storageClassName cannot be changed on a live cluster while a role collects dumps"),
		)

		// The rules pin what backs a claim and nothing else: a value that backs no
		// claim is free to change, and a size respelled in other units is the same
		// size.
		DescribeTable("should accept a storage edit that changes no claim template",
			func(name string, spec memgraphcomv1alpha1.MemgraphClusterSpec,
				mutate func(*memgraphcomv1alpha1.MemgraphCluster)) {
				createAccepted(name, spec)
				Expect(update(name, mutate)).To(Succeed())
			},
			Entry("the same lib size in other units", "claims-lib-size-units",
				memgraphcomv1alpha1.MemgraphClusterSpec{},
				func(c *memgraphcomv1alpha1.MemgraphCluster) {
					c.Spec.Storage.Data.LibPVCSize = ptr.To(resource.MustParse("1024Mi"))
				}),
			Entry("the log knobs of a role without a log claim", "claims-log-without-claim",
				memgraphcomv1alpha1.MemgraphClusterSpec{Storage: memgraphcomv1alpha1.StorageSpec{
					Data: memgraphcomv1alpha1.RoleStorageSpec{CreateLogStorageClaim: ptr.To(false)},
				}},
				func(c *memgraphcomv1alpha1.MemgraphCluster) {
					c.Spec.Storage.Data.LogPVCSize = ptr.To(resource.MustParse("5Gi"))
					c.Spec.Storage.Data.LogStorageClassName = ptr.To(customStorageClassName)
					c.Spec.Storage.Data.LogStorageAccessMode = corev1.ReadWriteMany
				}),
			// A role without a block has no size to edit, so the class is the one
			// core dumps knob that backs no claim here.
			Entry("the core dumps storage class while no role collects dumps", "claims-core-dumps-disabled",
				memgraphcomv1alpha1.MemgraphClusterSpec{},
				func(c *memgraphcomv1alpha1.MemgraphCluster) {
					c.Spec.CoreDumps.StorageClassName = ptr.To(customStorageClassName)
				}),
			Entry("the retention policy, which is no claim template field", "claims-retention",
				memgraphcomv1alpha1.MemgraphClusterSpec{},
				func(c *memgraphcomv1alpha1.MemgraphCluster) {
					c.Spec.Storage.RetentionPolicy = memgraphcomv1alpha1.RetentionPolicyDelete
				}),
		)

		// The floors and the odd rule are creation-time validation that keeps
		// applying on every update: a live cluster cannot be edited into a
		// topology it could not have been created with.
		DescribeTable("should reject a topology change that breaks a floor",
			func(name string, mutate func(*memgraphcomv1alpha1.MemgraphCluster), wantMessage string) {
				createAccepted(name, memgraphcomv1alpha1.MemgraphClusterSpec{
					Coordinators:  ptr.To(int32(5)),
					DataInstances: ptr.To(int32(2)),
				})

				expectRejectedUpdate(name, mutate, wantMessage)
			},
			Entry("coordinators below the HA floor", "scale-floor-coordinators",
				func(c *memgraphcomv1alpha1.MemgraphCluster) {
					c.Spec.Coordinators = ptr.To(int32(1))
				}, "should be greater than or equal to 3"),
			Entry("an even coordinator count", "scale-floor-coordinators-even",
				func(c *memgraphcomv1alpha1.MemgraphCluster) {
					c.Spec.Coordinators = ptr.To(int32(4))
				}, "coordinators must be an odd number"),
			Entry("no data instances left", "scale-floor-data",
				func(c *memgraphcomv1alpha1.MemgraphCluster) {
					c.Spec.DataInstances = ptr.To(int32(0))
				}, "should be greater than or equal to 1"),
		)
	})
})
