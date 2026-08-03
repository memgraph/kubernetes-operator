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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

	// memgraphDbName is the value of the app.kubernetes.io/name label the operator
	// stamps on everything, and the container name inside its pods.
	memgraphDbName = "memgraph"
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

		// convergedWith is the fully registered 3-coordinator topology with the given
		// number of data instances, the one on mainOrdinal elected MAIN — so a spec
		// can put MAIN where it needs it before lowering a count.
		convergedWith := func(dataInstances, mainOrdinal int) []memgraph.Instance {
			instances := make([]memgraph.Instance, 0, 3+dataInstances)
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
			for ordinal := range dataInstances {
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

		// convergedWithMainOn is that view at the default 2 data instances.
		convergedWithMainOn := func(mainOrdinal int) []memgraph.Instance {
			return convergedWith(2, mainOrdinal)
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

		// Moving MAIN is the one step of a retirement that can lose data, so it waits
		// for a survivor that actually holds the writes. Everything else about the
		// scale-down waits with it: the demotion would leave the cluster serving from
		// an instance missing transactions, and the unregistration cannot precede the
		// demotion at all.
		It("should not move MAIN off a retiring instance while every survivor is behind", func() {
			fake.setInstances(convergedWithMainOn(1))
			fake.setBehind("instance_0", 12)
			baseline := bootstrapped()
			Expect(status().Main).To(Equal("instance_1"))

			setCounts(3, 1)
			reconcileCluster(resourceName)

			Expect(sinceBootstrap(baseline)).To(BeEmpty(),
				"no demotion, no promotion, and no unregistration of a MAIN that cannot be demoted")
			Expect(replicas(dataSuffix)).To(Equal(int32(2)),
				"the retiring pod must outlive its registration, so the count is held")
			converged := convergedCondition()
			Expect(converged.Status).To(Equal(metav1.ConditionFalse))
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonNoCaughtUpSurvivor))
			// The retiring MAIN is still MAIN, and still serving: a paused scale-down
			// costs availability nothing, which is what makes waiting the better trade.
			Expect(apimeta.IsStatusConditionTrue(status().Conditions,
				memgraphcomv1alpha1.ConditionReady)).To(BeTrue())
			Expect(status().Main).To(Equal("instance_1"))

			By("holding there for as long as the survivor stays behind")
			reconcileCluster(resourceName)
			Expect(sinceBootstrap(baseline)).To(BeEmpty())
			Expect(replicas(dataSuffix)).To(Equal(int32(2)))
			Expect(convergedCondition().Reason).To(Equal(memgraphcomv1alpha1.ReasonNoCaughtUpSurvivor))

			By("handing MAIN over once the survivor has caught up")
			fake.setBehind("instance_0", 0)
			reconcileCluster(resourceName)

			leader := coordinatorAddress(0)
			Expect(sinceBootstrap(baseline)).To(Equal([]string{
				leader + ": DEMOTE INSTANCE instance_1",
				leader + ": SET INSTANCE instance_0 TO MAIN",
				leader + ": UNREGISTER INSTANCE instance_1",
			}), "the retirement resumes from where it stalled, in one pass")

			reconcileCluster(resourceName)
			Expect(replicas(dataSuffix)).To(Equal(int32(1)))
			reconcileCluster(resourceName)

			s := status()
			Expect(s.Main).To(Equal("instance_0"))
			Expect(s.DataInstances).To(Equal(int32(1)))
			Expect(apimeta.IsStatusConditionTrue(s.Conditions,
				memgraphcomv1alpha1.ConditionConverged)).To(BeTrue())
		})

		// The promotion is the one command of a retirement with no second chance: the
		// demotion has already landed, and a later pass cannot recompute which
		// survivor is safe because lag is served by the MAIN. So it carries every
		// survivor the lag view proved caught up, and a refused one moves to the next.
		It("should promote the next caught-up survivor when the first one is refused", func() {
			setCounts(3, 3)
			fake.setInstances(convergedWith(3, 2))
			baseline := bootstrapped()
			Expect(status().Main).To(Equal("instance_2"))

			fake.rejectCommand("SET INSTANCE instance_0 TO MAIN", errors.New("instance is not registered"))
			setCounts(3, 2)
			reconcileCluster(resourceName)

			leader := coordinatorAddress(0)
			Expect(sinceBootstrap(baseline)).To(Equal([]string{
				leader + ": DEMOTE INSTANCE instance_2",
				leader + ": SET INSTANCE instance_1 TO MAIN",
				leader + ": UNREGISTER INSTANCE instance_2",
			}), "the refused survivor is skipped and the retirement completes in the same pass")

			reconcileCluster(resourceName)
			Expect(replicas(dataSuffix)).To(Equal(int32(2)))
			reconcileCluster(resourceName)

			s := status()
			Expect(s.Main).To(Equal("instance_1"))
			Expect(apimeta.IsStatusConditionTrue(s.Conditions,
				memgraphcomv1alpha1.ConditionConverged)).To(BeTrue())
		})

		// When no survivor can be promoted the cluster would be left MAIN-less by the
		// demotion that already landed, so MAIN goes back to the instance being
		// retired: it was MAIN a moment ago and a MAIN-less cluster accepts no writes,
		// so nothing has advanced past it. The pass still fails — the cluster serves
		// again, but the retirement made no progress and has to be retried.
		It("should restore MAIN to the retiring instance when every survivor is refused", func() {
			fake.setInstances(convergedWithMainOn(1))
			baseline := bootstrapped()
			Expect(status().Main).To(Equal("instance_1"))

			fake.rejectCommand("SET INSTANCE instance_0 TO MAIN", errors.New("instance is down"))
			setCounts(3, 1)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: resourceName, Namespace: resourceNamespace},
			})
			Expect(err).To(HaveOccurred(), "a rolled-back handover is reported, not read as progress")

			leader := coordinatorAddress(0)
			Expect(sinceBootstrap(baseline)).To(Equal([]string{
				leader + ": DEMOTE INSTANCE instance_1",
				leader + ": SET INSTANCE instance_1 TO MAIN",
			}), "MAIN goes back to the demoted instance, and the unregistration never runs")
			Expect(replicas(dataSuffix)).To(Equal(int32(2)),
				"the retiring pod outlives a retirement that did not finish")

			s := status()
			Expect(s.Main).To(Equal("instance_1"))
			Expect(apimeta.IsStatusConditionTrue(s.Conditions, memgraphcomv1alpha1.ConditionReady)).To(BeTrue(),
				"the cluster serves from the restored MAIN")
			converged := convergedCondition()
			Expect(converged.Status).To(Equal(metav1.ConditionFalse))
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonRegistrationFailed))
			Expect(converged.Message).To(ContainSubstring("MAIN was restored to the retiring instance instance_1"))
			Expect(converged.Message).To(ContainSubstring("instance is down"),
				"the condition carries why the survivor was refused")
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

		// The gate has to key off the count the pass applied, not the spec.replicas it
		// can read back. Production reads through the informer cache, so a few lines
		// after a scale-up that field is still the pre-apply value — and it agrees
		// with a status.readyReplicas from the same old snapshot, because the cluster
		// really was converged at the old size. Two stale numbers that agree report a
		// grown topology as ready, and registration then names a pod Kubernetes has
		// not been asked to create.
		//
		// The gate is called directly here: the envtest client is uncached, so the
		// staleness itself cannot be reproduced, only the comparison it would defeat.
		// A StatefulSet left at 2 ready out of 2 is exactly what that stale read looks
		// like, and the pass that intends 3 must not accept it.
		It("should gate readiness on the applied count, not the StatefulSet's own spec", func() {
			bootstrapped()
			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			Expect(replicas(dataSuffix)).To(Equal(int32(2)))

			held := replicaCounts{
				coordinators: roleReplicas{name: resourceName + coordinatorSuffix, declared: 3, applied: 3},
				data:         roleReplicas{name: resourceName + dataSuffix, declared: 2, applied: 2},
			}
			// A zero rolloutRoles is a cluster with no restart under way, which is what
			// keeps this about the count comparison alone: the gate only ever tolerates
			// an unready pod while a role actually has pods left to restart.
			ready, err := reconciler.workloadsReady(ctx, cluster, held, rolloutRoles{})
			Expect(err).NotTo(HaveOccurred())
			Expect(ready).To(BeTrue(), "the cluster is ready at the size this pass applies")

			grown := held
			grown.data.declared, grown.data.applied = 3, 3
			ready, err = reconciler.workloadsReady(ctx, cluster, grown, rolloutRoles{})
			Expect(err).NotTo(HaveOccurred())
			Expect(ready).To(BeFalse(),
				"a pass applying 3 must not read 2-ready-of-2 as ready, whatever spec.replicas still says")
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

		// The same argument as the rejected apply, one layer down: a registration
		// command the leader refuses is reissued on every pass forever, so a
		// resource that only ever says "registration in progress" hides a cluster
		// that will never converge. The MAIN keeps serving throughout, so Ready is
		// the one condition that stays True.
		It("should report a registration command the coordinator leader rejected", func() {
			fake.setInstances(convergedCluster())
			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			reconcileCluster(resourceName)
			Expect(condition(memgraphcomv1alpha1.ConditionConverged).Status).To(Equal(metav1.ConditionTrue))

			// A replica loses its registration and the leader refuses to take it
			// back, which is the shape of a plan no retry can converge.
			fake.setInstances([]memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleMain),
			})
			fake.rejectCommand("REGISTER INSTANCE instance_1", errors.New("replication port already in use"))

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: resourceName, Namespace: resourceNamespace},
			})
			Expect(err).To(HaveOccurred(), "a rejected command fails the pass so it is retried with backoff")

			s := status()
			Expect(s.Main).To(Equal("instance_0"), "the last observed MAIN survives a rejected command")
			ready := condition(memgraphcomv1alpha1.ConditionReady)
			Expect(ready.Status).To(Equal(metav1.ConditionTrue),
				"a cluster with a MAIN keeps serving while a registration is refused")
			converged := condition(memgraphcomv1alpha1.ConditionConverged)
			Expect(converged.Status).To(Equal(metav1.ConditionFalse))
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonRegistrationFailed))
			Expect(converged.Message).To(ContainSubstring("instance_1"),
				"the condition must name the command that was refused")
			Expect(converged.Message).To(ContainSubstring("replication port already in use"),
				"the condition must carry the coordinator's own words")
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

		// A MAIN whose pod is gone keeps its role in the coordinators' Raft state, so
		// the role alone would claim the cluster serves writes for the whole failover
		// window — including every window a rolling restart opens on purpose.
		It("should report NotReady while the MAIN is unreachable", func() {
			fake.setInstances(convergedCluster())
			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			reconcileCluster(resourceName)
			Expect(status().Main).To(Equal("instance_0"))

			fake.setInstances([]memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				func() memgraph.Instance {
					main := observedDataInstance(0, memgraph.RoleMain)
					main.Health = "down"
					return main
				}(),
				observedDataInstance(1, memgraph.RoleReplica),
			})
			reconcileCluster(resourceName)

			Expect(status().Main).To(BeEmpty(), "an unreachable MAIN is not a MAIN the cluster can serve from")
			ready := condition(memgraphcomv1alpha1.ConditionReady)
			Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			Expect(ready.Reason).To(Equal(memgraphcomv1alpha1.ReasonNoMainElected))
			Expect(fake.executedCommands()).To(BeEmpty(),
				"the coordinators own the failover; the operator issues no promotion")
		})
	})

	Context("when a changed pod template has to be rolled through the cluster", func() {
		const (
			resourceName = "mgc-rollout"
			oldRevision  = "mgc-rollout-6c9f8b7d5"
			newRevision  = "mgc-rollout-77b4c8f9d"
		)

		observedCoordinator := func(id int, role string) memgraph.Instance {
			host := fmt.Sprintf("%s-coordinator-%d.%s-coordinator.%s.svc.cluster.local",
				resourceName, id-1, resourceName, resourceNamespace)
			return memgraph.Instance{
				Name: fmt.Sprintf("coordinator_%d", id), BoltServer: host + ":7687",
				CoordinatorServer: host + ":12000", ManagementServer: host + ":10000",
				Health: "up", Role: role,
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
		condition := func(condType string) *metav1.Condition {
			GinkgoHelper()
			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			return apimeta.FindStatusCondition(cluster.Status.Conditions, condType)
		}

		// putPod stands in for the StatefulSet controller envtest does not run: it
		// creates or replaces one role pod at the given revision, ready.
		putPod := func(suffix, component string, ordinal int, revision string) {
			GinkgoHelper()
			name := fmt.Sprintf("%s%s-%d", resourceName, suffix, ordinal)
			existing := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: resourceNamespace}}
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, existing))).To(Succeed())
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: resourceNamespace,
					Labels: map[string]string{
						"app.kubernetes.io/name":        memgraphDbName,
						"app.kubernetes.io/instance":    resourceName,
						"app.kubernetes.io/component":   component,
						"app.kubernetes.io/managed-by":  "memgraph-operator",
						appsv1.StatefulSetRevisionLabel: revision,
					},
				},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: memgraphDbName, Image: memgraphDbName}}},
			}
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())
			pod.Status.Conditions = []corev1.PodCondition{{
				Type: corev1.PodReady, Status: corev1.ConditionTrue,
				LastTransitionTime: metav1.Now(),
			}}
			Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())
		}

		// putPods places every pod of both roles at one revision.
		putPods := func(revision string) {
			GinkgoHelper()
			for ordinal := range 3 {
				putPod(coordinatorSuffix, "coordinator", ordinal, revision)
			}
			for ordinal := range 2 {
				putPod(dataSuffix, "data", ordinal, revision)
			}
		}

		// declareRevision publishes the revision both StatefulSets' current pod
		// template hashes to, which is what makes the pods above outdated.
		declareRevision := func(revision string) {
			GinkgoHelper()
			for _, suffix := range []string{coordinatorSuffix, dataSuffix} {
				sts := &appsv1.StatefulSet{}
				get(resourceName+suffix, sts)
				sts.Status.UpdateRevision = revision
				Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())
			}
		}

		podExists := func(suffix string, ordinal int) bool {
			GinkgoHelper()
			pod := &corev1.Pod{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s%s-%d", resourceName, suffix, ordinal),
				Namespace: resourceNamespace,
			}, pod)
			if apierrors.IsNotFound(err) {
				return false
			}
			Expect(err).NotTo(HaveOccurred())
			return pod.DeletionTimestamp == nil
		}

		BeforeEach(func() {
			resource := &memgraphcomv1alpha1.MemgraphCluster{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: resourceNamespace},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			fake.setInstances(convergedCluster())
			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			reconcileCluster(resourceName)
		})

		AfterEach(func() {
			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			Expect(k8sClient.Delete(ctx, cluster)).To(Succeed())
			deleteOwned(resourceName)
			Expect(k8sClient.DeleteAllOf(ctx, &corev1.Pod{},
				client.InNamespace(resourceNamespace),
				client.MatchingLabels{"app.kubernetes.io/instance": resourceName},
				client.GracePeriodSeconds(0),
			)).To(Succeed())
		})

		It("should report Updated once every pod runs the declared template", func() {
			putPods(newRevision)
			declareRevision(newRevision)
			reconcileCluster(resourceName)

			updated := condition(memgraphcomv1alpha1.ConditionUpdated)
			Expect(updated).NotTo(BeNil())
			Expect(updated.Status).To(Equal(metav1.ConditionTrue))
			Expect(updated.Reason).To(Equal(memgraphcomv1alpha1.ReasonAllPodsUpdated))
		})

		// The whole order in one spec: replicas before MAIN, data plane before
		// coordinators, Raft leader last, one pod at a time throughout.
		It("should restart data pods before coordinators, MAIN and the leader last", func() {
			putPods(oldRevision)
			declareRevision(newRevision)

			// instance_0 is MAIN, so the replica on ordinal 1 goes first.
			reconcileCluster(resourceName)
			Expect(podExists(dataSuffix, 1)).To(BeFalse(), "the non-MAIN data pod is restarted first")
			Expect(podExists(dataSuffix, 0)).To(BeTrue(), "the MAIN's pod is not touched yet")
			Expect(podExists(coordinatorSuffix, 2)).To(BeTrue(), "coordinators wait for the data plane")
			updated := condition(memgraphcomv1alpha1.ConditionUpdated)
			Expect(updated.Status).To(Equal(metav1.ConditionFalse))
			Expect(updated.Reason).To(Equal(memgraphcomv1alpha1.ReasonRollingRestartInProgress))

			// It comes back on the new revision, reachable and caught up.
			putPod(dataSuffix, "data", 1, newRevision)
			reconcileCluster(resourceName)
			Expect(podExists(dataSuffix, 0)).To(BeFalse(), "the MAIN's pod is restarted last of its role")
			Expect(podExists(coordinatorSuffix, 2)).To(BeTrue())

			// The coordinators fail over to instance_1, and the old MAIN returns as a
			// replica — which is what the operator observes rather than arranges.
			fake.setInstances([]memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleReplica),
				observedDataInstance(1, memgraph.RoleMain),
			})
			putPod(dataSuffix, "data", 0, newRevision)

			// Data done: coordinator_1 leads on ordinal 0, so ordinal 2 goes first.
			reconcileCluster(resourceName)
			Expect(podExists(coordinatorSuffix, 2)).To(BeFalse())
			Expect(podExists(coordinatorSuffix, 0)).To(BeTrue(), "the Raft leader's pod is last")

			putPod(coordinatorSuffix, "coordinator", 2, newRevision)
			reconcileCluster(resourceName)
			Expect(podExists(coordinatorSuffix, 1)).To(BeFalse())
			Expect(podExists(coordinatorSuffix, 0)).To(BeTrue())

			putPod(coordinatorSuffix, "coordinator", 1, newRevision)
			reconcileCluster(resourceName)
			Expect(podExists(coordinatorSuffix, 0)).To(BeFalse(), "the leader goes once nothing else is left")

			putPod(coordinatorSuffix, "coordinator", 0, newRevision)
			reconcileCluster(resourceName)
			Expect(condition(memgraphcomv1alpha1.ConditionUpdated).Status).To(Equal(metav1.ConditionTrue))
			Expect(fake.executedCommands()).To(BeEmpty(),
				"a rolling restart issues no registration commands at all")
		})

		It("should not restart the MAIN while no replica is caught up", func() {
			putPods(oldRevision)
			putPod(dataSuffix, "data", 1, newRevision)
			declareRevision(newRevision)
			fake.setBehind("instance_1", 7)

			reconcileCluster(resourceName)

			Expect(podExists(dataSuffix, 0)).To(BeTrue(), "the MAIN keeps serving; the roll waits")
			updated := condition(memgraphcomv1alpha1.ConditionUpdated)
			Expect(updated.Status).To(Equal(metav1.ConditionFalse))
			Expect(updated.Reason).To(Equal(memgraphcomv1alpha1.ReasonNoCaughtUpSurvivor))
		})

		// Registration convergence comes first: a cluster missing a registration is
		// not the one the spec describes, so it is no moment to start deleting pods.
		It("should not restart any pod while a registration is pending", func() {
			putPods(oldRevision)
			declareRevision(newRevision)
			fake.setInstances([]memgraph.Instance{
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleMain),
			})

			reconcileCluster(resourceName)

			Expect(podExists(dataSuffix, 1)).To(BeTrue())
			Expect(podExists(coordinatorSuffix, 2)).To(BeTrue())
			Expect(fake.executedCommands()).To(ContainElement(ContainSubstring("REGISTER INSTANCE instance_1")))
		})
	})
})
