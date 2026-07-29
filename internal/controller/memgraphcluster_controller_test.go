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
	"errors"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
	"github.com/memgraph/kubernetes-operator/internal/memgraph"
)

// Name suffixes of the per-role workload objects a reconcile creates.
const (
	coordinatorSuffix = "-coordinator"
	dataSuffix        = "-data"
)

// Non-default spec values the specs in this package override with, chosen so
// that they cannot be confused with the CRD schema defaults.
const (
	customImageTag         = "3.13.0"
	customSecretName       = "my-license"
	customStorageClassName = "fast-ssd"
	uploaderImage          = "amazon/aws-cli:2.33.28"
)

// libClaim returns the lib storage claim template of a provisioned
// StatefulSet, failing the spec if the builder stopped emitting it.
func libClaim(sts *appsv1.StatefulSet) corev1.PersistentVolumeClaimSpec {
	GinkgoHelper()
	for _, claim := range sts.Spec.VolumeClaimTemplates {
		if claim.Name == "lib-storage" {
			return claim.Spec
		}
	}
	Fail("StatefulSet " + sts.Name + " has no lib-storage claim template")
	return corev1.PersistentVolumeClaimSpec{}
}

var _ = Describe("MemgraphCluster Controller", func() {
	const resourceNamespace = "default"

	ctx := context.Background()

	var (
		reconciler *MemgraphClusterReconciler
		fake       *fakeMemgraph
	)

	BeforeEach(func() {
		fake = newFakeMemgraph()
		reconciler = &MemgraphClusterReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Memgraph: fake,
		}
	})

	reconcileCluster := func(name string) reconcile.Result {
		GinkgoHelper()
		result, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: name, Namespace: resourceNamespace},
		})
		Expect(err).NotTo(HaveOccurred())
		return result
	}

	get := func(name string, obj client.Object) {
		GinkgoHelper()
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: resourceNamespace}, obj)).To(Succeed())
	}

	// deleteOwned removes the workload objects a reconcile created for the
	// given cluster: envtest runs no garbage collector, so owner-reference
	// cascade deletion never fires and each spec must clean up explicitly.
	deleteOwned := func(clusterName string) {
		GinkgoHelper()
		for _, suffix := range []string{coordinatorSuffix, dataSuffix} {
			sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{
				Name: clusterName + suffix, Namespace: resourceNamespace,
			}}
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, sts))).To(Succeed())
			svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{
				Name: clusterName + suffix, Namespace: resourceNamespace,
			}}
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, svc))).To(Succeed())
		}
	}

	// markWorkloadsReady simulates the kubelet envtest does not run: it
	// reports every replica of both role StatefulSets as ready, which is what
	// gates the registration flow.
	markWorkloadsReady := func(clusterName string) {
		GinkgoHelper()
		for _, suffix := range []string{coordinatorSuffix, dataSuffix} {
			sts := &appsv1.StatefulSet{}
			get(clusterName+suffix, sts)
			sts.Status.Replicas = *sts.Spec.Replicas
			sts.Status.ReadyReplicas = *sts.Spec.Replicas
			sts.Status.AvailableReplicas = *sts.Spec.Replicas
			sts.Status.ObservedGeneration = sts.Generation
			Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())
		}
	}

	expectControlledBy := func(obj client.Object, cluster *memgraphcomv1alpha1.MemgraphCluster) {
		GinkgoHelper()
		ref := metav1.GetControllerOf(obj)
		Expect(ref).NotTo(BeNil(), "expected %s to carry a controller owner reference", obj.GetName())
		Expect(ref.Kind).To(Equal("MemgraphCluster"))
		Expect(ref.Name).To(Equal(cluster.Name))
		Expect(ref.UID).To(Equal(cluster.UID))
		Expect(ref.Controller).To(HaveValue(BeTrue()))
	}

	Context("when reconciling a minimal MemgraphCluster", func() {
		const resourceName = "mgc-minimal"

		cluster := &memgraphcomv1alpha1.MemgraphCluster{}

		BeforeEach(func() {
			resource := &memgraphcomv1alpha1.MemgraphCluster{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: resourceNamespace},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			get(resourceName, cluster)
		})

		AfterEach(func() {
			Expect(k8sClient.Delete(ctx, cluster)).To(Succeed())
			deleteOwned(resourceName)
		})

		It("should apply CRD schema defaults on admission", func() {
			Expect(cluster.Spec.Coordinators).To(HaveValue(Equal(int32(3))))
			Expect(cluster.Spec.DataInstances).To(HaveValue(Equal(int32(2))))
			Expect(cluster.Spec.Image.Repository).To(Equal(memgraphcomv1alpha1.DefaultImageRepository))
			Expect(cluster.Spec.Image.Tag).To(Equal(memgraphcomv1alpha1.DefaultImageTag))
			Expect(cluster.Spec.Image.PullPolicy).To(Equal(memgraphcomv1alpha1.DefaultImagePullPolicy))
			Expect(cluster.Spec.Secrets.Name).To(Equal(memgraphcomv1alpha1.DefaultSecretName))
			Expect(cluster.Spec.Secrets.LicenseKey).To(Equal(memgraphcomv1alpha1.DefaultLicenseSecretKey))
			Expect(cluster.Spec.Secrets.OrganizationKey).To(Equal(memgraphcomv1alpha1.DefaultOrganizationSecretKey))
		})

		It("should provision one StatefulSet and one headless Service per role", func() {
			reconcileCluster(resourceName)

			coordinatorSts := &appsv1.StatefulSet{}
			get(resourceName+coordinatorSuffix, coordinatorSts)
			Expect(coordinatorSts.Spec.Replicas).To(HaveValue(Equal(int32(3))))
			Expect(coordinatorSts.Spec.ServiceName).To(Equal(resourceName + coordinatorSuffix))
			expectControlledBy(coordinatorSts, cluster)

			dataSts := &appsv1.StatefulSet{}
			get(resourceName+dataSuffix, dataSts)
			Expect(dataSts.Spec.Replicas).To(HaveValue(Equal(int32(2))))
			Expect(dataSts.Spec.ServiceName).To(Equal(resourceName + dataSuffix))
			expectControlledBy(dataSts, cluster)

			for _, sts := range []*appsv1.StatefulSet{coordinatorSts, dataSts} {
				podSpec := sts.Spec.Template.Spec
				Expect(podSpec.Containers).To(HaveLen(1))
				container := podSpec.Containers[0]
				Expect(container.Image).To(Equal("docker.io/memgraph/memgraph:3.12.0-relwithdebinfo"))
				Expect(podSpec.SecurityContext.RunAsUser).To(HaveValue(Equal(int64(101))))
				Expect(podSpec.SecurityContext.RunAsGroup).To(HaveValue(Equal(int64(103))))

				licenseRef := container.Env[len(container.Env)-2].ValueFrom.SecretKeyRef
				Expect(licenseRef.Name).To(Equal("memgraph-secrets"))
				Expect(licenseRef.Key).To(Equal("MEMGRAPH_ENTERPRISE_LICENSE"))
			}

			for _, suffix := range []string{coordinatorSuffix, dataSuffix} {
				svc := &corev1.Service{}
				get(resourceName+suffix, svc)
				Expect(svc.Spec.ClusterIP).To(Equal(corev1.ClusterIPNone))
				Expect(svc.Spec.PublishNotReadyAddresses).To(BeTrue())
				expectControlledBy(svc, cluster)
			}
		})

		It("should back both roles with retained lib and log claims", func() {
			reconcileCluster(resourceName)

			for _, suffix := range []string{coordinatorSuffix, dataSuffix} {
				sts := &appsv1.StatefulSet{}
				get(resourceName+suffix, sts)

				claims := map[string]corev1.PersistentVolumeClaimSpec{}
				for _, claim := range sts.Spec.VolumeClaimTemplates {
					claims[claim.Name] = claim.Spec
				}
				Expect(claims).To(HaveKey("lib-storage"))
				Expect(claims).To(HaveKey("log-storage"))
				for name, claim := range claims {
					Expect(claim.AccessModes).To(ConsistOf(corev1.ReadWriteOnce), "claim %s", name)
					Expect(claim.Resources.Requests.Storage()).To(HaveValue(Equal(resource.MustParse("1Gi"))),
						"claim %s", name)
					Expect(claim.StorageClassName).To(BeNil(),
						"claim %s must fall back to the cluster's default StorageClass", name)
				}

				// The default keeps data safe from an accidental CR delete and
				// from a scale-down alike: one retention knob, both halves of
				// the policy.
				Expect(sts.Spec.PersistentVolumeClaimRetentionPolicy).To(HaveValue(Equal(
					appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{
						WhenDeleted: appsv1.RetainPersistentVolumeClaimRetentionPolicyType,
						WhenScaled:  appsv1.RetainPersistentVolumeClaimRetentionPolicyType,
					})))
			}
		})

		// Deleting storage is the StatefulSet machinery's job alone. An
		// operator-owned finalizer would be a second, undeclared deleter — and
		// one that can wedge a deletion when the operator is down.
		It("should claim no finalizer on the MemgraphCluster", func() {
			reconcileCluster(resourceName)

			stored := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, stored)
			Expect(stored.Finalizers).To(BeEmpty())
		})

		It("should be idempotent when reconciling an unchanged resource", func() {
			reconcileCluster(resourceName)

			versions := map[string]string{}
			for _, suffix := range []string{coordinatorSuffix, dataSuffix} {
				sts := &appsv1.StatefulSet{}
				get(resourceName+suffix, sts)
				versions["sts"+suffix] = sts.ResourceVersion
				svc := &corev1.Service{}
				get(resourceName+suffix, svc)
				versions["svc"+suffix] = svc.ResourceVersion
			}

			reconcileCluster(resourceName)

			for _, suffix := range []string{coordinatorSuffix, dataSuffix} {
				sts := &appsv1.StatefulSet{}
				get(resourceName+suffix, sts)
				Expect(sts.ResourceVersion).To(Equal(versions["sts"+suffix]),
					fmt.Sprintf("StatefulSet %s%s changed on a no-op reconcile", resourceName, suffix))
				svc := &corev1.Service{}
				get(resourceName+suffix, svc)
				Expect(svc.ResourceVersion).To(Equal(versions["svc"+suffix]),
					fmt.Sprintf("Service %s%s changed on a no-op reconcile", resourceName, suffix))
			}
		})
	})

	Context("when reconciling a fully specified MemgraphCluster", func() {
		const resourceName = "mgc-custom"

		cluster := &memgraphcomv1alpha1.MemgraphCluster{}

		BeforeEach(func() {
			cr := &memgraphcomv1alpha1.MemgraphCluster{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: resourceNamespace},
				Spec: memgraphcomv1alpha1.MemgraphClusterSpec{
					Coordinators:  ptr.To(int32(3)),
					DataInstances: ptr.To(int32(1)),
					Image: memgraphcomv1alpha1.ImageSpec{
						Repository: "registry.example.com/memgraph",
						Tag:        customImageTag,
						PullPolicy: corev1.PullAlways,
					},
					Secrets: memgraphcomv1alpha1.SecretsSpec{
						Name:            customSecretName,
						LicenseKey:      "license",
						OrganizationKey: "organization",
					},
					Storage: memgraphcomv1alpha1.StorageSpec{
						RetentionPolicy: memgraphcomv1alpha1.RetentionPolicyDelete,
						Coordinators: memgraphcomv1alpha1.RoleStorageSpec{
							LibPVCSize: ptr.To(resource.MustParse("2Gi")),
						},
						Data: memgraphcomv1alpha1.RoleStorageSpec{
							LibPVCSize:          ptr.To(resource.MustParse("100Gi")),
							LibStorageClassName: ptr.To(customStorageClassName),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			get(resourceName, cluster)
		})

		AfterEach(func() {
			Expect(k8sClient.Delete(ctx, cluster)).To(Succeed())
			deleteOwned(resourceName)
		})

		It("should propagate spec values into the workload objects", func() {
			reconcileCluster(resourceName)

			for suffix, replicas := range map[string]int32{coordinatorSuffix: 3, dataSuffix: 1} {
				sts := &appsv1.StatefulSet{}
				get(resourceName+suffix, sts)
				Expect(sts.Spec.Replicas).To(HaveValue(Equal(replicas)),
					"each role's StatefulSet runs the count its own spec field declares")

				container := sts.Spec.Template.Spec.Containers[0]
				Expect(container.Image).To(Equal("registry.example.com/memgraph:3.13.0"))
				Expect(container.ImagePullPolicy).To(Equal(corev1.PullAlways))

				licenseRef := container.Env[len(container.Env)-2].ValueFrom.SecretKeyRef
				Expect(licenseRef.Name).To(Equal(customSecretName))
				Expect(licenseRef.Key).To(Equal("license"))
				organizationRef := container.Env[len(container.Env)-1].ValueFrom.SecretKeyRef
				Expect(organizationRef.Name).To(Equal(customSecretName))
				Expect(organizationRef.Key).To(Equal("organization"))

				Expect(sts.Spec.PersistentVolumeClaimRetentionPolicy).To(HaveValue(Equal(
					appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{
						WhenDeleted: appsv1.DeletePersistentVolumeClaimRetentionPolicyType,
						WhenScaled:  appsv1.DeletePersistentVolumeClaimRetentionPolicyType,
					})), "the Delete policy covers the claims a scale-down orphans too")
			}
		})

		It("should size each role's claims from that role's storage block", func() {
			reconcileCluster(resourceName)

			coordinatorSts := &appsv1.StatefulSet{}
			get(resourceName+coordinatorSuffix, coordinatorSts)
			coordinatorLib := libClaim(coordinatorSts)
			Expect(coordinatorLib.Resources.Requests.Storage()).To(HaveValue(Equal(resource.MustParse("2Gi"))))
			Expect(coordinatorLib.StorageClassName).To(BeNil())

			dataSts := &appsv1.StatefulSet{}
			get(resourceName+dataSuffix, dataSts)
			dataLib := libClaim(dataSts)
			Expect(dataLib.Resources.Requests.Storage()).To(HaveValue(Equal(resource.MustParse("100Gi"))))
			Expect(dataLib.StorageClassName).To(HaveValue(Equal(customStorageClassName)))
		})
	})

	Context("when bootstrapping cluster registration", func() {
		const resourceName = "mgc-bootstrap"

		coordinatorAddress := func(ordinal int) string {
			return fmt.Sprintf("%s-coordinator-%d.%s-coordinator.%s.svc.cluster.local:7687",
				resourceName, ordinal, resourceName, resourceNamespace)
		}

		// observedCoordinator reports the coordinator with the given 1-based
		// Raft ID, which runs on the pod with ordinal ID-1.
		observedCoordinator := func(id int, role string) memgraph.Instance {
			return memgraph.Instance{
				Name:       fmt.Sprintf("coordinator_%d", id),
				BoltServer: coordinatorAddress(id - 1),
				Health:     "up",
				Role:       role,
			}
		}

		observedDataInstance := func(i int, role string) memgraph.Instance {
			return memgraph.Instance{
				Name:   fmt.Sprintf("instance_%d", i),
				Health: "up",
				Role:   role,
			}
		}

		BeforeEach(func() {
			resource := &memgraphcomv1alpha1.MemgraphCluster{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: resourceNamespace},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			Expect(k8sClient.Delete(ctx, cluster)).To(Succeed())
			deleteOwned(resourceName)
		})

		It("should not touch Memgraph before every pod is ready", func() {
			result := reconcileCluster(resourceName)

			Expect(result.RequeueAfter).To(BeNumerically(">", 0))
			Expect(fake.connects()).To(BeZero())
		})

		It("should bootstrap a fresh cluster to fully registered with one MAIN", func() {
			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)

			result := reconcileCluster(resourceName)

			leader := coordinatorAddress(0)
			Expect(fake.executedCommands()).To(Equal([]string{
				leader + ": ADD COORDINATOR 1",
				leader + ": ADD COORDINATOR 2",
				leader + ": ADD COORDINATOR 3",
				leader + ": REGISTER INSTANCE instance_0",
				leader + ": REGISTER INSTANCE instance_1",
				leader + ": SET INSTANCE instance_0 TO MAIN",
			}))
			Expect(result.RequeueAfter).To(BeNumerically(">", 0),
				"registration was issued, so a follow-up reconcile must verify convergence")

			result = reconcileCluster(resourceName)
			Expect(fake.executedCommands()).To(HaveLen(6),
				"a converged cluster must not receive further commands")
			Expect(result.RequeueAfter).To(Equal(resyncInterval),
				"a converged cluster must still reschedule a resync to catch registration drift")
		})

		It("should resume a partial bootstrap without duplicate registrations or a second MAIN", func() {
			// The state a crash mid-bootstrap leaves behind: two coordinators
			// formed, the first data instance registered and promoted. The
			// fake rejects duplicate registrations and second promotions, so
			// re-issuing anything fails this test loudly.
			fake.setInstances([]memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleMain),
			})

			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			reconcileCluster(resourceName)

			leader := coordinatorAddress(0)
			Expect(fake.executedCommands()).To(Equal([]string{
				leader + ": ADD COORDINATOR 3",
				leader + ": REGISTER INSTANCE instance_1",
			}))
		})

		It("should execute registration on the leader a follower reports", func() {
			fake.setInstances([]memgraph.Instance{
				observedCoordinator(1, memgraph.RoleFollower),
				observedCoordinator(2, memgraph.RoleLeader),
				observedCoordinator(3, memgraph.RoleFollower),
			})

			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			reconcileCluster(resourceName)

			leader := coordinatorAddress(1)
			Expect(fake.executedCommands()).To(Equal([]string{
				leader + ": REGISTER INSTANCE instance_0",
				leader + ": REGISTER INSTANCE instance_1",
				leader + ": SET INSTANCE instance_0 TO MAIN",
			}))
		})

		// A coordinator that reports no leader answers from its own state
		// machine: quorum is gone, or it stepped down or was removed from the
		// Raft cluster. Its view can be arbitrarily stale and no management
		// query it forwards would be accepted, so it is skipped rather than
		// planned against.
		It("should skip a coordinator reporting no leader and plan on the next one's view", func() {
			fake.setInstances([]memgraph.Instance{
				observedCoordinator(1, memgraph.RoleFollower),
				observedCoordinator(2, memgraph.RoleLeader),
				observedCoordinator(3, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleMain),
			})
			// coordinator_1 lost the leader and still remembers a cluster that
			// has both data instances registered.
			fake.setStaleView(coordinatorAddress(0), []memgraph.Instance{
				observedCoordinator(1, memgraph.RoleFollower),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleMain),
				observedDataInstance(1, memgraph.RoleReplica),
			})

			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			reconcileCluster(resourceName)

			leader := coordinatorAddress(1)
			Expect(fake.executedCommands()).To(Equal([]string{
				leader + ": REGISTER INSTANCE instance_1",
			}), "the leader's view is planned against, not the stale one that reports instance_1 registered")
		})

		It("should issue nothing while no coordinator reports a leader", func() {
			// Quorum lost: every coordinator answers, none names a leader.
			fake.setInstances([]memgraph.Instance{
				observedCoordinator(1, memgraph.RoleFollower),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
			})

			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			result := reconcileCluster(resourceName)

			Expect(fake.executedCommands()).To(BeEmpty(),
				"a cluster with no coordinator leader cannot be observed or written to")
			Expect(fake.connects()).To(Equal(3), "every declared coordinator is tried before giving up")
			Expect(result.RequeueAfter).To(BeNumerically(">", 0),
				"a cluster that lost its quorum is retried, not abandoned")
		})

		// The leader is whoever the coordinators elected, which need not be a
		// coordinator the CR declares — a coordinator on its way out of the
		// cluster can still hold leadership.
		It("should register on a leader outside the declared coordinator set", func() {
			fake.setInstances([]memgraph.Instance{
				observedCoordinator(1, memgraph.RoleFollower),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				observedCoordinator(4, memgraph.RoleLeader),
				observedDataInstance(0, memgraph.RoleMain),
			})

			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			reconcileCluster(resourceName)

			Expect(fake.executedCommands()).To(Equal([]string{
				coordinatorAddress(3) + ": REGISTER INSTANCE instance_1",
			}), "the undeclared leader is redirected to, and left registered as it is")
		})

		// convergedCluster is the fully registered view of the default
		// 3-coordinator, 2-data topology with instance_0 elected MAIN — the
		// steady state drift is introduced against below.
		convergedCluster := func() []memgraph.Instance {
			return []memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleMain),
				observedDataInstance(1, memgraph.RoleReplica),
			}
		}

		It("should re-register a data instance whose registration was lost, leaving MAIN untouched", func() {
			fake.setInstances(convergedCluster())
			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			reconcileCluster(resourceName)
			Expect(fake.executedCommands()).To(BeEmpty(), "the cluster started converged")

			// instance_1 loses its registration (pod rescheduled onto a fresh
			// node): drop it from the observed view and reconcile again.
			fake.setInstances([]memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleMain),
			})

			result := reconcileCluster(resourceName)

			leader := coordinatorAddress(0)
			Expect(fake.executedCommands()).To(Equal([]string{
				leader + ": REGISTER INSTANCE instance_1",
			}), "only the lost registration is re-issued; the existing MAIN is not re-promoted")
			Expect(result.RequeueAfter).To(BeNumerically(">", 0),
				"re-registration was issued, so a follow-up reconcile must verify convergence")
		})

		It("should re-add a coordinator whose registration was lost", func() {
			fake.setInstances(convergedCluster())
			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			reconcileCluster(resourceName)
			Expect(fake.executedCommands()).To(BeEmpty(), "the cluster started converged")

			// coordinator_3 disappears from the Raft cluster view.
			fake.setInstances([]memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleMain),
				observedDataInstance(1, memgraph.RoleReplica),
			})

			reconcileCluster(resourceName)

			leader := coordinatorAddress(0)
			Expect(fake.executedCommands()).To(Equal([]string{
				leader + ": ADD COORDINATOR 3",
			}), "only the missing coordinator is re-added")
		})

		It("should stay a no-op on a converged cluster across repeated resyncs", func() {
			fake.setInstances(convergedCluster())
			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)

			for range 3 {
				result := reconcileCluster(resourceName)
				Expect(fake.executedCommands()).To(BeEmpty(),
					"a converged cluster must never receive commands, however often it is resynced")
				Expect(result.RequeueAfter).To(Equal(resyncInterval),
					"each converged reconcile reschedules the drift-detection resync")
			}
		})
	})

	Context("when scaling the topology of a live cluster", func() {
		const resourceName = "mgc-scale"

		coordinatorAddress := func(ordinal int) string {
			return fmt.Sprintf("%s-coordinator-%d.%s-coordinator.%s.svc.cluster.local:7687",
				resourceName, ordinal, resourceName, resourceNamespace)
		}

		// convergedWithMainOn is the fully registered default 3/2 topology with the
		// data instance on the given ordinal elected MAIN, so a spec can put MAIN
		// where it needs it before lowering a count.
		convergedWithMainOn := func(mainOrdinal int) []memgraph.Instance {
			instances := make([]memgraph.Instance, 0, 5)
			for id := 1; id <= 3; id++ {
				role := memgraph.RoleFollower
				if id == 1 {
					role = memgraph.RoleLeader
				}
				instances = append(instances, memgraph.Instance{
					Name: fmt.Sprintf("coordinator_%d", id), BoltServer: coordinatorAddress(id - 1),
					Health: memgraph.HealthUp, Role: role,
				})
			}
			for ordinal := range 2 {
				role := memgraph.RoleReplica
				if ordinal == mainOrdinal {
					role = memgraph.RoleMain
				}
				instances = append(instances, memgraph.Instance{
					Name: fmt.Sprintf("instance_%d", ordinal), Health: memgraph.HealthUp, Role: role,
				})
			}
			return instances
		}

		status := func() memgraphcomv1alpha1.MemgraphClusterStatus {
			GinkgoHelper()
			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			return cluster.Status
		}

		convergedCondition := func() *metav1.Condition {
			GinkgoHelper()
			return apimeta.FindStatusCondition(status().Conditions, memgraphcomv1alpha1.ConditionConverged)
		}

		// setCounts edits the declared topology of the live cluster.
		setCounts := func(coordinators, dataInstances int32) {
			GinkgoHelper()
			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			cluster.Spec.Coordinators = ptr.To(coordinators)
			cluster.Spec.DataInstances = ptr.To(dataInstances)
			Expect(k8sClient.Update(ctx, cluster)).To(Succeed())
		}

		replicas := func(suffix string) int32 {
			GinkgoHelper()
			sts := &appsv1.StatefulSet{}
			get(resourceName+suffix, sts)
			Expect(sts.Spec.Replicas).NotTo(BeNil())
			return *sts.Spec.Replicas
		}

		// bootstrapped drives the default 3/2 cluster to converged, so the specs
		// below start from a live, fully registered cluster. It returns how many
		// commands that took, which is the baseline sinceBootstrap counts from.
		bootstrapped := func() int {
			GinkgoHelper()
			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			reconcileCluster(resourceName)
			reconcileCluster(resourceName)
			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			Expect(apimeta.IsStatusConditionTrue(cluster.Status.Conditions,
				memgraphcomv1alpha1.ConditionConverged)).To(BeTrue())
			return len(fake.executedCommands())
		}

		// sinceBootstrap is the commands the spec's own topology edit caused, so
		// the assertions are not about the bootstrap that set the scene.
		sinceBootstrap := func(baseline int) []string {
			GinkgoHelper()
			executed := fake.executedCommands()
			Expect(len(executed)).To(BeNumerically(">=", baseline))
			return executed[baseline:]
		}

		BeforeEach(func() {
			resource := &memgraphcomv1alpha1.MemgraphCluster{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: resourceNamespace},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			Expect(k8sClient.Delete(ctx, cluster)).To(Succeed())
			deleteOwned(resourceName)
		})

		It("should grow both roles and register only the added members", func() {
			baseline := bootstrapped()

			setCounts(5, 3)
			// The added pods are not ready yet, so this pass only widens the
			// StatefulSets.
			reconcileCluster(resourceName)
			Expect(replicas(coordinatorSuffix)).To(Equal(int32(5)))
			Expect(replicas(dataSuffix)).To(Equal(int32(3)))
			Expect(sinceBootstrap(baseline)).To(BeEmpty(),
				"registration waits until every pod of the grown topology is ready")

			markWorkloadsReady(resourceName)
			reconcileCluster(resourceName)

			leader := coordinatorAddress(0)
			Expect(sinceBootstrap(baseline)).To(Equal([]string{
				leader + ": ADD COORDINATOR 4",
				leader + ": ADD COORDINATOR 5",
				leader + ": REGISTER INSTANCE instance_2",
			}), "the members the cluster already has are left alone, and no MAIN is re-promoted")

			reconcileCluster(resourceName)
			s := status()
			Expect(s.Coordinators).To(Equal(int32(5)))
			Expect(s.DataInstances).To(Equal(int32(3)))
			Expect(s.Main).To(Equal("instance_0"), "growing the cluster does not move MAIN")
			converged := apimeta.FindStatusCondition(s.Conditions, memgraphcomv1alpha1.ConditionConverged)
			Expect(converged.Status).To(Equal(metav1.ConditionTrue))
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonAllInstancesRegistered))
		})

		// grownToFive drives the cluster to a converged five-coordinator topology,
		// which is the only shape a coordinator shrink can start from: the count must
		// stay odd and at or above three, so five is the smallest cluster with members
		// to drop. It returns the command count the shrink assertions start from.
		grownToFive := func() int {
			GinkgoHelper()
			bootstrapped()
			setCounts(5, 2)
			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			reconcileCluster(resourceName)
			reconcileCluster(resourceName)
			Expect(apimeta.IsStatusConditionTrue(status().Conditions,
				memgraphcomv1alpha1.ConditionConverged)).To(BeTrue())
			return len(fake.executedCommands())
		}

		// Raft membership is given up before the pods are, so no removed member's pod
		// outlives its vote. With the leader on a survivor that is the whole shrink:
		// removing a follower needs no leadership dance.
		It("should remove retiring coordinators from Raft and only then shed their pods", func() {
			baseline := grownToFive()

			setCounts(3, 2)
			reconcileCluster(resourceName)

			leader := coordinatorAddress(0)
			Expect(sinceBootstrap(baseline)).To(Equal([]string{
				leader + ": REMOVE COORDINATOR 4",
				leader + ": REMOVE COORDINATOR 5",
			}), "both retiring members leave the Raft cluster in one pass under a surviving leader")
			Expect(replicas(coordinatorSuffix)).To(Equal(int32(5)),
				"a pass with pending commands must never lower the replica count")
			converged := convergedCondition()
			Expect(converged.Status).To(Equal(metav1.ConditionFalse))
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonRetirementInProgress))
			Expect(converged.Message).To(ContainSubstring("coordinator_4"),
				"the condition must name the coordinators being retired")

			By("shedding the pods once the members have left the Raft cluster")
			reconcileCluster(resourceName)
			Expect(replicas(coordinatorSuffix)).To(Equal(int32(3)))
			Expect(sinceBootstrap(baseline)).To(HaveLen(2), "the removals are not re-issued")
			Expect(convergedCondition().Reason).To(Equal(memgraphcomv1alpha1.ReasonRetirementInProgress))

			By("reporting the shrink as finished once the StatefulSet runs the declared count")
			reconcileCluster(resourceName)
			s := status()
			Expect(s.Coordinators).To(Equal(int32(3)))
			Expect(s.Main).To(Equal("instance_0"), "shrinking the coordinators does not move MAIN")
			Expect(apimeta.IsStatusConditionTrue(s.Conditions,
				memgraphcomv1alpha1.ConditionConverged)).To(BeTrue())
		})

		// A StatefulSet sheds its highest ordinals, so the Raft leader may well sit on
		// one of them — and Raft refuses to remove its own leader. The plan then ends
		// with YIELD LEADERSHIP and nothing after it, because the election picks the
		// successor: the pass stops there and the next one removes under whoever won.
		// The fake refuses a removal aimed at its leader, so a plan that skipped the
		// yield would fail this spec rather than quietly working.
		It("should yield leadership off a retiring coordinator before removing it", func() {
			baseline := grownToFive()

			By("parking Raft leadership on the coordinator the shrink retires")
			fake.setLeader("coordinator_4")

			setCounts(3, 2)
			reconcileCluster(resourceName)

			retiringLeader := coordinatorAddress(3)
			Expect(sinceBootstrap(baseline)).To(Equal([]string{
				retiringLeader + ": REMOVE COORDINATOR 5",
				retiringLeader + ": YIELD LEADERSHIP",
			}), "the yield comes last, after the removal the planner could still order safely")
			converged := convergedCondition()
			Expect(converged.Status).To(Equal(metav1.ConditionFalse))
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonLeadershipTransferInProgress))
			Expect(converged.Message).To(ContainSubstring("coordinator_4"),
				"the condition must name the coordinator being moved off leadership")
			Expect(replicas(coordinatorSuffix)).To(Equal(int32(5)),
				"a pass with a pending yield must never lower the replica count")

			By("removing the former leader on the next pass, under whichever coordinator won")
			reconcileCluster(resourceName)
			Expect(sinceBootstrap(baseline)).To(Equal([]string{
				retiringLeader + ": REMOVE COORDINATOR 5",
				retiringLeader + ": YIELD LEADERSHIP",
				coordinatorAddress(0) + ": REMOVE COORDINATOR 4",
			}))
			Expect(convergedCondition().Reason).To(Equal(memgraphcomv1alpha1.ReasonRetirementInProgress))

			By("shedding the pods and converging once the Raft cluster is down to three")
			reconcileCluster(resourceName)
			Expect(replicas(coordinatorSuffix)).To(Equal(int32(3)))
			reconcileCluster(resourceName)
			s := status()
			Expect(s.Coordinators).To(Equal(int32(3)))
			Expect(apimeta.IsStatusConditionTrue(s.Conditions,
				memgraphcomv1alpha1.ConditionConverged)).To(BeTrue())
		})

		// Raising the count back to what is already running is a no-op scale: the
		// members are still registered, so nothing is planned and nothing is applied.
		It("should converge again when a lowered coordinator count is raised back", func() {
			baseline := grownToFive()

			setCounts(3, 2)
			setCounts(5, 2)
			reconcileCluster(resourceName)

			Expect(sinceBootstrap(baseline)).To(BeEmpty(),
				"a shrink that was undone before it was acted on touches the cluster not at all")
			Expect(replicas(coordinatorSuffix)).To(Equal(int32(5)))
			Expect(apimeta.IsStatusConditionTrue(status().Conditions,
				memgraphcomv1alpha1.ConditionConverged)).To(BeTrue())
		})

		// Both counts lowered in one edit: each role's retirement is planned
		// independently, and both StatefulSets shrink once the plan is empty.
		It("should retire members of both roles in one edit", func() {
			baseline := grownToFive()

			setCounts(3, 1)
			reconcileCluster(resourceName)

			leader := coordinatorAddress(0)
			Expect(sinceBootstrap(baseline)).To(Equal([]string{
				leader + ": UNREGISTER INSTANCE instance_1",
				leader + ": REMOVE COORDINATOR 4",
				leader + ": REMOVE COORDINATOR 5",
			}), "the surviving MAIN is left alone, and each role's removals are planned on their own")
			Expect(replicas(coordinatorSuffix)).To(Equal(int32(5)))
			Expect(replicas(dataSuffix)).To(Equal(int32(2)))
			Expect(convergedCondition().Message).To(SatisfyAll(
				ContainSubstring("coordinator_4"), ContainSubstring("instance_1")),
				"the condition must name the retiring members of both roles")

			reconcileCluster(resourceName)
			Expect(replicas(coordinatorSuffix)).To(Equal(int32(3)))
			Expect(replicas(dataSuffix)).To(Equal(int32(1)))

			reconcileCluster(resourceName)
			s := status()
			Expect(s.Coordinators).To(Equal(int32(3)))
			Expect(s.DataInstances).To(Equal(int32(1)))
			Expect(apimeta.IsStatusConditionTrue(s.Conditions,
				memgraphcomv1alpha1.ConditionConverged)).To(BeTrue())
		})

		// The whole point of the shrink: the member beyond the declared count leaves
		// the cluster before its pod does, so the coordinators never expect an
		// instance whose pod is gone.
		It("should unregister a retiring data instance and only then shed its pod", func() {
			baseline := bootstrapped()
			Expect(status().Main).To(Equal("instance_0"))

			setCounts(3, 1)
			reconcileCluster(resourceName)

			leader := coordinatorAddress(0)
			Expect(sinceBootstrap(baseline)).To(Equal([]string{
				leader + ": UNREGISTER INSTANCE instance_1",
			}), "the MAIN survives the shrink, so nothing but the removal is issued")
			Expect(replicas(dataSuffix)).To(Equal(int32(2)),
				"a pass with pending commands must never lower the replica count")
			converged := convergedCondition()
			Expect(converged.Status).To(Equal(metav1.ConditionFalse))
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonRetirementInProgress))
			Expect(converged.Message).To(ContainSubstring("instance_1"),
				"the condition must name the instance being retired")

			By("shedding the pod once the instance has left the cluster")
			reconcileCluster(resourceName)
			Expect(replicas(dataSuffix)).To(Equal(int32(1)))
			Expect(sinceBootstrap(baseline)).To(HaveLen(1), "the removal is not re-issued")
			Expect(convergedCondition().Reason).To(Equal(memgraphcomv1alpha1.ReasonRetirementInProgress))

			By("reporting the shrink as finished once the StatefulSet runs the declared count")
			reconcileCluster(resourceName)
			s := status()
			Expect(s.DataInstances).To(Equal(int32(1)))
			Expect(s.Main).To(Equal("instance_0"), "the surviving MAIN was never moved")
			Expect(apimeta.IsStatusConditionTrue(s.Conditions,
				memgraphcomv1alpha1.ConditionConverged)).To(BeTrue())
		})

		// Memgraph refuses to unregister the MAIN, so a retiring instance holding it
		// is demoted and a survivor promoted in its place — within the same pass, on
		// the same leader connection, so the cluster is MAIN-less for the time
		// between two queries.
		It("should move MAIN off a retiring data instance before unregistering it", func() {
			fake.setInstances(convergedWithMainOn(1))
			baseline := bootstrapped()
			Expect(status().Main).To(Equal("instance_1"))

			setCounts(3, 1)
			reconcileCluster(resourceName)

			leader := coordinatorAddress(0)
			Expect(sinceBootstrap(baseline)).To(Equal([]string{
				leader + ": DEMOTE INSTANCE instance_1",
				leader + ": SET INSTANCE instance_0 TO MAIN",
				leader + ": UNREGISTER INSTANCE instance_1",
			}), "demote, promote and unregister issue in one pass against one leader")
			Expect(replicas(dataSuffix)).To(Equal(int32(2)),
				"a pass with pending commands must never lower the replica count")

			reconcileCluster(resourceName)
			Expect(replicas(dataSuffix)).To(Equal(int32(1)))
			reconcileCluster(resourceName)

			s := status()
			Expect(s.Main).To(Equal("instance_0"), "MAIN moved to the surviving instance")
			Expect(s.DataInstances).To(Equal(int32(1)))
			Expect(apimeta.IsStatusConditionTrue(s.Conditions,
				memgraphcomv1alpha1.ConditionConverged)).To(BeTrue())
		})

		// The readiness gate covers the pods on their way out too: they belong to
		// the StatefulSet the operator is still holding at its current size. A
		// retiring pod that cannot become ready therefore blocks its own removal,
		// which is a deliberate trade — the alternative is acting on a cluster whose
		// state is only half known.
		It("should not retire anything while a pod of the held StatefulSet is unready", func() {
			baseline := bootstrapped()

			setCounts(3, 1)
			sts := &appsv1.StatefulSet{}
			get(resourceName+dataSuffix, sts)
			sts.Status.ReadyReplicas = *sts.Spec.Replicas - 1
			sts.Status.AvailableReplicas = sts.Status.ReadyReplicas
			Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())

			reconcileCluster(resourceName)

			Expect(sinceBootstrap(baseline)).To(BeEmpty(),
				"a half-known cluster is not written to, retirement included")
			Expect(replicas(dataSuffix)).To(Equal(int32(2)))
			converged := convergedCondition()
			Expect(converged.Status).To(Equal(metav1.ConditionFalse))
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonWorkloadsNotReady))
		})

		It("should report the registered counts as observed, not as declared", func() {
			bootstrapped()
			Expect(status().DataInstances).To(Equal(int32(2)))

			// instance_1 loses its registration: the count reports what the
			// cluster has, which is what makes it worth watching during a scale.
			fake.setInstances([]memgraph.Instance{
				{
					Name: "coordinator_1", BoltServer: coordinatorAddress(0),
					Health: memgraph.HealthUp, Role: memgraph.RoleLeader,
				},
				{
					Name: "coordinator_2", BoltServer: coordinatorAddress(1),
					Health: memgraph.HealthUp, Role: memgraph.RoleFollower,
				},
				{Name: "instance_0", Health: memgraph.HealthUp, Role: memgraph.RoleMain},
			})
			reconcileCluster(resourceName)

			s := status()
			Expect(s.Coordinators).To(Equal(int32(2)), "coordinator_3 is no longer a member")
			Expect(s.DataInstances).To(Equal(int32(1)))
		})
	})

	Context("when reporting status and conditions", func() {
		const resourceName = "mgc-status"

		observedCoordinator := func(id int, role string) memgraph.Instance {
			return memgraph.Instance{
				Name: fmt.Sprintf("coordinator_%d", id),
				BoltServer: fmt.Sprintf("%s-coordinator-%d.%s-coordinator.%s.svc.cluster.local:7687",
					resourceName, id-1, resourceName, resourceNamespace),
				Health: "up",
				Role:   role,
			}
		}
		observedDataInstance := func(i int, role string) memgraph.Instance {
			return memgraph.Instance{Name: fmt.Sprintf("instance_%d", i), Health: "up", Role: role}
		}
		convergedCluster := func() []memgraph.Instance {
			return []memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleMain),
				observedDataInstance(1, memgraph.RoleReplica),
			}
		}

		status := func() memgraphcomv1alpha1.MemgraphClusterStatus {
			GinkgoHelper()
			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			return cluster.Status
		}
		condition := func(condType string) *metav1.Condition {
			GinkgoHelper()
			s := status()
			return apimeta.FindStatusCondition(s.Conditions, condType)
		}

		BeforeEach(func() {
			resource := &memgraphcomv1alpha1.MemgraphCluster{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: resourceNamespace},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			Expect(k8sClient.Delete(ctx, cluster)).To(Succeed())
			deleteOwned(resourceName)
		})

		It("should report bootstrapping while workload pods are not ready", func() {
			reconcileCluster(resourceName)

			s := status()
			Expect(s.Main).To(BeEmpty(), "no MAIN is known before the cluster is observed")
			ready := condition(memgraphcomv1alpha1.ConditionReady)
			Expect(ready).NotTo(BeNil())
			Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			Expect(ready.Reason).To(Equal(memgraphcomv1alpha1.ReasonWorkloadsNotReady))
			converged := condition(memgraphcomv1alpha1.ConditionConverged)
			Expect(converged).NotTo(BeNil())
			Expect(converged.Status).To(Equal(metav1.ConditionFalse))
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonWorkloadsNotReady))
		})

		It("should report degraded when no coordinator is reachable", func() {
			fake.setConnectErr(errors.New("connection refused"))
			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			reconcileCluster(resourceName)

			ready := condition(memgraphcomv1alpha1.ConditionReady)
			Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			Expect(ready.Reason).To(Equal(memgraphcomv1alpha1.ReasonCoordinatorUnreachable))
			converged := condition(memgraphcomv1alpha1.ConditionConverged)
			Expect(converged.Status).To(Equal(metav1.ConditionFalse))
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonCoordinatorUnreachable))
		})

		// Coordinators that answer but have no leader between them are a
		// different problem from coordinators that do not answer at all — the
		// pods are up and serving Bolt, what is missing is the Raft quorum — so
		// they get their own reason.
		It("should report a missing quorum apart from unreachable coordinators", func() {
			fake.setInstances([]memgraph.Instance{
				observedCoordinator(1, memgraph.RoleFollower),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleMain),
			})
			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			reconcileCluster(resourceName)

			for _, condType := range []string{
				memgraphcomv1alpha1.ConditionReady,
				memgraphcomv1alpha1.ConditionConverged,
			} {
				cond := condition(condType)
				Expect(cond.Status).To(Equal(metav1.ConditionFalse), "condition %s", condType)
				Expect(cond.Reason).To(Equal(memgraphcomv1alpha1.ReasonNoCoordinatorLeader), "condition %s", condType)
			}
		})

		// A rejected apply is retried behind the scenes forever, so the resource
		// itself has to say what the API server refused — otherwise the
		// conditions keep describing the cluster that is still running while the
		// declared spec never lands. Removing a role's log storage claim is the
		// realistic trigger: Kubernetes forbids changing a StatefulSet's
		// volumeClaimTemplates, so the flip needs the StatefulSet recreated.
		It("should report the API server's rejection when applying a workload fails", func() {
			fake.setInstances(convergedCluster())
			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			reconcileCluster(resourceName)
			Expect(condition(memgraphcomv1alpha1.ConditionReady).Status).To(Equal(metav1.ConditionTrue))
			Expect(condition(memgraphcomv1alpha1.ConditionConverged).Status).To(Equal(metav1.ConditionTrue))

			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			cluster.Spec.Storage.Data.CreateLogStorageClaim = ptr.To(false)
			Expect(k8sClient.Update(ctx, cluster)).To(Succeed())

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: resourceName, Namespace: resourceNamespace},
			})
			Expect(err).To(HaveOccurred(), "the API server refuses to drop a volume claim template")

			s := status()
			Expect(s.Main).To(Equal("instance_0"), "the last observed MAIN survives an apply failure")
			for _, condType := range []string{
				memgraphcomv1alpha1.ConditionReady,
				memgraphcomv1alpha1.ConditionConverged,
			} {
				cond := condition(condType)
				Expect(cond.Status).To(Equal(metav1.ConditionFalse), "condition %s", condType)
				Expect(cond.Reason).To(Equal(memgraphcomv1alpha1.ReasonApplyFailed), "condition %s", condType)
				Expect(cond.Message).To(ContainSubstring(resourceName+dataSuffix),
					"condition %s must name the object that was refused", condType)
				Expect(cond.Message).To(ContainSubstring("Forbidden"),
					"condition %s must carry the API server's own words", condType)
			}
		})

		It("should report ready and converged once the cluster is bootstrapped", func() {
			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			reconcileCluster(resourceName)
			// Second reconcile observes the registrations issued by the first.
			reconcileCluster(resourceName)

			s := status()
			Expect(s.Main).To(Equal("instance_0"))
			Expect(s.Coordinators).To(Equal(int32(3)), "every declared coordinator is registered")
			Expect(s.DataInstances).To(Equal(int32(2)))
			ready := condition(memgraphcomv1alpha1.ConditionReady)
			Expect(ready.Status).To(Equal(metav1.ConditionTrue))
			Expect(ready.Reason).To(Equal(memgraphcomv1alpha1.ReasonMainElected))
			converged := condition(memgraphcomv1alpha1.ConditionConverged)
			Expect(converged.Status).To(Equal(metav1.ConditionTrue))
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonAllInstancesRegistered))
		})

		It("should stay ready but drop convergence while a lost registration is restored", func() {
			fake.setInstances(convergedCluster())
			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			reconcileCluster(resourceName)
			Expect(condition(memgraphcomv1alpha1.ConditionConverged).Status).To(Equal(metav1.ConditionTrue))

			// A replica loses its registration; the MAIN keeps serving.
			fake.setInstances([]memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleMain),
			})
			reconcileCluster(resourceName)

			s := status()
			Expect(s.Main).To(Equal("instance_0"), "the serving MAIN is unchanged")
			ready := condition(memgraphcomv1alpha1.ConditionReady)
			Expect(ready.Status).To(Equal(metav1.ConditionTrue),
				"a cluster with a MAIN still serves while a replica is re-registered")
			converged := condition(memgraphcomv1alpha1.ConditionConverged)
			Expect(converged.Status).To(Equal(metav1.ConditionFalse))
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonRegistrationInProgress))
		})

		It("should track MAIN across a coordinator-driven failover", func() {
			fake.setInstances(convergedCluster())
			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			reconcileCluster(resourceName)
			Expect(status().Main).To(Equal("instance_0"))

			// The Raft coordinators fail over to instance_1; the operator only
			// observes the new MAIN, it never promotes one.
			fake.setInstances([]memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleReplica),
				observedDataInstance(1, memgraph.RoleMain),
			})
			reconcileCluster(resourceName)

			Expect(status().Main).To(Equal("instance_1"))
			Expect(fake.executedCommands()).To(BeEmpty(),
				"failover belongs to the coordinators; the operator issues no promotion")
		})

		It("should update status through the subresource without modifying spec", func() {
			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			specBefore := cluster.Spec.DeepCopy()

			fake.setInstances(convergedCluster())
			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			reconcileCluster(resourceName)

			get(resourceName, cluster)
			Expect(&cluster.Spec).To(Equal(specBefore), "status updates must never mutate spec")
			Expect(cluster.Status.Conditions).NotTo(BeEmpty())
		})
	})
})
