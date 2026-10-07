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
	"maps"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
	"github.com/memgraph/kubernetes-operator/internal/memgraph"
	"github.com/memgraph/kubernetes-operator/internal/resources"
)

// dataHostnamePattern is the per-instance external-dns hostname the exposure
// specs ask for, with the placeholder the operator substitutes.
const dataHostnamePattern = "data-{ordinal}.memgraph.example.com"

// instanceLabel is the identity label every object of a cluster carries.
const (
	instanceLabel = "app.kubernetes.io/instance"
	nameLabel     = "app.kubernetes.io/name"
)

// The flag names and values the flags specs share, so each is spelled once.
const (
	logLevelFlag       = "log-level"
	logLevelUnderscore = "log_level"
	snapshotOnExitFlag = "storage-snapshot-on-exit"

	debugLevel memgraphcomv1alpha1.FlagValue = "DEBUG"
	flagOn     memgraphcomv1alpha1.FlagValue = "true"
	flagOff    memgraphcomv1alpha1.FlagValue = "false"

	// The coordinator settings the specs and the fake share.
	downTimeoutSetting = "instance_down_timeout_sec"
	globalReadOnly     = "global_read_only"
	readsOnMainSetting = "enabled_reads_on_main"
	componentLabel     = "app.kubernetes.io/component"
	managedByLabel     = "app.kubernetes.io/managed-by"
	futureSetting      = "some_future_setting"
	settingOn          = flagOn
	settingOff         = flagOff
)

// memgraphContainerName is the name of the database container in every pod,
// which the sidecar specs assert their container lands beside.
const memgraphContainerName = "memgraph"

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
			Client:            k8sClient,
			Scheme:            k8sClient.Scheme(),
			APIReader:         k8sClient,
			Memgraph:          fake,
			GatewayAPI:        true,
			ServiceMonitorAPI: true,
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
				Expect(container.Image).To(Equal(memgraphcomv1alpha1.DefaultImageReference))
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

		It("should back both roles with a retained lib claim", func() {
			reconcileCluster(resourceName)

			for _, suffix := range []string{coordinatorSuffix, dataSuffix} {
				sts := &appsv1.StatefulSet{}
				get(resourceName+suffix, sts)

				Expect(sts.Spec.VolumeClaimTemplates).To(HaveLen(1), "the log files live on the lib claim")
				claim := libClaim(sts)
				Expect(claim.AccessModes).To(ConsistOf(corev1.ReadWriteOnce))
				Expect(claim.Resources.Requests.Storage()).To(HaveValue(Equal(resource.MustParse("10Gi"))))
				Expect(claim.StorageClassName).To(BeNil(), "the claim must fall back to the cluster's default StorageClass")

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
			return fmt.Sprintf("%s-coordinator-%d.%s-coordinator.%s.svc.cluster.local:%d",
				resourceName, ordinal, resourceName, resourceNamespace, memgraphcomv1alpha1.BoltPort)
		}

		// observedCoordinator reports the coordinator with the given zero-based
		// Raft ID, which equals its pod ordinal.
		observedCoordinator := func(id int, role string) memgraph.Instance {
			return memgraph.Instance{
				Name:       fmt.Sprintf("coordinator_%d", id),
				BoltServer: coordinatorAddress(id),
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
				leader + ": ADD COORDINATOR 0",
				leader + ": ADD COORDINATOR 1",
				leader + ": ADD COORDINATOR 2",
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
				observedCoordinator(0, memgraph.RoleLeader),
				observedCoordinator(1, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleMain),
			})

			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			reconcileCluster(resourceName)

			leader := coordinatorAddress(0)
			Expect(fake.executedCommands()).To(Equal([]string{
				leader + ": ADD COORDINATOR 2",
				leader + ": REGISTER INSTANCE instance_1",
			}))
		})

		It("should execute registration on the leader a follower reports", func() {
			fake.setInstances([]memgraph.Instance{
				observedCoordinator(0, memgraph.RoleFollower),
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
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
				observedCoordinator(0, memgraph.RoleFollower),
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleMain),
			})
			// coordinator_0 lost the leader and still remembers a cluster that
			// has both data instances registered.
			fake.setStaleView(coordinatorAddress(0), []memgraph.Instance{
				observedCoordinator(0, memgraph.RoleFollower),
				observedCoordinator(1, memgraph.RoleFollower),
				observedCoordinator(2, memgraph.RoleFollower),
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
				observedCoordinator(0, memgraph.RoleFollower),
				observedCoordinator(1, memgraph.RoleFollower),
				observedCoordinator(2, memgraph.RoleFollower),
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
				observedCoordinator(0, memgraph.RoleFollower),
				observedCoordinator(1, memgraph.RoleFollower),
				observedCoordinator(2, memgraph.RoleFollower),
				observedCoordinator(3, memgraph.RoleLeader),
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
				observedCoordinator(0, memgraph.RoleLeader),
				observedCoordinator(1, memgraph.RoleFollower),
				observedCoordinator(2, memgraph.RoleFollower),
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
				observedCoordinator(0, memgraph.RoleLeader),
				observedCoordinator(1, memgraph.RoleFollower),
				observedCoordinator(2, memgraph.RoleFollower),
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

			// coordinator_2 disappears from the Raft cluster view.
			fake.setInstances([]memgraph.Instance{
				observedCoordinator(0, memgraph.RoleLeader),
				observedCoordinator(1, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleMain),
				observedDataInstance(1, memgraph.RoleReplica),
			})

			reconcileCluster(resourceName)

			leader := coordinatorAddress(0)
			Expect(fake.executedCommands()).To(Equal([]string{
				leader + ": ADD COORDINATOR 2",
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
			return fmt.Sprintf("%s-coordinator-%d.%s-coordinator.%s.svc.cluster.local:%d",
				resourceName, ordinal, resourceName, resourceNamespace, memgraphcomv1alpha1.BoltPort)
		}

		// convergedWith is the fully registered 3-coordinator topology with the given
		// number of data instances, the one on mainOrdinal elected MAIN — so a spec
		// can put MAIN where it needs it before lowering a count.
		convergedWith := func(dataInstances, mainOrdinal int) []memgraph.Instance {
			instances := make([]memgraph.Instance, 0, 3+dataInstances)
			for id := range 3 {
				role := memgraph.RoleFollower
				if id == 0 {
					role = memgraph.RoleLeader
				}
				instances = append(instances, memgraph.Instance{
					Name: fmt.Sprintf("coordinator_%d", id), BoltServer: coordinatorAddress(id),
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
				leader + ": ADD COORDINATOR 3",
				leader + ": ADD COORDINATOR 4",
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
				leader + ": REMOVE COORDINATOR 3",
				leader + ": REMOVE COORDINATOR 4",
			}), "both retiring members leave the Raft cluster in one pass under a surviving leader")
			Expect(replicas(coordinatorSuffix)).To(Equal(int32(5)),
				"a pass with pending commands must never lower the replica count")
			converged := convergedCondition()
			Expect(converged.Status).To(Equal(metav1.ConditionFalse))
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonRetirementInProgress))
			Expect(converged.Message).To(ContainSubstring("coordinator_3"),
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
			fake.setLeader("coordinator_3")

			setCounts(3, 2)
			reconcileCluster(resourceName)

			retiringLeader := coordinatorAddress(3)
			Expect(sinceBootstrap(baseline)).To(Equal([]string{
				retiringLeader + ": REMOVE COORDINATOR 4",
				retiringLeader + ": YIELD LEADERSHIP",
			}), "the yield comes last, after the removal the planner could still order safely")
			converged := convergedCondition()
			Expect(converged.Status).To(Equal(metav1.ConditionFalse))
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonLeadershipTransferInProgress))
			Expect(converged.Message).To(ContainSubstring("coordinator_3"),
				"the condition must name the coordinator being moved off leadership")
			Expect(replicas(coordinatorSuffix)).To(Equal(int32(5)),
				"a pass with a pending yield must never lower the replica count")

			By("removing the former leader on the next pass, under whichever coordinator won")
			reconcileCluster(resourceName)
			Expect(sinceBootstrap(baseline)).To(Equal([]string{
				retiringLeader + ": REMOVE COORDINATOR 4",
				retiringLeader + ": YIELD LEADERSHIP",
				coordinatorAddress(0) + ": REMOVE COORDINATOR 3",
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
				leader + ": REMOVE COORDINATOR 3",
				leader + ": REMOVE COORDINATOR 4",
			}), "the surviving MAIN is left alone, and each role's removals are planned on their own")
			Expect(replicas(coordinatorSuffix)).To(Equal(int32(5)))
			Expect(replicas(dataSuffix)).To(Equal(int32(2)))
			Expect(convergedCondition().Message).To(SatisfyAll(
				ContainSubstring("coordinator_3"), ContainSubstring("instance_1")),
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

		// The gate has to key off the count the pass applied, not the spec.replicas
		// on the StatefulSet. The pass reads each StatefulSet once, before applying,
		// so after a scale-up that field is still the pre-apply value — and it agrees
		// with a status.readyReplicas from the same old snapshot, because the cluster
		// really was converged at the old size. Two stale numbers that agree report a
		// grown topology as ready, and registration then names a pod Kubernetes has
		// not been asked to create.
		//
		// The gate is called directly here, on the StatefulSets as they stand: one
		// left at 2 ready out of 2 is exactly what that pre-apply read looks like,
		// and the pass that intends 3 must not accept it.
		It("should gate readiness on the applied count, not the StatefulSet's own spec", func() {
			bootstrapped()
			Expect(replicas(dataSuffix)).To(Equal(int32(2)))

			coordinators, data := &appsv1.StatefulSet{}, &appsv1.StatefulSet{}
			get(resourceName+coordinatorSuffix, coordinators)
			get(resourceName+dataSuffix, data)
			held := replicaCounts{
				coordinators: roleReplicas{name: resourceName + coordinatorSuffix, declared: 3, applied: 3,
					statefulSet: coordinators},
				data: roleReplicas{name: resourceName + dataSuffix, declared: 2, applied: 2, statefulSet: data},
			}
			// A zero rolloutRoles is a cluster with no restart under way, which is what
			// keeps this about the count comparison alone: the gate only ever tolerates
			// an unready pod while a role actually has pods left to restart.
			Expect(workloadsReady(held, rolloutRoles{})).To(BeTrue(),
				"the cluster is ready at the size this pass applies")

			grown := held
			grown.data.declared, grown.data.applied = 3, 3
			Expect(workloadsReady(grown, rolloutRoles{})).To(BeFalse(),
				"a pass applying 3 must not read 2-ready-of-2 as ready, whatever spec.replicas still says")
		})

		It("should report the registered counts as observed, not as declared", func() {
			bootstrapped()
			Expect(status().DataInstances).To(Equal(int32(2)))

			// instance_1 loses its registration: the count reports what the
			// cluster has, which is what makes it worth watching during a scale.
			fake.setInstances([]memgraph.Instance{
				{
					Name: "coordinator_0", BoltServer: coordinatorAddress(0),
					Health: memgraph.HealthUp, Role: memgraph.RoleLeader,
				},
				{
					Name: "coordinator_1", BoltServer: coordinatorAddress(1),
					Health: memgraph.HealthUp, Role: memgraph.RoleFollower,
				},
				{Name: "instance_0", Health: memgraph.HealthUp, Role: memgraph.RoleMain},
			})
			reconcileCluster(resourceName)

			s := status()
			Expect(s.Coordinators).To(Equal(int32(2)), "coordinator_2 is no longer a member")
			Expect(s.DataInstances).To(Equal(int32(1)))
		})
	})

	Context("when reporting status and conditions", func() {
		const resourceName = "mgc-status"

		observedCoordinator := func(id int, role string) memgraph.Instance {
			return memgraph.Instance{
				Name: fmt.Sprintf("coordinator_%d", id),
				BoltServer: fmt.Sprintf("%s-coordinator-%d.%s-coordinator.%s.svc.cluster.local:%d",
					resourceName, id, resourceName, resourceNamespace, memgraphcomv1alpha1.BoltPort),
				Health: "up",
				Role:   role,
			}
		}
		observedDataInstance := func(i int, role string) memgraph.Instance {
			return memgraph.Instance{Name: fmt.Sprintf("instance_%d", i), Health: "up", Role: role}
		}
		convergedCluster := func() []memgraph.Instance {
			return []memgraph.Instance{
				observedCoordinator(0, memgraph.RoleLeader),
				observedCoordinator(1, memgraph.RoleFollower),
				observedCoordinator(2, memgraph.RoleFollower),
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
				observedCoordinator(0, memgraph.RoleFollower),
				observedCoordinator(1, memgraph.RoleFollower),
				observedCoordinator(2, memgraph.RoleFollower),
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
		// declared spec never lands. Every claim template field but the sizes is
		// pinned at admission, and the sizes reach a live StatefulSet only by
		// recreating it, so the trigger is a StatefulSet that predates the
		// operator with a claim of another access mode: Kubernetes forbids
		// changing a StatefulSet's volumeClaimTemplates, so the apply that would
		// bring it onto the declared spec is refused.
		It("should report the API server's rejection when applying a workload fails", func() {
			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			preexisting := resources.DataStatefulSet(cluster, resources.DeclaredDataInstances(cluster))
			preexisting.Spec.VolumeClaimTemplates[0].Spec.AccessModes =
				[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}
			Expect(k8sClient.Create(ctx, preexisting)).To(Succeed())

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: resourceName, Namespace: resourceNamespace},
			})
			Expect(err).To(HaveOccurred(), "the API server refuses to change a volume claim template")

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
				observedCoordinator(0, memgraph.RoleLeader),
				observedCoordinator(1, memgraph.RoleFollower),
				observedCoordinator(2, memgraph.RoleFollower),
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
				observedCoordinator(0, memgraph.RoleLeader),
				observedCoordinator(1, memgraph.RoleFollower),
				observedCoordinator(2, memgraph.RoleFollower),
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
				observedCoordinator(0, memgraph.RoleLeader),
				observedCoordinator(1, memgraph.RoleFollower),
				observedCoordinator(2, memgraph.RoleFollower),
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
				observedCoordinator(0, memgraph.RoleLeader),
				observedCoordinator(1, memgraph.RoleFollower),
				observedCoordinator(2, memgraph.RoleFollower),
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
				resourceName, id, resourceName, resourceNamespace)
			return memgraph.Instance{
				Name:       fmt.Sprintf("coordinator_%d", id),
				BoltServer: fmt.Sprintf("%s:%d", host, memgraphcomv1alpha1.BoltPort),
				CoordinatorServer: fmt.Sprintf("%s:%d", host,
					memgraphcomv1alpha1.CoordinatorPort),
				ManagementServer: fmt.Sprintf("%s:%d", host,
					memgraphcomv1alpha1.ManagementPort),
				Health: "up", Role: role,
			}
		}
		observedDataInstance := func(i int, role string) memgraph.Instance {
			return memgraph.Instance{Name: fmt.Sprintf("instance_%d", i), Health: "up", Role: role}
		}
		convergedCluster := func() []memgraph.Instance {
			return []memgraph.Instance{
				observedCoordinator(0, memgraph.RoleLeader),
				observedCoordinator(1, memgraph.RoleFollower),
				observedCoordinator(2, memgraph.RoleFollower),
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
						nameLabel:                       memgraphDbName,
						instanceLabel:                   resourceName,
						componentLabel:                  component,
						managedByLabel:                  resources.ManagedByValue,
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
				client.MatchingLabels{instanceLabel: resourceName},
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

		// The race the e2e suite caught: both StatefulSets are applied in one
		// pass, their statuses land one at a time, and for a moment the
		// coordinators show the new revision while the data StatefulSet still
		// shows the old one and has not observed its new template. Judged on that
		// view the data plane looks done, and a coordinator would go first.
		It("should not judge a role whose StatefulSet status lags its template", func() {
			putPods(oldRevision)
			// Only the coordinator StatefulSet has published the new revision; the
			// data StatefulSet's status still describes the previous template.
			sts := &appsv1.StatefulSet{}
			get(resourceName+coordinatorSuffix, sts)
			sts.Status.UpdateRevision = newRevision
			Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())
			get(resourceName+dataSuffix, sts)
			sts.Status.UpdateRevision = oldRevision
			sts.Status.ObservedGeneration = sts.Generation - 1
			Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())

			reconcileCluster(resourceName)

			for ordinal := range 3 {
				Expect(podExists(coordinatorSuffix, ordinal)).To(BeTrue(),
					"no coordinator goes while the data StatefulSet's status is behind")
			}
			updated := condition(memgraphcomv1alpha1.ConditionUpdated)
			Expect(updated.Status).To(Equal(metav1.ConditionFalse))
			Expect(updated.Message).To(ContainSubstring("data StatefulSet's status"))

			// The status catches up: the data plane is outdated after all, and the
			// roll starts where it should, with a data pod.
			get(resourceName+dataSuffix, sts)
			sts.Status.UpdateRevision = newRevision
			sts.Status.ObservedGeneration = sts.Generation
			Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())
			reconcileCluster(resourceName)

			Expect(podExists(dataSuffix, 1)).To(BeFalse(), "the non-MAIN data pod is restarted first")
			Expect(podExists(coordinatorSuffix, 2)).To(BeTrue())
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
				observedCoordinator(0, memgraph.RoleLeader),
				observedCoordinator(1, memgraph.RoleFollower),
				observedCoordinator(2, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleReplica),
				observedDataInstance(1, memgraph.RoleMain),
			})
			putPod(dataSuffix, "data", 0, newRevision)

			// Data done: coordinator_0 leads on ordinal 0, so ordinal 2 goes first.
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
				observedCoordinator(0, memgraph.RoleLeader),
				observedCoordinator(1, memgraph.RoleFollower),
				observedCoordinator(2, memgraph.RoleFollower),
				observedDataInstance(0, memgraph.RoleMain),
			})

			reconcileCluster(resourceName)

			Expect(podExists(dataSuffix, 1)).To(BeTrue())
			Expect(podExists(coordinatorSuffix, 2)).To(BeTrue())
			Expect(fake.executedCommands()).To(ContainElement(ContainSubstring("REGISTER INSTANCE instance_1")))
		})
	})

	Context("when a storage size grows on a live cluster", func() {
		const (
			resourceName = "mgc-resize"
			revision     = "mgc-resize-6c9f8b7d5"
			expandable   = "expandable"
			fixed        = "fixed-size"
			mainInstance = "instance_0"
			replica      = "instance_1"
			data         = "data"
			coordinator  = "coordinator"
		)

		observedCoordinator := func(id int, role string) memgraph.Instance {
			host := fmt.Sprintf("%s-coordinator-%d.%s-coordinator.%s.svc.cluster.local",
				resourceName, id, resourceName, resourceNamespace)
			return memgraph.Instance{
				Name:              fmt.Sprintf("coordinator_%d", id),
				BoltServer:        fmt.Sprintf("%s:%d", host, memgraphcomv1alpha1.BoltPort),
				CoordinatorServer: fmt.Sprintf("%s:%d", host, memgraphcomv1alpha1.CoordinatorPort),
				ManagementServer:  fmt.Sprintf("%s:%d", host, memgraphcomv1alpha1.ManagementPort),
				Health:            "up", Role: role,
			}
		}
		convergedCluster := func() []memgraph.Instance {
			return []memgraph.Instance{
				observedCoordinator(0, memgraph.RoleLeader),
				observedCoordinator(1, memgraph.RoleFollower),
				observedCoordinator(2, memgraph.RoleFollower),
				{Name: mainInstance, Health: "up", Role: memgraph.RoleMain},
				{Name: replica, Health: "up", Role: memgraph.RoleReplica},
			}
		}
		condition := func(condType string) *metav1.Condition {
			GinkgoHelper()
			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			return apimeta.FindStatusCondition(cluster.Status.Conditions, condType)
		}
		claimName := func(ordinal int) string {
			return fmt.Sprintf("lib-storage-%s%s-%d", resourceName, dataSuffix, ordinal)
		}
		getClaim := func(ordinal int) *corev1.PersistentVolumeClaim {
			GinkgoHelper()
			pvc := &corev1.PersistentVolumeClaim{}
			get(claimName(ordinal), pvc)
			return pvc
		}

		// putPod stands in for the StatefulSet controller envtest does not
		// run: one data pod at the role's revision, ready.
		putPod := func(suffix, component string, ordinal int) {
			GinkgoHelper()
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s%s-%d", resourceName, suffix, ordinal),
					Namespace: resourceNamespace,
					Labels: map[string]string{
						nameLabel:                       memgraphDbName,
						instanceLabel:                   resourceName,
						componentLabel:                  component,
						managedByLabel:                  resources.ManagedByValue,
						appsv1.StatefulSetRevisionLabel: revision,
					},
				},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: memgraphDbName, Image: memgraphDbName}}},
			}
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())
			pod.Status.Conditions = []corev1.PodCondition{{
				Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.Now(),
			}}
			Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())
		}
		podUID := func(suffix string, ordinal int) types.UID {
			GinkgoHelper()
			pod := &corev1.Pod{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name: fmt.Sprintf("%s%s-%d", resourceName, suffix, ordinal), Namespace: resourceNamespace,
			}, pod)
			if apierrors.IsNotFound(err) || err == nil && pod.DeletionTimestamp != nil {
				return ""
			}
			Expect(err).NotTo(HaveOccurred())
			return pod.UID
		}

		// putClaim stands in for the StatefulSet controller again: one bound
		// lib claim of a data pod, labelled with the StatefulSet's selector,
		// holding what it asks for.
		putClaim := func(ordinal int, class, size string) {
			GinkgoHelper()
			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: claimName(ordinal), Namespace: resourceNamespace,
					Labels: map[string]string{
						nameLabel: memgraphDbName, instanceLabel: resourceName, componentLabel: data,
					},
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					StorageClassName: ptr.To(class),
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, pvc)).To(Succeed())
			pvc.Status.Phase = corev1.ClaimBound
			pvc.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)}
			Expect(k8sClient.Status().Update(ctx, pvc)).To(Succeed())
		}
		// setClaimStatus stands in for the resizer and the kubelet.
		setClaimStatus := func(ordinal int, mutate func(*corev1.PersistentVolumeClaimStatus)) {
			GinkgoHelper()
			pvc := getClaim(ordinal)
			mutate(&pvc.Status)
			Expect(k8sClient.Status().Update(ctx, pvc)).To(Succeed())
		}
		grownTo := func(size string) func(*corev1.PersistentVolumeClaimStatus) {
			return func(status *corev1.PersistentVolumeClaimStatus) {
				status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)}
				status.Conditions = nil
			}
		}
		resizePendingSince := func(at time.Time) func(*corev1.PersistentVolumeClaimStatus) {
			return func(status *corev1.PersistentVolumeClaimStatus) {
				status.Conditions = []corev1.PersistentVolumeClaimCondition{{
					Type:               corev1.PersistentVolumeClaimFileSystemResizePending,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(at),
				}}
			}
		}

		// declareRevision publishes the revision both StatefulSets' pod
		// templates hash to, which every pod above carries.
		declareRevision := func() {
			GinkgoHelper()
			for _, suffix := range []string{coordinatorSuffix, dataSuffix} {
				sts := &appsv1.StatefulSet{}
				get(resourceName+suffix, sts)
				sts.Status.UpdateRevision = revision
				Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())
			}
		}
		converge := func() {
			GinkgoHelper()
			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			declareRevision()
			reconcileCluster(resourceName)
		}
		growLib := func(size string) {
			GinkgoHelper()
			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			cluster.Spec.Storage.Data.LibPVCSize = ptr.To(resource.MustParse(size))
			Expect(k8sClient.Update(ctx, cluster)).To(Succeed())
		}
		dataSet := func() *appsv1.StatefulSet {
			GinkgoHelper()
			sts := &appsv1.StatefulSet{}
			get(resourceName+dataSuffix, sts)
			return sts
		}
		// collectOrphaned stands in for the garbage collector envtest does not
		// run: it finishes an orphaning delete by dropping its finalizer.
		collectOrphaned := func() {
			GinkgoHelper()
			sts := dataSet()
			Expect(sts.DeletionTimestamp).NotTo(BeNil())
			sts.Finalizers = nil
			Expect(k8sClient.Update(ctx, sts)).To(Succeed())
			Eventually(func() bool {
				return apierrors.IsNotFound(k8sClient.Get(ctx,
					types.NamespacedName{Name: resourceName + dataSuffix, Namespace: resourceNamespace}, &appsv1.StatefulSet{}))
			}).Should(BeTrue())
		}

		createCluster := func(class string) {
			GinkgoHelper()
			resource := &memgraphcomv1alpha1.MemgraphCluster{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: resourceNamespace},
				Spec: memgraphcomv1alpha1.MemgraphClusterSpec{Storage: memgraphcomv1alpha1.StorageSpec{
					Data: memgraphcomv1alpha1.RoleStorageSpec{
						LibPVCSize:          ptr.To(resource.MustParse("1Gi")),
						LibStorageClassName: ptr.To(class),
					},
				}},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			fake.setInstances(convergedCluster())
			for ordinal := range 3 {
				putPod(coordinatorSuffix, coordinator, ordinal)
			}
			for ordinal := range 2 {
				putPod(dataSuffix, data, ordinal)
				putClaim(ordinal, class, "1Gi")
			}
			// A claim retained from an earlier scale-down, which nothing mounts.
			putClaim(3, class, "1Gi")
			converge()
			Expect(condition(memgraphcomv1alpha1.ConditionConverged).Status).To(Equal(metav1.ConditionTrue))
		}

		BeforeEach(func() {
			for name, allow := range map[string]bool{expandable: true, fixed: false} {
				class := &storagev1.StorageClass{
					ObjectMeta:           metav1.ObjectMeta{Name: name},
					Provisioner:          "example.com/csi",
					AllowVolumeExpansion: ptr.To(allow),
				}
				Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, class))).To(Succeed())
			}
		})

		AfterEach(func() {
			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			Expect(k8sClient.Delete(ctx, cluster)).To(Succeed())
			for _, suffix := range []string{coordinatorSuffix, dataSuffix} {
				sts := &appsv1.StatefulSet{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name: resourceName + suffix, Namespace: resourceNamespace,
				}, sts); err == nil && len(sts.Finalizers) > 0 {
					sts.Finalizers = nil
					Expect(k8sClient.Update(ctx, sts)).To(Succeed())
				}
			}
			deleteOwned(resourceName)
			for _, list := range []client.Object{&corev1.Pod{}, &corev1.PersistentVolumeClaim{}} {
				Expect(k8sClient.DeleteAllOf(ctx, list,
					client.InNamespace(resourceNamespace),
					client.MatchingLabels{instanceLabel: resourceName},
					client.GracePeriodSeconds(0),
				)).To(Succeed())
			}
			// Claims carry the protection finalizer the controller manager
			// would drop once no pod uses them; envtest runs none.
			var claims corev1.PersistentVolumeClaimList
			Expect(k8sClient.List(ctx, &claims, client.InNamespace(resourceNamespace),
				client.MatchingLabels{instanceLabel: resourceName})).To(Succeed())
			for i := range claims.Items {
				claims.Items[i].Finalizers = nil
				Expect(client.IgnoreNotFound(k8sClient.Update(ctx, &claims.Items[i]))).To(Succeed())
			}
		})

		It("should grow every claim, then recreate the StatefulSet around its pods", func() {
			createCluster(expandable)
			uids := map[int]types.UID{0: podUID(dataSuffix, 0), 1: podUID(dataSuffix, 1)}

			growLib("10Gi")
			reconcileCluster(resourceName)

			for _, ordinal := range []int{0, 1, 3} {
				Expect(getClaim(ordinal).Spec.Resources.Requests.Storage()).To(HaveValue(Equal(resource.MustParse("10Gi"))),
					"claim %d, the retained one included, asks for the grown size", ordinal)
			}
			sts := dataSet()
			Expect(sts.DeletionTimestamp).To(BeNil(), "the claims go first")
			Expect(libClaim(sts).Resources.Requests[corev1.ResourceStorage]).To(Equal(resource.MustParse("1Gi")),
				"the apply restates the live claim template rather than being refused")
			converged := condition(memgraphcomv1alpha1.ConditionConverged)
			Expect(converged.Status).To(Equal(metav1.ConditionFalse))
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonVolumeExpansionInProgress))
			Expect(converged.Message).To(ContainSubstring(claimName(0)))

			// Every claim asks for the size: the StatefulSet is deleted with
			// its dependents orphaned, and the pass ends there.
			reconcileCluster(resourceName)
			sts = dataSet()
			Expect(sts.DeletionTimestamp).NotTo(BeNil())
			Expect(sts.Finalizers).To(ContainElement(metav1.FinalizerOrphanDependents))
			converged = condition(memgraphcomv1alpha1.ConditionConverged)
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonVolumeExpansionInProgress))
			Expect(converged.Message).To(ContainSubstring("Recreating StatefulSet " + resourceName + dataSuffix))

			// Nothing is applied while it is still being deleted.
			reconcileCluster(resourceName)
			Expect(condition(memgraphcomv1alpha1.ConditionConverged).Message).To(ContainSubstring("Waiting for StatefulSet"))

			collectOrphaned()
			reconcileCluster(resourceName)
			sts = dataSet()
			Expect(libClaim(sts).Resources.Requests[corev1.ResourceStorage]).To(Equal(resource.MustParse("10Gi")),
				"the recreated StatefulSet carries the grown claim template")
			Expect(sts.Spec.Replicas).To(HaveValue(Equal(int32(2))))
			for ordinal, uid := range uids {
				Expect(podUID(dataSuffix, ordinal)).To(Equal(uid), "pod %d is not restarted", ordinal)
			}

			// Converged waits for the mounted claims to hold the size, not for
			// the retained one, whose filesystem grows only at its next mount.
			markWorkloadsReady(resourceName)
			declareRevision()
			setClaimStatus(3, resizePendingSince(time.Now()))
			reconcileCluster(resourceName)
			converged = condition(memgraphcomv1alpha1.ConditionConverged)
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonVolumeExpansionInProgress))
			Expect(converged.Message).To(ContainSubstring(claimName(1)))
			Expect(converged.Message).NotTo(ContainSubstring(claimName(3)))

			setClaimStatus(0, grownTo("10Gi"))
			setClaimStatus(1, grownTo("10Gi"))
			reconcileCluster(resourceName)
			Expect(condition(memgraphcomv1alpha1.ConditionConverged).Status).To(Equal(metav1.ConditionTrue))
			Expect(condition(memgraphcomv1alpha1.ConditionUpdated).Status).To(Equal(metav1.ConditionTrue),
				"a driver growing filesystems online restarts nothing")
			for ordinal, uid := range uids {
				Expect(podUID(dataSuffix, ordinal)).To(Equal(uid), "pod %d is not restarted", ordinal)
			}
		})

		// While the StatefulSet is gone its replica count is read off the pods
		// it left: recreating it at a count lowered in the meantime would shed
		// pods whose members never left the cluster.
		It("should recreate the StatefulSet at the count its pods run, not a lowered one", func() {
			createCluster(expandable)
			growLib("10Gi")
			reconcileCluster(resourceName)
			reconcileCluster(resourceName)
			Expect(dataSet().DeletionTimestamp).NotTo(BeNil())

			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			cluster.Spec.DataInstances = ptr.To(int32(1))
			Expect(k8sClient.Update(ctx, cluster)).To(Succeed())
			collectOrphaned()
			reconcileCluster(resourceName)

			Expect(dataSet().Spec.Replicas).To(HaveValue(Equal(int32(2))),
				"instance_1 is shed only by the retirement, once it has left the cluster")
			Expect(podUID(dataSuffix, 1)).NotTo(BeEmpty())
		})

		It("should report a resize the StorageClass does not allow and leave the StatefulSet alone", func() {
			createCluster(fixed)
			growLib("10Gi")
			reconcileCluster(resourceName)
			reconcileCluster(resourceName)

			Expect(dataSet().DeletionTimestamp).To(BeNil(), "nothing is recreated while a claim refuses the size")
			Expect(getClaim(0).Spec.Resources.Requests.Storage()).To(HaveValue(Equal(resource.MustParse("1Gi"))))
			converged := condition(memgraphcomv1alpha1.ConditionConverged)
			Expect(converged.Status).To(Equal(metav1.ConditionFalse))
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonVolumeExpansionRefused))
			Expect(converged.Message).To(ContainSubstring(claimName(0)))
			Expect(converged.Message).To(ContainSubstring("storageclass that provisions the pvc must support resize"))
		})

		It("should report a resize the storage provider gave up on", func() {
			createCluster(expandable)
			growLib("10Gi")
			reconcileCluster(resourceName)
			reconcileCluster(resourceName)
			collectOrphaned()
			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			declareRevision()
			setClaimStatus(0, func(status *corev1.PersistentVolumeClaimStatus) {
				status.AllocatedResourceStatuses = map[corev1.ResourceName]corev1.ClaimResourceStatus{
					corev1.ResourceStorage: corev1.PersistentVolumeClaimControllerResizeInfeasible,
				}
				status.Conditions = []corev1.PersistentVolumeClaimCondition{{
					Type: corev1.PersistentVolumeClaimControllerResizeError, Status: corev1.ConditionTrue,
					Message: "disk quota exceeded",
				}}
			})
			reconcileCluster(resourceName)

			converged := condition(memgraphcomv1alpha1.ConditionConverged)
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonVolumeExpansionFailed))
			Expect(converged.Message).To(ContainSubstring(fmt.Sprintf(
				"%s (pod %s%s-0): ControllerResizeInfeasible: disk quota exceeded", claimName(0), resourceName, dataSuffix)))
		})

		// A claim whose filesystem is still to grow under a running pod is
		// what every driver reports until the kubelet's next sync, so it is
		// waited on and named, never restarted for.
		It("should restart nothing while a filesystem waits to grow, naming the pod it waits on", func() {
			createCluster(expandable)
			growLib("10Gi")
			reconcileCluster(resourceName)
			reconcileCluster(resourceName)
			collectOrphaned()
			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			declareRevision()

			setClaimStatus(0, resizePendingSince(time.Now()))
			setClaimStatus(1, resizePendingSince(time.Now()))
			uids := map[int]types.UID{0: podUID(dataSuffix, 0), 1: podUID(dataSuffix, 1)}
			reconcileCluster(resourceName)

			for ordinal, uid := range uids {
				Expect(podUID(dataSuffix, ordinal)).To(Equal(uid), "pod %d is not restarted", ordinal)
			}
			Expect(condition(memgraphcomv1alpha1.ConditionUpdated).Status).To(Equal(metav1.ConditionTrue))
			converged := condition(memgraphcomv1alpha1.ConditionConverged)
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonVolumeExpansionInProgress))
			Expect(converged.Message).To(ContainSubstring(
				fmt.Sprintf("%s (pod %s%s-1)", claimName(1), resourceName, dataSuffix)))
			Expect(converged.Message).To(ContainSubstring("about a minute"))
		})

		// What AKS reports on a VM size that cannot change an attached disk:
		// the resizer retries forever, so it is still in progress, but the
		// message carries the cloud's reason, which is the one thing to act on.
		It("should carry the storage driver's resize error while it retries", func() {
			createCluster(expandable)
			growLib("10Gi")
			reconcileCluster(resourceName)
			reconcileCluster(resourceName)
			collectOrphaned()
			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			declareRevision()
			setClaimStatus(0, func(status *corev1.PersistentVolumeClaimStatus) {
				status.AllocatedResourceStatuses = map[corev1.ResourceName]corev1.ClaimResourceStatus{
					corev1.ResourceStorage: corev1.PersistentVolumeClaimControllerResizeInProgress,
				}
				status.Conditions = []corev1.PersistentVolumeClaimCondition{{
					Type: corev1.PersistentVolumeClaimControllerResizeError, Status: corev1.ConditionTrue,
					// The cloud's reason comes last, after an HTTP response
					// longer than the condition message has room for.
					Message: "failed to resize disk with error(PATCH " + strings.Repeat("/subscriptions/x", 80) +
						"\nRESPONSE 409: 409 Conflict\nERROR CODE: OperationNotAllowed\n" +
						"Change in disk property of VM of size 'Standard_A2_v2' is not supported.",
				}}
			})
			reconcileCluster(resourceName)

			converged := condition(memgraphcomv1alpha1.ConditionConverged)
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonVolumeExpansionInProgress))
			Expect(len(converged.Message)).To(BeNumerically("<=", 1024))
			Expect(converged.Message).To(ContainSubstring("The storage driver reports ..."))
			Expect(converged.Message).To(HaveSuffix("RESPONSE 409: 409 Conflict ERROR CODE: OperationNotAllowed " +
				"Change in disk property of VM of size 'Standard_A2_v2' is not supported.; see docs/storage-resize.md"))
		})
	})

	Context("when the spec carries Memgraph flags", func() {
		const (
			resourceName   = "mgc-flags"
			firstInstance  = "instance_0"
			secondInstance = "instance_1"
		)

		cluster := &memgraphcomv1alpha1.MemgraphCluster{}

		podAddress := func(suffix string, ordinal int) string {
			return fmt.Sprintf("%s%s-%d.%s%s.%s.svc.cluster.local:%d",
				resourceName, suffix, ordinal, resourceName, suffix, resourceNamespace, memgraphcomv1alpha1.BoltPort)
		}
		observedCoordinator := func(id int, role string) memgraph.Instance {
			host, _, _ := strings.Cut(podAddress(coordinatorSuffix, id), ":")
			return memgraph.Instance{
				Name:              fmt.Sprintf("coordinator_%d", id),
				BoltServer:        fmt.Sprintf("%s:%d", host, memgraphcomv1alpha1.BoltPort),
				CoordinatorServer: fmt.Sprintf("%s:%d", host, memgraphcomv1alpha1.CoordinatorPort),
				ManagementServer:  fmt.Sprintf("%s:%d", host, memgraphcomv1alpha1.ManagementPort),
				Health:            "up", Role: role,
			}
		}
		convergedCluster := func() []memgraph.Instance {
			return []memgraph.Instance{
				observedCoordinator(0, memgraph.RoleLeader),
				observedCoordinator(1, memgraph.RoleFollower),
				observedCoordinator(2, memgraph.RoleFollower),
				{Name: firstInstance, Health: "up", Role: memgraph.RoleMain},
				{Name: secondInstance, Health: "up", Role: memgraph.RoleReplica},
			}
		}

		// putPod stands in for the StatefulSet controller and the kubelet envtest
		// does not run: one role pod, ready or not. The settings pass dials only
		// ready pods, so a pod has to exist and be ready to be reached at all.
		putPod := func(suffix, component string, ordinal int, ready bool) {
			GinkgoHelper()
			name := fmt.Sprintf("%s%s-%d", resourceName, suffix, ordinal)
			existing := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: resourceNamespace}}
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, existing))).To(Succeed())
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: resourceNamespace,
					Labels: map[string]string{
						nameLabel:      memgraphDbName,
						instanceLabel:  resourceName,
						componentLabel: component,
						managedByLabel: resources.ManagedByValue,
					},
				},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: memgraphDbName, Image: memgraphDbName}}},
			}
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())
			status := corev1.ConditionFalse
			if ready {
				status = corev1.ConditionTrue
			}
			pod.Status.Conditions = []corev1.PodCondition{{
				Type: corev1.PodReady, Status: status, LastTransitionTime: metav1.Now(),
			}}
			Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())
		}
		putPods := func() {
			GinkgoHelper()
			for ordinal := range 3 {
				putPod(coordinatorSuffix, "coordinator", ordinal, true)
			}
			for ordinal := range 2 {
				putPod(dataSuffix, "data", ordinal, true)
			}
		}

		setFlags := func(flags memgraphcomv1alpha1.FlagsSpec) {
			GinkgoHelper()
			get(resourceName, cluster)
			cluster.Spec.Flags = flags
			Expect(k8sClient.Update(ctx, cluster)).To(Succeed())
		}
		condition := func(condType string) *metav1.Condition {
			GinkgoHelper()
			get(resourceName, cluster)
			return apimeta.FindStatusCondition(cluster.Status.Conditions, condType)
		}
		flagsAnnotation := func(suffix string) string {
			GinkgoHelper()
			sts := &appsv1.StatefulSet{}
			get(resourceName+suffix, sts)
			return sts.Spec.Template.Annotations[resources.FlagsAnnotation]
		}
		setCommands := func() []string {
			var sets []string
			for _, command := range fake.executedCommands() {
				if strings.Contains(command, "SET DATABASE SETTING") {
					sets = append(sets, command)
				}
			}
			return sets
		}

		BeforeEach(func() {
			resource := &memgraphcomv1alpha1.MemgraphCluster{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: resourceNamespace},
				Spec: memgraphcomv1alpha1.MemgraphClusterSpec{
					Flags: memgraphcomv1alpha1.FlagsSpec{
						Data: map[string]memgraphcomv1alpha1.FlagValue{
							logLevelFlag:       debugLevel,
							snapshotOnExitFlag: flagOff,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			get(resourceName, cluster)

			fake.setInstances(convergedCluster())
			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			putPods()
		})

		AfterEach(func() {
			get(resourceName, cluster)
			Expect(k8sClient.Delete(ctx, cluster)).To(Succeed())
			deleteOwned(resourceName)
			for _, suffix := range []string{coordinatorSuffix, dataSuffix} {
				cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
					Name: resourceName + suffix + "-flags", Namespace: resourceNamespace,
				}}
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, cm))).To(Succeed())
			}
			Expect(k8sClient.DeleteAllOf(ctx, &corev1.Pod{},
				client.InNamespace(resourceNamespace),
				client.MatchingLabels{instanceLabel: resourceName},
				client.GracePeriodSeconds(0),
			)).To(Succeed())
		})

		It("should write each role's flag file into an owned ConfigMap the pods mount", func() {
			data := &corev1.ConfigMap{}
			get(resourceName+dataSuffix+"-flags", data)
			expectControlledBy(data, cluster)
			Expect(data.Data[resources.FlagFileKey]).To(Equal(
				"--also_log_to_stderr=true\n--log_level=DEBUG\n--log_retention_days=35\n--storage_snapshot_on_exit=false\n"))

			coordinators := &corev1.ConfigMap{}
			get(resourceName+coordinatorSuffix+"-flags", coordinators)
			Expect(coordinators.Data[resources.FlagFileKey]).To(Equal(
				"--also_log_to_stderr=true\n--log_level=INFO\n--log_retention_days=35\n"),
				"the data flags are not the coordinators'")

			sts := &appsv1.StatefulSet{}
			get(resourceName+dataSuffix, sts)
			container := sts.Spec.Template.Spec.Containers[0]
			Expect(container.Args[0]).To(Equal("--flag-file=/etc/memgraph-flags/memgraph.flags"))
			Expect(container.Args).NotTo(ContainElement(ContainSubstring("snapshot")),
				"spec.flags must not reach the command line")
			Expect(sts.Spec.Template.Spec.Volumes).To(ContainElement(HaveField("ConfigMap.Name", data.Name)))
		})

		It("should apply a run-time flag to every ready pod of the role and roll nothing", func() {
			hashBefore := flagsAnnotation(dataSuffix)
			reconcileCluster(resourceName)

			for ordinal := range 2 {
				Expect(fake.settingsOf(podAddress(dataSuffix, ordinal))).To(HaveKeyWithValue("log.level", string(debugLevel)),
					"data pod %d must have been SET", ordinal)
			}
			for ordinal := range 3 {
				Expect(fake.settingsOf(podAddress(coordinatorSuffix, ordinal))).To(HaveKeyWithValue("log.level", "INFO"),
					"the coordinators keep their default, the flag is the data instances'")
			}
			Expect(setCommands()).To(ConsistOf(
				podAddress(dataSuffix, 0)+`: SET DATABASE SETTING "log.level" TO "DEBUG"`,
				podAddress(dataSuffix, 1)+`: SET DATABASE SETTING "log.level" TO "DEBUG"`,
			), "exactly the run-time flag that differs, once per instance; the startup-only one is never SET")
			Expect(flagsAnnotation(dataSuffix)).To(Equal(hashBefore), "a run-time flag is not part of the pod template")
			Expect(condition(memgraphcomv1alpha1.ConditionConverged).Status).To(Equal(metav1.ConditionTrue))
			Expect(condition(memgraphcomv1alpha1.ConditionUpdated).Status).To(Equal(metav1.ConditionTrue))

			By("issuing nothing on the next pass, because every instance is in line")
			reconcileCluster(resourceName)
			Expect(setCommands()).To(HaveLen(2))

			By("following a further change to the same flag without touching the template")
			setFlags(memgraphcomv1alpha1.FlagsSpec{Data: map[string]memgraphcomv1alpha1.FlagValue{
				logLevelFlag: "WARNING", snapshotOnExitFlag: flagOff,
			}})
			reconcileCluster(resourceName)
			Expect(fake.settingsOf(podAddress(dataSuffix, 1))).To(HaveKeyWithValue("log.level", "WARNING"))
			Expect(flagsAnnotation(dataSuffix)).To(Equal(hashBefore))
		})

		It("should change the pod template, and so roll, for a startup-only flag", func() {
			hashBefore := flagsAnnotation(dataSuffix)
			coordinatorsBefore := flagsAnnotation(coordinatorSuffix)

			setFlags(memgraphcomv1alpha1.FlagsSpec{Data: map[string]memgraphcomv1alpha1.FlagValue{
				logLevelFlag: debugLevel, snapshotOnExitFlag: flagOn,
			}})
			reconcileCluster(resourceName)

			Expect(flagsAnnotation(dataSuffix)).NotTo(Equal(hashBefore), "a startup-only flag change must change the template")
			Expect(flagsAnnotation(coordinatorSuffix)).To(Equal(coordinatorsBefore), "the coordinators' flags did not change")
			Expect(setCommands()).NotTo(ContainElement(ContainSubstring("snapshot")),
				"a startup-only flag is never SET")
		})

		It("should skip a pod that is not ready and report a ready pod that does not answer", func() {
			putPod(dataSuffix, "data", 1, false)
			fake.setUnreachable(podAddress(coordinatorSuffix, 2), true)
			setFlags(memgraphcomv1alpha1.FlagsSpec{
				Coordinators: map[string]memgraphcomv1alpha1.FlagValue{logLevelFlag: "WARNING"},
				Data:         map[string]memgraphcomv1alpha1.FlagValue{logLevelFlag: debugLevel, snapshotOnExitFlag: flagOff},
			})

			result := reconcileCluster(resourceName)

			Expect(fake.settingsOf(podAddress(dataSuffix, 0))).To(HaveKeyWithValue("log.level", string(debugLevel)))
			Expect(fake.settingsOf(podAddress(dataSuffix, 1))).To(HaveKeyWithValue("log.level", "INFO"),
				"an unready pod is not dialed")
			Expect(fake.settingsOf(podAddress(coordinatorSuffix, 0))).To(HaveKeyWithValue("log.level", "WARNING"))
			converged := condition(memgraphcomv1alpha1.ConditionConverged)
			Expect(converged.Status).To(Equal(metav1.ConditionFalse))
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonSettingsPending))
			Expect(converged.Message).To(ContainSubstring(resourceName + coordinatorSuffix + "-2"))
			Expect(converged.Message).NotTo(ContainSubstring(resourceName+dataSuffix+"-1"),
				"an unready pod is the roll's or the kubelet's business, not a setting owed")
			Expect(result.RequeueAfter).To(Equal(requeueWhilePending))

			By("catching the pod up once it answers")
			fake.setUnreachable(podAddress(coordinatorSuffix, 2), false)
			reconcileCluster(resourceName)
			Expect(fake.settingsOf(podAddress(coordinatorSuffix, 2))).To(HaveKeyWithValue("log.level", "WARNING"))
			Expect(condition(memgraphcomv1alpha1.ConditionConverged).Status).To(Equal(metav1.ConditionTrue))
		})

		It("should report a SET the instance rejects with Memgraph's error and keep retrying it", func() {
			setFlags(memgraphcomv1alpha1.FlagsSpec{Data: map[string]memgraphcomv1alpha1.FlagValue{
				logLevelFlag: debugLevel, "also-log-to-stderr": "1",
			}})

			result := reconcileCluster(resourceName)

			converged := condition(memgraphcomv1alpha1.ConditionConverged)
			Expect(converged.Status).To(Equal(metav1.ConditionFalse))
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonSettingsRejected))
			Expect(converged.Message).To(ContainSubstring(resourceName + dataSuffix + "-0"))
			Expect(converged.Message).To(ContainSubstring(`SET DATABASE SETTING "log.to_stderr" TO "1"`))
			Expect(converged.Message).To(ContainSubstring("Boolean value supports only"))
			Expect(result.RequeueAfter).To(Equal(requeueWhilePending))
			Expect(condition(memgraphcomv1alpha1.ConditionReady).Status).To(Equal(metav1.ConditionTrue),
				"a refused setting does not make the cluster unready")

			By("clearing once the flag is fixed")
			setFlags(memgraphcomv1alpha1.FlagsSpec{Data: map[string]memgraphcomv1alpha1.FlagValue{
				logLevelFlag: debugLevel, "also-log-to-stderr": "false",
			}})
			reconcileCluster(resourceName)
			Expect(fake.settingsOf(podAddress(dataSuffix, 0))).To(HaveKeyWithValue("log.to_stderr", "false"))
			Expect(condition(memgraphcomv1alpha1.ConditionConverged).Status).To(Equal(metav1.ConditionTrue))
		})

		It("should leave a setting alone once its flag is removed, unless the operator has a default for it", func() {
			setFlags(memgraphcomv1alpha1.FlagsSpec{Data: map[string]memgraphcomv1alpha1.FlagValue{
				logLevelFlag: debugLevel, "query-execution-timeout-sec": "10",
			}})
			reconcileCluster(resourceName)
			Expect(fake.settingsOf(podAddress(dataSuffix, 0))).To(HaveKeyWithValue("query.timeout", "10"))

			setFlags(memgraphcomv1alpha1.FlagsSpec{})
			reconcileCluster(resourceName)

			settings := fake.settingsOf(podAddress(dataSuffix, 0))
			Expect(settings).To(HaveKeyWithValue("query.timeout", "10"),
				"a removed flag issues no SET: the setting reverts when the instance next restarts without it")
			Expect(settings).To(HaveKeyWithValue("log.level", "INFO"),
				"a removed flag the operator has a default for goes back to the default, which the flag file now says")
			Expect(condition(memgraphcomv1alpha1.ConditionConverged).Status).To(Equal(metav1.ConditionTrue))
		})
	})

	Context("when the license Secret changes", func() {
		const (
			resourceName   = "mgc-license"
			secretName     = resourceName + "-secret"
			renewedLicense = "license-renewed-in-the-secret"
			licenseSetting = `SET DATABASE SETTING "enterprise.license"`
		)

		cluster := &memgraphcomv1alpha1.MemgraphCluster{}

		podName := func(suffix string, ordinal int) string {
			return fmt.Sprintf("%s%s-%d", resourceName, suffix, ordinal)
		}
		podAddress := func(suffix string, ordinal int) string {
			return fmt.Sprintf("%s.%s%s.%s.svc.cluster.local:%d",
				podName(suffix, ordinal), resourceName, suffix, resourceNamespace, memgraphcomv1alpha1.BoltPort)
		}
		// every is each pod of both roles as the license pass dials it.
		every := func(visit func(suffix string, ordinal int)) {
			for ordinal := range 2 {
				visit(dataSuffix, ordinal)
			}
			for ordinal := range 3 {
				visit(coordinatorSuffix, ordinal)
			}
		}

		// putPod stands in for the StatefulSet controller and the kubelet
		// envtest does not run. The license pass dials only ready pods.
		putPod := func(suffix string, ordinal int, ready bool) {
			GinkgoHelper()
			existing := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName(suffix, ordinal), Namespace: resourceNamespace}}
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, existing))).To(Succeed())
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      podName(suffix, ordinal),
					Namespace: resourceNamespace,
					Labels: map[string]string{
						nameLabel:      memgraphDbName,
						instanceLabel:  resourceName,
						componentLabel: strings.TrimPrefix(suffix, "-"),
						managedByLabel: resources.ManagedByValue,
					},
				},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: memgraphDbName, Image: memgraphDbName}}},
			}
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())
			status := corev1.ConditionFalse
			if ready {
				status = corev1.ConditionTrue
			}
			pod.Status.Conditions = []corev1.PodCondition{{
				Type: corev1.PodReady, Status: status, LastTransitionTime: metav1.Now(),
			}}
			Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())
		}
		podUIDs := func() map[string]types.UID {
			GinkgoHelper()
			uids := map[string]types.UID{}
			every(func(suffix string, ordinal int) {
				pod := &corev1.Pod{}
				get(podName(suffix, ordinal), pod)
				uids[pod.Name] = pod.UID
			})
			return uids
		}

		putSecret := func(license, organization string) {
			GinkgoHelper()
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: resourceNamespace}}
			data := map[string][]byte{
				memgraphcomv1alpha1.DefaultLicenseSecretKey:      []byte(license),
				memgraphcomv1alpha1.DefaultOrganizationSecretKey: []byte(organization),
			}
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(secret), secret); apierrors.IsNotFound(err) {
				secret.Data = data
				Expect(k8sClient.Create(ctx, secret)).To(Succeed())
				return
			}
			secret.Data = data
			Expect(k8sClient.Update(ctx, secret)).To(Succeed())
		}
		licenseCondition := func() *metav1.Condition {
			GinkgoHelper()
			get(resourceName, cluster)
			condition := apimeta.FindStatusCondition(cluster.Status.Conditions, memgraphcomv1alpha1.ConditionLicenseApplied)
			Expect(condition).NotTo(BeNil(), "every pass reports LicenseApplied")
			return condition
		}
		licenseCommands := func() []string {
			var sets []string
			for _, command := range fake.executedCommands() {
				if strings.Contains(command, "SET DATABASE SETTING") {
					sets = append(sets, command)
				}
			}
			return sets
		}

		BeforeEach(func() {
			putSecret(startupLicense, startupOrganization)
			resource := &memgraphcomv1alpha1.MemgraphCluster{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: resourceNamespace},
				Spec: memgraphcomv1alpha1.MemgraphClusterSpec{
					Secrets: memgraphcomv1alpha1.SecretsSpec{Name: secretName},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			get(resourceName, cluster)

			reconcileCluster(resourceName)
			every(func(suffix string, ordinal int) { putPod(suffix, ordinal, true) })
		})

		AfterEach(func() {
			get(resourceName, cluster)
			Expect(k8sClient.Delete(ctx, cluster)).To(Succeed())
			deleteOwned(resourceName)
			Expect(k8sClient.DeleteAllOf(ctx, &corev1.Pod{},
				client.InNamespace(resourceNamespace),
				client.MatchingLabels{instanceLabel: resourceName},
				client.GracePeriodSeconds(0),
			)).To(Succeed())
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: resourceNamespace}}
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, secret))).To(Succeed())
		})

		It("should SET a renewed license on every ready pod of both roles and restart none", func() {
			reconcileCluster(resourceName)
			Expect(licenseCommands()).To(BeEmpty(), "pods already on the Secret's license are left alone")
			Expect(licenseCondition().Status).To(Equal(metav1.ConditionTrue))

			uids := podUIDs()
			putSecret(renewedLicense, startupOrganization)
			reconcileCluster(resourceName)

			every(func(suffix string, ordinal int) {
				Expect(fake.settingsOf(podAddress(suffix, ordinal))).To(
					HaveKeyWithValue("enterprise.license", renewedLicense), "%s must run the renewed license", podName(suffix, ordinal))
			})
			Expect(licenseCommands()).To(HaveLen(5), "one SET of the license per pod, and nothing for the unchanged organization")
			Expect(licenseCommands()).To(HaveEach(ContainSubstring(licenseSetting)))
			Expect(podUIDs()).To(Equal(uids), "a renewal restarts no pod")
			condition := licenseCondition()
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))
			Expect(condition.Reason).To(Equal(memgraphcomv1alpha1.ReasonLicenseMatchesSecret))

			By("issuing nothing on the next pass, because every pod is in line")
			reconcileCluster(resourceName)
			Expect(licenseCommands()).To(HaveLen(5))
		})

		It("should apply the license before the cluster is ready to register", func() {
			// The StatefulSets were never marked ready, so the pass stops at
			// the readiness gate: before any coordinator, leader or plan.
			putSecret(renewedLicense, startupOrganization)
			reconcileCluster(resourceName)

			every(func(suffix string, ordinal int) {
				Expect(fake.settingsOf(podAddress(suffix, ordinal))).To(HaveKeyWithValue("enterprise.license", renewedLicense))
			})
			Expect(licenseCondition().Status).To(Equal(metav1.ConditionTrue))
			get(resourceName, cluster)
			converged := apimeta.FindStatusCondition(cluster.Status.Conditions, memgraphcomv1alpha1.ConditionConverged)
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonWorkloadsNotReady),
				"the pass stopped short of registration and still applied the license")
		})

		It("should skip a pod that is not ready and report a ready pod that does not answer", func() {
			putPod(dataSuffix, 1, false)
			fake.setUnreachable(podAddress(coordinatorSuffix, 2), true)
			putSecret(renewedLicense, startupOrganization)

			reconcileCluster(resourceName)

			Expect(fake.settingsOf(podAddress(dataSuffix, 1))).To(HaveKeyWithValue("enterprise.license", startupLicense),
				"an unready pod is not dialed: it reads the Secret itself when it starts")
			condition := licenseCondition()
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal(memgraphcomv1alpha1.ReasonLicensePending))
			Expect(condition.Message).To(ContainSubstring(podName(coordinatorSuffix, 2)))
			Expect(condition.Message).NotTo(ContainSubstring(podName(dataSuffix, 1)))

			By("catching the pod up once it answers")
			fake.setUnreachable(podAddress(coordinatorSuffix, 2), false)
			reconcileCluster(resourceName)
			Expect(fake.settingsOf(podAddress(coordinatorSuffix, 2))).To(HaveKeyWithValue("enterprise.license", renewedLicense))
			Expect(licenseCondition().Status).To(Equal(metav1.ConditionTrue))
		})

		It("should report a license Memgraph rejects without putting the license in status", func() {
			fake.rejectCommand(licenseSetting, errors.New("Invalid license key: the license has already expired."))
			putSecret(renewedLicense, startupOrganization)

			reconcileCluster(resourceName)

			condition := licenseCondition()
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal(memgraphcomv1alpha1.ReasonLicenseRejected))
			Expect(condition.Message).To(ContainSubstring(podName(dataSuffix, 0)))
			Expect(condition.Message).To(ContainSubstring("the license has already expired"))
			Expect(condition.Message).To(ContainSubstring(`"enterprise.license"`))
			get(resourceName, cluster)
			Expect(fmt.Sprint(cluster.Status)).NotTo(ContainSubstring(renewedLicense), "the license is secret material")
		})

		It("should SET a changed organization together with the license", func() {
			const organization = "Another Organization"
			putSecret(renewedLicense, organization)

			reconcileCluster(resourceName)

			every(func(suffix string, ordinal int) {
				settings := fake.settingsOf(podAddress(suffix, ordinal))
				Expect(settings).To(HaveKeyWithValue("organization.name", organization))
				Expect(settings).To(HaveKeyWithValue("enterprise.license", renewedLicense))
			})
			Expect(licenseCommands()).To(HaveLen(10), "two SETs per pod, the organization and the license")
			Expect(licenseCondition().Status).To(Equal(metav1.ConditionTrue))
		})

		It("should report a missing Secret or key as pending", func() {
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: resourceNamespace}}
			Expect(k8sClient.Delete(ctx, secret)).To(Succeed())
			reconcileCluster(resourceName)
			condition := licenseCondition()
			Expect(condition.Reason).To(Equal(memgraphcomv1alpha1.ReasonLicensePending))
			Expect(condition.Message).To(ContainSubstring(secretName + " does not exist"))

			secret = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: resourceNamespace},
				Data:       map[string][]byte{memgraphcomv1alpha1.DefaultLicenseSecretKey: []byte(renewedLicense)},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
			reconcileCluster(resourceName)
			condition = licenseCondition()
			Expect(condition.Reason).To(Equal(memgraphcomv1alpha1.ReasonLicensePending))
			Expect(condition.Message).To(ContainSubstring("no key " + memgraphcomv1alpha1.DefaultOrganizationSecretKey))
			Expect(licenseCommands()).To(BeEmpty(), "half a pair is never applied")
		})

		It("should map a changed Secret to the clusters that read their license from it", func() {
			named := func(name string) client.Object {
				return &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: resourceNamespace}}
			}
			Expect(reconciler.clustersForSecret(ctx, named(secretName))).To(ConsistOf(reconcile.Request{
				NamespacedName: types.NamespacedName{Name: resourceName, Namespace: resourceNamespace},
			}))
			Expect(reconciler.clustersForSecret(ctx, named("some-other-secret"))).To(BeEmpty())
		})
	})

	Context("when the spec names an AWS credentials Secret", func() {
		const (
			resourceName    = "mgc-aws"
			secretName      = resourceName + "-credentials"
			accessKey       = "AKIA-started-with"
			secretKey       = "secret-started-with"
			rotatedKey      = "secret-rotated-in-the-secret"
			region          = "eu-west-1"
			minioEndpoint   = "http://minio:9000"
			secretKeySET    = `SET DATABASE SETTING "aws.secret_key"`
			endpointSET     = `SET DATABASE SETTING "aws.endpoint_url"`
			accessKeySET    = `SET DATABASE SETTING "aws.access_key"`
			firstInstance   = "instance_0"
			secondInstance  = "instance_1"
			dataComponent   = "data"
			coordsComponent = "coordinator"
		)

		cluster := &memgraphcomv1alpha1.MemgraphCluster{}

		podName := func(suffix string, ordinal int) string {
			return fmt.Sprintf("%s%s-%d", resourceName, suffix, ordinal)
		}
		podAddress := func(suffix string, ordinal int) string {
			return fmt.Sprintf("%s.%s%s.%s.svc.cluster.local:%d",
				podName(suffix, ordinal), resourceName, suffix, resourceNamespace, memgraphcomv1alpha1.BoltPort)
		}
		observedCoordinator := func(id int, role string) memgraph.Instance {
			host, _, _ := strings.Cut(podAddress(coordinatorSuffix, id), ":")
			return memgraph.Instance{
				Name:              fmt.Sprintf("coordinator_%d", id),
				BoltServer:        fmt.Sprintf("%s:%d", host, memgraphcomv1alpha1.BoltPort),
				CoordinatorServer: fmt.Sprintf("%s:%d", host, memgraphcomv1alpha1.CoordinatorPort),
				ManagementServer:  fmt.Sprintf("%s:%d", host, memgraphcomv1alpha1.ManagementPort),
				Health:            "up", Role: role,
			}
		}

		// putPod stands in for the StatefulSet controller and the kubelet
		// envtest does not run. The credentials pass dials only ready pods.
		putPod := func(suffix, component string, ordinal int, ready bool) {
			GinkgoHelper()
			existing := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName(suffix, ordinal), Namespace: resourceNamespace}}
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, existing))).To(Succeed())
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      podName(suffix, ordinal),
					Namespace: resourceNamespace,
					Labels: map[string]string{
						nameLabel:      memgraphDbName,
						instanceLabel:  resourceName,
						componentLabel: component,
						managedByLabel: resources.ManagedByValue,
					},
				},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: memgraphDbName, Image: memgraphDbName}}},
			}
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())
			status := corev1.ConditionFalse
			if ready {
				status = corev1.ConditionTrue
			}
			pod.Status.Conditions = []corev1.PodCondition{{
				Type: corev1.PodReady, Status: status, LastTransitionTime: metav1.Now(),
			}}
			Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())
		}

		putSecret := func(data map[string][]byte) {
			GinkgoHelper()
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: resourceNamespace}}
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(secret), secret); apierrors.IsNotFound(err) {
				secret.Data = data
				Expect(k8sClient.Create(ctx, secret)).To(Succeed())
				return
			}
			secret.Data = data
			Expect(k8sClient.Update(ctx, secret)).To(Succeed())
		}
		// credentials is the Secret's four keys. The endpoint is empty for an
		// instance talking to AWS itself, which is what Memgraph defaults to.
		credentials := func(access, secret, endpoint string) map[string][]byte {
			return map[string][]byte{
				memgraphcomv1alpha1.AWSAccessKeySecretKey:   []byte(access),
				memgraphcomv1alpha1.AWSSecretKeySecretKey:   []byte(secret),
				memgraphcomv1alpha1.AWSRegionSecretKey:      []byte(region),
				memgraphcomv1alpha1.AWSEndpointURLSecretKey: []byte(endpoint),
			}
		}
		converged := func() *metav1.Condition {
			GinkgoHelper()
			get(resourceName, cluster)
			return apimeta.FindStatusCondition(cluster.Status.Conditions, memgraphcomv1alpha1.ConditionConverged)
		}
		awsCommands := func() []string {
			var sets []string
			for _, command := range fake.executedCommands() {
				if strings.Contains(command, `SET DATABASE SETTING "aws.`) {
					sets = append(sets, command)
				}
			}
			return sets
		}
		podUIDs := func() map[string]types.UID {
			GinkgoHelper()
			var pods corev1.PodList
			Expect(k8sClient.List(ctx, &pods, client.InNamespace(resourceNamespace),
				client.MatchingLabels{instanceLabel: resourceName})).To(Succeed())
			uids := map[string]types.UID{}
			for _, pod := range pods.Items {
				uids[pod.Name] = pod.UID
			}
			return uids
		}

		BeforeEach(func() {
			putSecret(credentials(accessKey, secretKey, ""))
			resource := &memgraphcomv1alpha1.MemgraphCluster{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: resourceNamespace},
				Spec: memgraphcomv1alpha1.MemgraphClusterSpec{
					AWSCredentials: &memgraphcomv1alpha1.AWSCredentialsSpec{SecretName: secretName},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			get(resourceName, cluster)

			fake.setInstances([]memgraph.Instance{
				observedCoordinator(0, memgraph.RoleLeader),
				observedCoordinator(1, memgraph.RoleFollower),
				observedCoordinator(2, memgraph.RoleFollower),
				{Name: firstInstance, Health: "up", Role: memgraph.RoleMain},
				{Name: secondInstance, Health: "up", Role: memgraph.RoleReplica},
			})
			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			for ordinal := range 3 {
				putPod(coordinatorSuffix, coordsComponent, ordinal, true)
			}
			for ordinal := range 2 {
				putPod(dataSuffix, dataComponent, ordinal, true)
			}
		})

		AfterEach(func() {
			get(resourceName, cluster)
			Expect(k8sClient.Delete(ctx, cluster)).To(Succeed())
			deleteOwned(resourceName)
			for _, suffix := range []string{coordinatorSuffix, dataSuffix} {
				cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
					Name: resourceName + suffix + "-flags", Namespace: resourceNamespace,
				}}
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, cm))).To(Succeed())
			}
			Expect(k8sClient.DeleteAllOf(ctx, &corev1.Pod{},
				client.InNamespace(resourceNamespace),
				client.MatchingLabels{instanceLabel: resourceName},
				client.GracePeriodSeconds(0),
			)).To(Succeed())
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: resourceNamespace}}
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, secret))).To(Succeed())
		})

		It("should SET the four values on every ready data instance, follow a change, and restart none", func() {
			reconcileCluster(resourceName)

			for ordinal := range 2 {
				settings := fake.settingsOf(podAddress(dataSuffix, ordinal))
				Expect(settings).To(HaveKeyWithValue("aws.access_key", accessKey))
				Expect(settings).To(HaveKeyWithValue("aws.secret_key", secretKey))
				Expect(settings).To(HaveKeyWithValue("aws.region", region))
				Expect(settings).To(HaveKeyWithValue("aws.endpoint_url", ""))
			}
			for ordinal := range 3 {
				Expect(fake.settingsOf(podAddress(coordinatorSuffix, ordinal))).To(HaveKeyWithValue("aws.access_key", ""),
					"the coordinators run no queries that read from AWS")
			}
			Expect(awsCommands()).To(HaveLen(6),
				"the keys and the region once per data instance; the empty endpoint is already Memgraph's")
			Expect(converged().Status).To(Equal(metav1.ConditionTrue))

			By("issuing nothing on the next pass, because every instance is in line")
			reconcileCluster(resourceName)
			Expect(awsCommands()).To(HaveLen(6))

			By("SETting only what changed once the Secret does")
			uids := podUIDs()
			putSecret(credentials(accessKey, rotatedKey, minioEndpoint))
			reconcileCluster(resourceName)
			for ordinal := range 2 {
				settings := fake.settingsOf(podAddress(dataSuffix, ordinal))
				Expect(settings).To(HaveKeyWithValue("aws.secret_key", rotatedKey))
				Expect(settings).To(HaveKeyWithValue("aws.endpoint_url", minioEndpoint))
			}
			Expect(awsCommands()[6:]).To(HaveLen(4))
			Expect(awsCommands()[6:]).To(HaveEach(Or(ContainSubstring(secretKeySET), ContainSubstring(endpointSET))))
			Expect(podUIDs()).To(Equal(uids), "a rotation restarts no pod")
			Expect(converged().Status).To(Equal(metav1.ConditionTrue))
		})

		// The AWS credentials and the run-time flags are both SET on a data
		// instance in the same pass, and both plan against its settings: one
		// read serves both, and what the first step SETs is what the second sees.
		It("should read each pod's settings once per pass, however many steps SET on it", func() {
			get(resourceName, cluster)
			cluster.Spec.Flags.Data = map[string]memgraphcomv1alpha1.FlagValue{"query_execution_timeout_sec": "1200"}
			Expect(k8sClient.Update(ctx, cluster)).To(Succeed())

			reads := func() []int {
				counts := make([]int, 0, 2)
				for ordinal := range 2 {
					counts = append(counts, fake.settingsReadsOf(podAddress(dataSuffix, ordinal)))
				}
				return counts
			}
			before := reads()
			reconcileCluster(resourceName)
			after := reads()
			for ordinal := range 2 {
				Expect(after[ordinal]-before[ordinal]).To(Equal(1), "data instance %d", ordinal)
				settings := fake.settingsOf(podAddress(dataSuffix, ordinal))
				Expect(settings).To(HaveKeyWithValue("aws.secret_key", secretKey))
				Expect(settings).To(HaveKeyWithValue("query.timeout", "1200"))
			}
			Expect(converged().Status).To(Equal(metav1.ConditionTrue))
		})

		It("should start the data instances with the Secret's values on their command line, by reference", func() {
			sts := &appsv1.StatefulSet{}
			get(resourceName+dataSuffix, sts)
			container := sts.Spec.Template.Spec.Containers[0]
			Expect(container.Args).To(ContainElements(
				"--aws-access-key=$(MEMGRAPH_AWS_ACCESS_KEY)", "--aws-secret-key=$(MEMGRAPH_AWS_SECRET_KEY)",
				"--aws-region=$(MEMGRAPH_AWS_REGION)", "--aws-endpoint-url=$(MEMGRAPH_AWS_ENDPOINT_URL)"))
			Expect(container.Env).To(ContainElement(And(
				HaveField("Name", "MEMGRAPH_AWS_SECRET_KEY"),
				HaveField("ValueFrom.SecretKeyRef.Name", secretName),
				HaveField("ValueFrom.SecretKeyRef.Key", memgraphcomv1alpha1.AWSSecretKeySecretKey),
			)))
			Expect(fmt.Sprint(sts.Spec)).NotTo(ContainSubstring(secretKey), "the pod spec names the Secret, never a credential")
		})

		It("should skip a data instance that is not ready", func() {
			putPod(dataSuffix, dataComponent, 1, false)

			reconcileCluster(resourceName)

			Expect(fake.settingsOf(podAddress(dataSuffix, 0))).To(HaveKeyWithValue("aws.access_key", accessKey))
			Expect(fake.settingsOf(podAddress(dataSuffix, 1))).To(HaveKeyWithValue("aws.access_key", ""),
				"an unready pod is not dialed: it reads the Secret itself when it starts")
			Expect(converged().Message).NotTo(ContainSubstring(podName(dataSuffix, 1)),
				"an unready pod is the roll's or the kubelet's business, not a credential owed")
		})

		It("should report a credential Memgraph rejects without putting it in status", func() {
			fake.rejectCommand(accessKeySET, errors.New("Unknown setting name 'aws.access_key'"))

			reconcileCluster(resourceName)

			condition := converged()
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal(memgraphcomv1alpha1.ReasonSettingsRejected))
			Expect(condition.Message).To(ContainSubstring(podName(dataSuffix, 0)))
			Expect(condition.Message).To(ContainSubstring(`"aws.access_key"`))
			get(resourceName, cluster)
			Expect(fmt.Sprint(cluster.Status)).NotTo(ContainSubstring(accessKey), "a credential is secret material")
		})

		It("should report a missing Secret or key as pending and apply none of the values", func() {
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: resourceNamespace}}
			Expect(k8sClient.Delete(ctx, secret)).To(Succeed())
			reconcileCluster(resourceName)
			condition := converged()
			Expect(condition.Reason).To(Equal(memgraphcomv1alpha1.ReasonSettingsPending))
			Expect(condition.Message).To(ContainSubstring(secretName + " does not exist"))

			withoutEndpoint := credentials(accessKey, secretKey, "")
			delete(withoutEndpoint, memgraphcomv1alpha1.AWSEndpointURLSecretKey)
			putSecret(withoutEndpoint)
			reconcileCluster(resourceName)
			condition = converged()
			Expect(condition.Reason).To(Equal(memgraphcomv1alpha1.ReasonSettingsPending))
			Expect(condition.Message).To(ContainSubstring("no key " + memgraphcomv1alpha1.AWSEndpointURLSecretKey))
			Expect(awsCommands()).To(BeEmpty(), "a partial set is never applied, and the pods could not start on it")
		})

		It("should map a changed Secret to the clusters that read their AWS credentials from it", func() {
			named := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: resourceNamespace}}
			Expect(reconciler.clustersForSecret(ctx, named)).To(ConsistOf(reconcile.Request{
				NamespacedName: types.NamespacedName{Name: resourceName, Namespace: resourceNamespace},
			}))
		})
	})

	Context("when deciding whether a changed flag needs a restart", func() {
		const (
			resourceName = "mgc-flag-restarts"
			oldRevision  = "mgc-flag-restarts-6c9f8b7d5"
			newRevision  = "mgc-flag-restarts-77b4c8f9d"
		)

		cluster := &memgraphcomv1alpha1.MemgraphCluster{}
		// startedWith is each role's template annotations as the pods were
		// started: the flags and the hash of everything else.
		startedWith := map[string]map[string]string{}

		observedCoordinator := func(id int, role string) memgraph.Instance {
			host := fmt.Sprintf("%s-coordinator-%d.%s-coordinator.%s.svc.cluster.local",
				resourceName, id, resourceName, resourceNamespace)
			return memgraph.Instance{
				Name:              fmt.Sprintf("coordinator_%d", id),
				BoltServer:        fmt.Sprintf("%s:%d", host, memgraphcomv1alpha1.BoltPort),
				CoordinatorServer: fmt.Sprintf("%s:%d", host, memgraphcomv1alpha1.CoordinatorPort),
				ManagementServer:  fmt.Sprintf("%s:%d", host, memgraphcomv1alpha1.ManagementPort),
				Health:            "up", Role: role,
			}
		}
		templateAnnotations := func(suffix string) map[string]string {
			GinkgoHelper()
			sts := &appsv1.StatefulSet{}
			get(resourceName+suffix, sts)
			return map[string]string{
				resources.FlagsAnnotation:        sts.Spec.Template.Annotations[resources.FlagsAnnotation],
				resources.TemplateHashAnnotation: sts.Spec.Template.Annotations[resources.TemplateHashAnnotation],
			}
		}
		// putPod stands in for the StatefulSet controller: one ready pod at the
		// given revision, started with the given flags annotation, the way a
		// pod created from that template would carry it.
		putPod := func(suffix, component string, ordinal int, revision string, annotations map[string]string) {
			GinkgoHelper()
			name := fmt.Sprintf("%s%s-%d", resourceName, suffix, ordinal)
			existing := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: resourceNamespace}}
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, existing))).To(Succeed())
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: resourceNamespace,
					Labels: map[string]string{
						nameLabel:                       memgraphDbName,
						instanceLabel:                   resourceName,
						componentLabel:                  component,
						managedByLabel:                  resources.ManagedByValue,
						appsv1.StatefulSetRevisionLabel: revision,
					},
					Annotations: annotations,
				},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: memgraphDbName, Image: memgraphDbName}}},
			}
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())
			pod.Status.Conditions = []corev1.PodCondition{{
				Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.Now(),
			}}
			Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())
		}
		// declareRevision publishes, for one StatefulSet, the revision its
		// current template hashes to, with a status that describes it.
		declareRevision := func(suffix, revision string) {
			GinkgoHelper()
			sts := &appsv1.StatefulSet{}
			get(resourceName+suffix, sts)
			sts.Status.Replicas = *sts.Spec.Replicas
			sts.Status.ReadyReplicas = *sts.Spec.Replicas
			sts.Status.ObservedGeneration = sts.Generation
			sts.Status.UpdateRevision = revision
			Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())
		}
		podUIDs := func() map[string]types.UID {
			GinkgoHelper()
			var pods corev1.PodList
			Expect(k8sClient.List(ctx, &pods, client.InNamespace(resourceNamespace),
				client.MatchingLabels{instanceLabel: resourceName})).To(Succeed())
			uids := map[string]types.UID{}
			for _, pod := range pods.Items {
				if pod.DeletionTimestamp == nil {
					uids[pod.Name] = pod.UID
				}
			}
			return uids
		}
		condition := func(condType string) *metav1.Condition {
			GinkgoHelper()
			get(resourceName, cluster)
			return apimeta.FindStatusCondition(cluster.Status.Conditions, condType)
		}
		// changeFlags edits spec.flags, lets the apply carry it into the
		// templates, and declares a new revision for each role whose template
		// changed, as the StatefulSet controller would: from then on that
		// role's pods are on the old revision, and only the flags annotation
		// says why. A role whose template is back where its pods started is
		// declared at the pods' revision again, as the StatefulSet controller
		// reuses the old ControllerRevision.
		changeSpec := func(mutate func(*memgraphcomv1alpha1.MemgraphClusterSpec)) {
			GinkgoHelper()
			get(resourceName, cluster)
			mutate(&cluster.Spec)
			Expect(k8sClient.Update(ctx, cluster)).To(Succeed())
			reconcileCluster(resourceName)
			for _, suffix := range []string{coordinatorSuffix, dataSuffix} {
				revision := oldRevision
				if !maps.Equal(templateAnnotations(suffix), startedWith[suffix]) {
					revision = newRevision
				}
				declareRevision(suffix, revision)
			}
		}
		changeFlags := func(flags memgraphcomv1alpha1.FlagsSpec) {
			GinkgoHelper()
			changeSpec(func(spec *memgraphcomv1alpha1.MemgraphClusterSpec) { spec.Flags = flags })
		}
		podAddress := func(suffix string, ordinal int) string {
			return fmt.Sprintf("%s%s-%d.%s%s.%s.svc.cluster.local:%d",
				resourceName, suffix, ordinal, resourceName, suffix, resourceNamespace, memgraphcomv1alpha1.BoltPort)
		}

		BeforeEach(func() {
			resource := &memgraphcomv1alpha1.MemgraphCluster{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: resourceNamespace},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			get(resourceName, cluster)

			fake.setInstances([]memgraph.Instance{
				observedCoordinator(0, memgraph.RoleLeader),
				observedCoordinator(1, memgraph.RoleFollower),
				observedCoordinator(2, memgraph.RoleFollower),
				{Name: "instance_0", Health: "up", Role: memgraph.RoleMain},
				{Name: "instance_1", Health: "up", Role: memgraph.RoleReplica},
			})
			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			for _, suffix := range []string{coordinatorSuffix, dataSuffix} {
				startedWith[suffix] = templateAnnotations(suffix)
			}
			for ordinal := range 3 {
				putPod(coordinatorSuffix, "coordinator", ordinal, oldRevision, startedWith[coordinatorSuffix])
			}
			for ordinal := range 2 {
				putPod(dataSuffix, "data", ordinal, oldRevision, startedWith[dataSuffix])
			}
			declareRevision(coordinatorSuffix, oldRevision)
			declareRevision(dataSuffix, oldRevision)
			reconcileCluster(resourceName)
			Expect(condition(memgraphcomv1alpha1.ConditionUpdated).Status).To(Equal(metav1.ConditionTrue))
		})

		AfterEach(func() {
			get(resourceName, cluster)
			Expect(k8sClient.Delete(ctx, cluster)).To(Succeed())
			deleteOwned(resourceName)
			for _, suffix := range []string{coordinatorSuffix, dataSuffix} {
				cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
					Name: resourceName + suffix + "-flags", Namespace: resourceNamespace,
				}}
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, cm))).To(Succeed())
			}
			Expect(k8sClient.DeleteAllOf(ctx, &corev1.Pod{},
				client.InNamespace(resourceNamespace),
				client.MatchingLabels{instanceLabel: resourceName},
				client.GracePeriodSeconds(0),
			)).To(Succeed())
		})

		It("should SET a coordinator setting and restart no coordinator for it", func() {
			before := podUIDs()
			changeFlags(memgraphcomv1alpha1.FlagsSpec{Coordinators: map[string]memgraphcomv1alpha1.FlagValue{
				readsOnMainSetting: settingOn,
			}})
			reconcileCluster(resourceName)

			Expect(podUIDs()).To(Equal(before), "a coordinator setting must not restart a pod")
			Expect(fake.coordinatorSettingsView()).To(HaveKeyWithValue(readsOnMainSetting, string(settingOn)))
			Expect(condition(memgraphcomv1alpha1.ConditionUpdated).Status).To(Equal(metav1.ConditionTrue))
			Expect(condition(memgraphcomv1alpha1.ConditionConverged).Status).To(Equal(metav1.ConditionTrue))
		})

		It("should restart nothing for a name Memgraph does not have, and report it", func() {
			before := podUIDs()
			changeFlags(memgraphcomv1alpha1.FlagsSpec{Coordinators: map[string]memgraphcomv1alpha1.FlagValue{
				"enabled_reads_on_mai": settingOn,
			}})
			reconcileCluster(resourceName)

			Expect(podUIDs()).To(Equal(before), "a flag that does not exist must not restart a pod")
			converged := condition(memgraphcomv1alpha1.ConditionConverged)
			Expect(converged.Status).To(Equal(metav1.ConditionFalse))
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonUnknownFlags))
			Expect(converged.Message).To(ContainSubstring("coordinators: enabled_reads_on_mai"))
			Expect(condition(memgraphcomv1alpha1.ConditionUpdated).Status).To(Equal(metav1.ConditionTrue))

			By("clearing once the key is removed, still without a restart")
			changeFlags(memgraphcomv1alpha1.FlagsSpec{})
			reconcileCluster(resourceName)
			Expect(podUIDs()).To(Equal(before))
			Expect(condition(memgraphcomv1alpha1.ConditionConverged).Status).To(Equal(metav1.ConditionTrue))
		})

		It("should still restart for a real startup flag beside a coordinator setting", func() {
			before := podUIDs()
			changeFlags(memgraphcomv1alpha1.FlagsSpec{Coordinators: map[string]memgraphcomv1alpha1.FlagValue{
				readsOnMainSetting: settingOn,
				"memory-limit":     "2048",
			}})
			reconcileCluster(resourceName)

			after := podUIDs()
			Expect(after).To(HaveLen(len(before)-1), "exactly one coordinator pod is restarted per pass")
			for name := range before {
				if _, ok := after[name]; !ok {
					Expect(name).To(HavePrefix(resourceName+coordinatorSuffix), "only coordinators changed")
				}
			}
			Expect(condition(memgraphcomv1alpha1.ConditionUpdated).Reason).To(
				Equal(memgraphcomv1alpha1.ReasonRollingRestartInProgress))
		})

		It("should still restart for anything else that changed beside a coordinator setting", func() {
			before := podUIDs()
			changeSpec(func(spec *memgraphcomv1alpha1.MemgraphClusterSpec) {
				spec.Flags.Coordinators = map[string]memgraphcomv1alpha1.FlagValue{readsOnMainSetting: settingOn}
				spec.ExtraEnv.Coordinators = []memgraphcomv1alpha1.EnvVar{{Name: "E2E_ROLL", Value: "1"}}
			})
			reconcileCluster(resourceName)

			Expect(fake.coordinatorSettingsView()).To(HaveKeyWithValue(readsOnMainSetting, string(settingOn)))
			after := podUIDs()
			Expect(after).To(HaveLen(len(before)-1), "the new environment variable needs the restart the setting does not")
			for name := range before {
				if _, ok := after[name]; !ok {
					Expect(name).To(HavePrefix(resourceName + coordinatorSuffix))
				}
			}
		})

		It("should ask a pod on the current template, not the leader, what is a flag", func() {
			// The leader, coordinator_0, runs a binary without new_flag; the
			// data pods are on their current template and their binary has it.
			// Asked of the leader, new_flag would be unknown and nothing would
			// restart; it is a real flag, so the coordinators must roll.
			const newFlag = "new_flag"
			fake.setConfigFor(podAddress(dataSuffix, 0), map[string]string{newFlag: "0"})
			fake.setConfigFor(podAddress(dataSuffix, 1), map[string]string{newFlag: "0"})
			before := podUIDs()
			changeFlags(memgraphcomv1alpha1.FlagsSpec{Coordinators: map[string]memgraphcomv1alpha1.FlagValue{
				newFlag: "1",
			}})
			reconcileCluster(resourceName)

			Expect(podUIDs()).To(HaveLen(len(before)-1), "a flag the template's binary has is a startup flag")
			Expect(condition(memgraphcomv1alpha1.ConditionConverged).Reason).NotTo(
				Equal(memgraphcomv1alpha1.ReasonUnknownFlags))
		})

		It("should wait rather than restart while the flags cannot be classified", func() {
			before := podUIDs()
			fake.setConfigUnknown(true)
			changeFlags(memgraphcomv1alpha1.FlagsSpec{Coordinators: map[string]memgraphcomv1alpha1.FlagValue{
				"memory-limit": "2048",
			}})
			result := reconcileCluster(resourceName)

			Expect(podUIDs()).To(Equal(before), "nothing is restarted on a guess")
			updated := condition(memgraphcomv1alpha1.ConditionUpdated)
			Expect(updated.Status).To(Equal(metav1.ConditionFalse))
			Expect(updated.Reason).To(Equal(memgraphcomv1alpha1.ReasonFlagsUnclassified))
			Expect(updated.Message).To(ContainSubstring("memory_limit"))
			Expect(result.RequeueAfter).To(Equal(requeueWhilePending))

			By("rolling once the view is back and says it is a startup flag")
			fake.setConfigUnknown(false)
			reconcileCluster(resourceName)
			Expect(podUIDs()).To(HaveLen(len(before) - 1))
		})
	})

	Context("when the spec carries coordinator settings", func() {
		const (
			resourceName  = "mgc-coordinator-settings"
			firstInstance = "instance_0"
		)

		cluster := &memgraphcomv1alpha1.MemgraphCluster{}

		observedCoordinator := func(id int, role string) memgraph.Instance {
			host := fmt.Sprintf("%s-coordinator-%d.%s-coordinator.%s.svc.cluster.local",
				resourceName, id, resourceName, resourceNamespace)
			return memgraph.Instance{
				Name:              fmt.Sprintf("coordinator_%d", id),
				BoltServer:        fmt.Sprintf("%s:%d", host, memgraphcomv1alpha1.BoltPort),
				CoordinatorServer: fmt.Sprintf("%s:%d", host, memgraphcomv1alpha1.CoordinatorPort),
				ManagementServer:  fmt.Sprintf("%s:%d", host, memgraphcomv1alpha1.ManagementPort),
				Health:            "up", Role: role,
			}
		}
		setSettings := func(settings map[string]memgraphcomv1alpha1.FlagValue) {
			GinkgoHelper()
			get(resourceName, cluster)
			cluster.Spec.Flags.Coordinators = settings
			Expect(k8sClient.Update(ctx, cluster)).To(Succeed())
		}
		condition := func(condType string) *metav1.Condition {
			GinkgoHelper()
			get(resourceName, cluster)
			return apimeta.FindStatusCondition(cluster.Status.Conditions, condType)
		}
		coordinatorSets := func() []string {
			var sets []string
			for _, command := range fake.executedCommands() {
				if strings.Contains(command, "SET COORDINATOR SETTING") {
					sets = append(sets, command)
				}
			}
			return sets
		}

		BeforeEach(func() {
			resource := &memgraphcomv1alpha1.MemgraphCluster{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: resourceNamespace},
				Spec: memgraphcomv1alpha1.MemgraphClusterSpec{
					Flags: memgraphcomv1alpha1.FlagsSpec{Coordinators: map[string]memgraphcomv1alpha1.FlagValue{
						downTimeoutSetting:   "7",
						"sync_failover_only": settingOn,
					}},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			get(resourceName, cluster)

			// The leader is coordinator_1: the pass must write wherever it
			// connected, not on a coordinator it picked by ordinal.
			fake.setInstances([]memgraph.Instance{
				observedCoordinator(0, memgraph.RoleFollower),
				observedCoordinator(1, memgraph.RoleLeader),
				observedCoordinator(2, memgraph.RoleFollower),
				{Name: firstInstance, Health: "up", Role: memgraph.RoleMain},
				{Name: "instance_1", Health: "up", Role: memgraph.RoleReplica},
			})
			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
		})

		AfterEach(func() {
			get(resourceName, cluster)
			Expect(k8sClient.Delete(ctx, cluster)).To(Succeed())
			deleteOwned(resourceName)
			for _, suffix := range []string{coordinatorSuffix, dataSuffix} {
				cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
					Name: resourceName + suffix + "-flags", Namespace: resourceNamespace,
				}}
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, cm))).To(Succeed())
			}
		})

		It("should write only the settings that differ, once, through the leader connection", func() {
			reconcileCluster(resourceName)

			view := fake.coordinatorSettingsView()
			Expect(view).To(HaveKeyWithValue(downTimeoutSetting, "7"))
			Expect(view).To(HaveKeyWithValue("sync_failover_only", string(settingOn)), "already the default, so untouched")
			Expect(view).To(HaveKeyWithValue(globalReadOnly, string(settingOff)), "a setting the spec does not name is left alone")
			Expect(coordinatorSets()).To(ConsistOf(
				observedCoordinator(1, memgraph.RoleLeader).BoltServer +
					`: SET COORDINATOR SETTING "` + downTimeoutSetting + `" TO "7"`,
			))
			Expect(condition(memgraphcomv1alpha1.ConditionConverged).Status).To(Equal(metav1.ConditionTrue))

			// The builder cannot know a key is a coordinator setting, so the
			// line is in the file — gflags ignores it — and in the template
			// annotation; it is the restart decision that asks Memgraph.
			cm := &corev1.ConfigMap{}
			get(resourceName+coordinatorSuffix+"-flags", cm)
			Expect(cm.Data[resources.FlagFileKey]).To(ContainSubstring("--" + downTimeoutSetting + "=7\n"))

			By("issuing nothing on the next pass")
			reconcileCluster(resourceName)
			Expect(coordinatorSets()).To(HaveLen(1))

			By("leaving the setting as it is once its key is removed")
			setSettings(map[string]memgraphcomv1alpha1.FlagValue{})
			reconcileCluster(resourceName)
			Expect(fake.coordinatorSettingsView()).To(HaveKeyWithValue(downTimeoutSetting, "7"))
			Expect(coordinatorSets()).To(HaveLen(1))
		})

		It("should wait while no ready leader reports the settings, and never write on an empty view", func() {
			fake.setCoordinatorSettingsUnknown(true)
			result := reconcileCluster(resourceName)

			Expect(coordinatorSets()).To(BeEmpty())
			converged := condition(memgraphcomv1alpha1.ConditionConverged)
			Expect(converged.Status).To(Equal(metav1.ConditionFalse))
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonSettingsPending))
			Expect(converged.Message).To(ContainSubstring("coordinator settings"))
			Expect(result.RequeueAfter).To(Equal(requeueWhilePending))

			fake.setCoordinatorSettingsUnknown(false)
			reconcileCluster(resourceName)
			Expect(fake.coordinatorSettingsView()).To(HaveKeyWithValue(downTimeoutSetting, "7"))
			Expect(condition(memgraphcomv1alpha1.ConditionConverged).Status).To(Equal(metav1.ConditionTrue))
		})

		It("should report a key the coordinators have neither as a flag nor as a setting, and apply it once corrected", func() {
			setSettings(map[string]memgraphcomv1alpha1.FlagValue{futureSetting: "1"})
			result := reconcileCluster(resourceName)

			converged := condition(memgraphcomv1alpha1.ConditionConverged)
			Expect(converged.Status).To(Equal(metav1.ConditionFalse))
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonUnknownFlags))
			Expect(converged.Message).To(ContainSubstring("coordinators: " + futureSetting))
			Expect(coordinatorSets()).To(BeEmpty(), "nothing is written for a key Memgraph does not have")
			Expect(result.RequeueAfter).To(Equal(requeueWhilePending))
			Expect(condition(memgraphcomv1alpha1.ConditionReady).Status).To(Equal(metav1.ConditionTrue))

			setSettings(map[string]memgraphcomv1alpha1.FlagValue{globalReadOnly: settingOn})
			reconcileCluster(resourceName)
			Expect(fake.coordinatorSettingsView()).To(HaveKeyWithValue(globalReadOnly, string(settingOn)))
			Expect(condition(memgraphcomv1alpha1.ConditionConverged).Status).To(Equal(metav1.ConditionTrue))
		})
	})

	Context("when exposing the cluster outside Kubernetes", func() {
		const resourceName = "mgc-external"

		const (
			coordinatorsAddress = "203.0.113.10"
			firstDataAddress    = "203.0.113.11"
			secondDataAddress   = "203.0.113.12"
			thirdDataAddress    = "203.0.113.13"
			coordinatorsService = resourceName + coordinatorSuffix + "-external"

			firstInstance  = "instance_0"
			secondInstance = "instance_1"
		)
		dataService := func(ordinal int) string {
			return fmt.Sprintf("%s%s-%d-external", resourceName, dataSuffix, ordinal)
		}
		podAddress := func(suffix string, ordinal int) string {
			return fmt.Sprintf("%s%s-%d.%s%s.%s.svc.cluster.local:%d",
				resourceName, suffix, ordinal, resourceName, suffix, resourceNamespace, memgraphcomv1alpha1.BoltPort)
		}
		bolt := func(host string) string {
			return fmt.Sprintf("%s:%d", host, memgraphcomv1alpha1.BoltPort)
		}

		// giveAddress plays the cloud controller envtest does not run: it reports
		// the named LoadBalancer Service as reachable at the given address.
		giveAddress := func(service string, ingress corev1.LoadBalancerIngress) {
			GinkgoHelper()
			svc := &corev1.Service{}
			get(service, svc)
			svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{ingress}
			Expect(k8sClient.Status().Update(ctx, svc)).To(Succeed())
		}
		giveIP := func(service, ip string) {
			GinkgoHelper()
			giveAddress(service, corev1.LoadBalancerIngress{IP: ip})
		}

		externalServices := func() []corev1.Service {
			GinkgoHelper()
			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			var services corev1.ServiceList
			Expect(k8sClient.List(ctx, &services, client.InNamespace(resourceNamespace),
				client.MatchingLabels(resources.ExternalServicesSelector(cluster)))).To(Succeed())
			return services.Items
		}
		externalServiceNames := func() []string {
			GinkgoHelper()
			services := externalServices()
			names := make([]string, 0, len(services))
			for _, service := range services {
				names = append(names, service.Name)
			}
			return names
		}

		// announced is the bolt address the fake cluster currently has the named
		// member registered at.
		announced := func(name string) string {
			GinkgoHelper()
			for _, instance := range fake.view() {
				if instance.Name == name {
					return instance.BoltServer
				}
			}
			Fail("instance " + name + " is not registered")
			return ""
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

		updateSpec := func(mutate func(*memgraphcomv1alpha1.MemgraphClusterSpec)) {
			GinkgoHelper()
			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			mutate(&cluster.Spec)
			Expect(k8sClient.Update(ctx, cluster)).To(Succeed())
		}

		// bootstrapped drives the exposed cluster to fully registered: one pass
		// provisions, one registers once the pods are ready, one observes the
		// result. What the members are announced at depends on whether the
		// LoadBalancers had addresses by the registration pass.
		bootstrapped := func() {
			GinkgoHelper()
			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			reconcileCluster(resourceName)
			reconcileCluster(resourceName)
		}

		// addressed gives every LoadBalancer its address before the cluster is
		// bootstrapped, which is the common case: a LoadBalancer is provisioned in
		// seconds, a Memgraph pod takes longer to become ready.
		addressed := func() {
			GinkgoHelper()
			reconcileCluster(resourceName)
			giveIP(coordinatorsService, coordinatorsAddress)
			giveIP(dataService(0), firstDataAddress)
			giveIP(dataService(1), secondDataAddress)
			bootstrapped()
		}

		BeforeEach(func() {
			resource := &memgraphcomv1alpha1.MemgraphCluster{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: resourceNamespace},
				Spec: memgraphcomv1alpha1.MemgraphClusterSpec{
					ExternalAccess: &memgraphcomv1alpha1.ExternalAccessSpec{
						Type: memgraphcomv1alpha1.ExternalAccessLoadBalancer,
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
			// envtest runs no garbage collector, so the external Services the
			// cluster owns are removed by hand along with the workloads.
			for _, service := range externalServices() {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &service))).To(Succeed())
			}
			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			Expect(k8sClient.Delete(ctx, cluster)).To(Succeed())
			deleteOwned(resourceName)
		})

		It("should provision one shared coordinators LoadBalancer and one per data instance", func() {
			reconcileCluster(resourceName)

			Expect(externalServiceNames()).To(ConsistOf(coordinatorsService, dataService(0), dataService(1)))
			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			for _, service := range externalServices() {
				Expect(service.Spec.Type).To(Equal(corev1.ServiceTypeLoadBalancer))
				Expect(service.Spec.Ports).To(HaveLen(1), "only the bolt port leaves the cluster")
				Expect(service.Spec.Ports[0].Port).To(Equal(memgraphcomv1alpha1.BoltPort))
				expectControlledBy(&service, cluster)
			}

			data := &corev1.Service{}
			get(dataService(1), data)
			Expect(data.Spec.Selector).To(HaveKeyWithValue(appsv1.StatefulSetPodNameLabel, resourceName+dataSuffix+"-1"),
				"a data LoadBalancer fronts exactly one pod")
			coordinators := &corev1.Service{}
			get(coordinatorsService, coordinators)
			Expect(coordinators.Spec.Selector).NotTo(HaveKey(appsv1.StatefulSetPodNameLabel),
				"the coordinators' LoadBalancer fronts every coordinator")
		})

		It("should register at pod addresses while the LoadBalancers have none, and report the wait", func() {
			bootstrapped()

			for _, name := range []string{"coordinator_0", "coordinator_1", "coordinator_2"} {
				Expect(announced(name)).To(HavePrefix(resourceName + coordinatorSuffix + "-"))
			}
			Expect(announced(firstInstance)).To(Equal(podAddress(dataSuffix, 0)))
			Expect(announced(secondInstance)).To(Equal(podAddress(dataSuffix, 1)))

			s := status()
			Expect(apimeta.IsStatusConditionTrue(s.Conditions, memgraphcomv1alpha1.ConditionReady)).To(BeTrue(),
				"in-cluster clients are served while the LoadBalancers are provisioned")
			converged := convergedCondition()
			Expect(converged).NotTo(BeNil())
			Expect(converged.Status).To(Equal(metav1.ConditionFalse))
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonExternalAddressPending))
			for _, service := range []string{coordinatorsService, dataService(0), dataService(1)} {
				Expect(converged.Message).To(ContainSubstring(service))
			}
			Expect(s.ExternalAccess).NotTo(BeNil())
			Expect(s.ExternalAccess.Coordinators).To(BeEmpty())
			Expect(s.ExternalAccess.Data).To(Equal([]memgraphcomv1alpha1.ExternalAddress{
				{Name: firstInstance}, {Name: secondInstance},
			}))
		})

		It("should move the announced addresses onto the LoadBalancers' as they get them", func() {
			bootstrapped()
			baseline := len(fake.executedCommands())

			giveIP(coordinatorsService, coordinatorsAddress)
			giveIP(dataService(0), firstDataAddress)
			reconcileCluster(resourceName)

			leader := podAddress(coordinatorSuffix, 0)
			Expect(fake.executedCommands()[baseline:]).To(Equal([]string{
				leader + ": UPDATE CONFIG FOR COORDINATOR 0 bolt_server=" + bolt(coordinatorsAddress),
				leader + ": UPDATE CONFIG FOR COORDINATOR 1 bolt_server=" + bolt(coordinatorsAddress),
				leader + ": UPDATE CONFIG FOR COORDINATOR 2 bolt_server=" + bolt(coordinatorsAddress),
				leader + ": UPDATE CONFIG FOR INSTANCE instance_0 bolt_server=" + bolt(firstDataAddress),
			}))
			reconcileCluster(resourceName)
			converged := convergedCondition()
			Expect(converged.Status).To(Equal(metav1.ConditionFalse))
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonExternalAddressPending))
			Expect(converged.Message).To(ContainSubstring(dataService(1)))
			Expect(converged.Message).NotTo(ContainSubstring(dataService(0)))

			giveAddress(dataService(1), corev1.LoadBalancerIngress{IP: secondDataAddress, Hostname: "b.elb.example.com"})
			reconcileCluster(resourceName)
			reconcileCluster(resourceName)

			Expect(announced(secondInstance)).To(Equal(bolt("b.elb.example.com")),
				"a reported hostname is announced over a reported IP")
			Expect(apimeta.IsStatusConditionTrue(status().Conditions, memgraphcomv1alpha1.ConditionConverged)).To(BeTrue())
			Expect(status().ExternalAccess).To(Equal(&memgraphcomv1alpha1.ExternalAccessStatus{
				Coordinators: bolt(coordinatorsAddress),
				Data: []memgraphcomv1alpha1.ExternalAddress{
					{Name: firstInstance, Address: bolt(firstDataAddress)},
					{Name: secondInstance, Address: bolt("b.elb.example.com")},
				},
			}))
		})

		It("should announce the external-dns hostname over whatever the LoadBalancer reports", func() {
			updateSpec(func(spec *memgraphcomv1alpha1.MemgraphClusterSpec) {
				spec.ExternalAccess.Coordinators.Annotations = map[string]string{externalDNSAnnotation: "memgraph.example.com"}
				spec.ExternalAccess.Data.Annotations = map[string]string{
					externalDNSAnnotation: dataHostnamePattern,
				}
			})
			addressed()

			data := &corev1.Service{}
			get(dataService(1), data)
			Expect(data.Annotations).To(HaveKeyWithValue(externalDNSAnnotation, "data-1.memgraph.example.com"),
				"the ordinal is substituted into the per-instance annotation")

			Expect(announced("coordinator_1")).To(Equal(bolt("memgraph.example.com")))
			Expect(announced(firstInstance)).To(Equal(bolt("data-0.memgraph.example.com")))
			Expect(announced(secondInstance)).To(Equal(bolt("data-1.memgraph.example.com")))
			Expect(fake.executedCommands()).NotTo(ContainElement(ContainSubstring("UPDATE CONFIG")),
				"a member whose address is known when it is registered needs no update")
			Expect(apimeta.IsStatusConditionTrue(status().Conditions, memgraphcomv1alpha1.ConditionConverged)).To(BeTrue())
		})

		It("should register a grown data instance at its external address and drop the Service of a retired one", func() {
			addressed()
			Expect(apimeta.IsStatusConditionTrue(status().Conditions, memgraphcomv1alpha1.ConditionConverged)).To(BeTrue())

			updateSpec(func(spec *memgraphcomv1alpha1.MemgraphClusterSpec) { spec.DataInstances = ptr.To(int32(3)) })
			// The widening pass creates the new instance's Service ahead of its pod.
			reconcileCluster(resourceName)
			Expect(externalServiceNames()).To(ContainElement(dataService(2)))
			giveIP(dataService(2), thirdDataAddress)
			markWorkloadsReady(resourceName)
			reconcileCluster(resourceName)
			Expect(announced("instance_2")).To(Equal(bolt(thirdDataAddress)))
			Expect(fake.executedCommands()).NotTo(ContainElement(ContainSubstring("UPDATE CONFIG")))

			updateSpec(func(spec *memgraphcomv1alpha1.MemgraphClusterSpec) { spec.DataInstances = ptr.To(int32(2)) })
			// Unregister, then shed the pod, then the pass that sees the shrunk
			// StatefulSet drops the Service: the instance keeps its LoadBalancer for
			// as long as it keeps its pod.
			reconcileCluster(resourceName)
			Expect(externalServiceNames()).To(ContainElement(dataService(2)))
			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			reconcileCluster(resourceName)
			Expect(externalServiceNames()).To(ConsistOf(coordinatorsService, dataService(0), dataService(1)))
			Expect(status().ExternalAccess.Data).To(HaveLen(2))
		})

		It("should take the LoadBalancers away and re-announce pod addresses when the block is removed", func() {
			addressed()
			baseline := len(fake.executedCommands())

			updateSpec(func(spec *memgraphcomv1alpha1.MemgraphClusterSpec) { spec.ExternalAccess = nil })
			reconcileCluster(resourceName)

			Expect(externalServiceNames()).To(BeEmpty())
			leader := podAddress(coordinatorSuffix, 0)
			Expect(fake.executedCommands()[baseline:]).To(Equal([]string{
				leader + ": UPDATE CONFIG FOR COORDINATOR 0 bolt_server=" + podAddress(coordinatorSuffix, 0),
				leader + ": UPDATE CONFIG FOR COORDINATOR 1 bolt_server=" + podAddress(coordinatorSuffix, 1),
				leader + ": UPDATE CONFIG FOR COORDINATOR 2 bolt_server=" + podAddress(coordinatorSuffix, 2),
				leader + ": UPDATE CONFIG FOR INSTANCE instance_0 bolt_server=" + podAddress(dataSuffix, 0),
				leader + ": UPDATE CONFIG FOR INSTANCE instance_1 bolt_server=" + podAddress(dataSuffix, 1),
			}))
			reconcileCluster(resourceName)
			Expect(status().ExternalAccess).To(BeNil())
			Expect(apimeta.IsStatusConditionTrue(status().Conditions, memgraphcomv1alpha1.ConditionConverged)).To(BeTrue())
		})

		It("should keep talking to the coordinators over their pod addresses", func() {
			addressed()

			for _, command := range fake.executedCommands() {
				Expect(command).To(HavePrefix(podAddress(coordinatorSuffix, 0)+": "),
					"the operator must never go through the coordinators' LoadBalancer itself")
			}
		})
	})

	// Bolt TLS changes nothing about what the operator says to a coordinator,
	// only how it dials: the intent travels with every connect, and the fake,
	// which speaks neither mode, records it. Everything else the block does —
	// the mount, the flags, the ServiceMonitor's scheme — is pure builder
	// output, pinned by the builder tests.
	Context("when serving Bolt over TLS", func() {
		const resourceName = "mgc-bolt-tls"

		cluster := &memgraphcomv1alpha1.MemgraphCluster{}

		BeforeEach(func() {
			resource := &memgraphcomv1alpha1.MemgraphCluster{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: resourceNamespace},
				Spec: memgraphcomv1alpha1.MemgraphClusterSpec{
					TLS: &memgraphcomv1alpha1.TLSSpec{
						Bolt: &memgraphcomv1alpha1.BoltTLSSpec{SecretName: "bolt-tls"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			get(resourceName, cluster)
		})

		AfterEach(func() {
			Expect(k8sClient.Delete(ctx, cluster)).To(Succeed())
			deleteOwned(resourceName)
		})

		It("should dial every coordinator with the TLS intent", func() {
			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			reconcileCluster(resourceName)

			Expect(fake.connects()).NotTo(BeZero(), "the bootstrap pass must have dialed a coordinator")
			Expect(fake.tlsConnects()).To(Equal(fake.connects()),
				"every dial on a cluster serving Bolt TLS must ask for TLS first")
		})

		It("should drop the TLS intent when the block is removed", func() {
			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			reconcileCluster(resourceName)
			dialedWithTLS := fake.tlsConnects()

			get(resourceName, cluster)
			cluster.Spec.TLS = nil
			Expect(k8sClient.Update(ctx, cluster)).To(Succeed())
			reconcileCluster(resourceName)

			Expect(fake.connects()).To(BeNumerically(">", dialedWithTLS), "the pass after the edit must dial again")
			Expect(fake.tlsConnects()).To(Equal(dialedWithTLS), "a plaintext cluster is dialed plaintext first")
		})
	})

	Context("when exposing the cluster through a Gateway", func() {
		const resourceName = "mgc-gateway"

		const (
			gatewayAddress = "203.0.113.20"
			gatewayName    = resourceName + "-gateway"
			portBase       = int32(9100)

			firstInstance  = "instance_0"
			secondInstance = "instance_1"
		)
		coordinatorsService := resourceName + coordinatorSuffix + "-external"
		coordinatorsRoute := resourceName + coordinatorSuffix + "-bolt"
		dataService := func(ordinal int) string {
			return fmt.Sprintf("%s%s-%d-external", resourceName, dataSuffix, ordinal)
		}
		dataRoute := func(ordinal int) string {
			return fmt.Sprintf("%s%s-%d-bolt", resourceName, dataSuffix, ordinal)
		}
		// podAddress is the in-cluster bolt address of a role's first pod: the
		// leader the fake elects, and the instance the specs read back.
		podAddress := func(suffix string) string {
			return fmt.Sprintf("%s%s-0.%s%s.%s.svc.cluster.local:%d",
				resourceName, suffix, resourceName, suffix, resourceNamespace, memgraphcomv1alpha1.BoltPort)
		}
		at := func(host string, port int32) string {
			return fmt.Sprintf("%s:%d", host, port)
		}

		// giveAddress plays the Gateway controller envtest does not run: it
		// reports the Gateway as reachable at the given address.
		giveAddress := func(address gatewayv1.GatewayStatusAddress) {
			GinkgoHelper()
			gateway := &gatewayv1.Gateway{}
			get(gatewayName, gateway)
			gateway.Status.Addresses = []gatewayv1.GatewayStatusAddress{address}
			Expect(k8sClient.Status().Update(ctx, gateway)).To(Succeed())
		}
		giveIP := func() {
			GinkgoHelper()
			giveAddress(gatewayv1.GatewayStatusAddress{Type: ptr.To(gatewayv1.IPAddressType), Value: gatewayAddress})
		}

		externalSelector := func() client.MatchingLabels {
			GinkgoHelper()
			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			return client.MatchingLabels(resources.ExternalServicesSelector(cluster))
		}
		externalNames := func(list client.ObjectList) []string {
			GinkgoHelper()
			Expect(k8sClient.List(ctx, list, client.InNamespace(resourceNamespace), externalSelector())).To(Succeed())
			items, err := apimeta.ExtractList(list)
			Expect(err).NotTo(HaveOccurred())
			names := make([]string, 0, len(items))
			for _, item := range items {
				names = append(names, item.(client.Object).GetName())
			}
			return names
		}
		serviceType := func(name string) corev1.ServiceType {
			GinkgoHelper()
			svc := &corev1.Service{}
			get(name, svc)
			return svc.Spec.Type
		}

		announced := func(name string) string {
			GinkgoHelper()
			for _, instance := range fake.view() {
				if instance.Name == name {
					return instance.BoltServer
				}
			}
			Fail("instance " + name + " is not registered")
			return ""
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

		updateSpec := func(mutate func(*memgraphcomv1alpha1.MemgraphClusterSpec)) {
			GinkgoHelper()
			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			mutate(&cluster.Spec)
			Expect(k8sClient.Update(ctx, cluster)).To(Succeed())
		}

		bootstrapped := func() {
			GinkgoHelper()
			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			reconcileCluster(resourceName)
			reconcileCluster(resourceName)
		}

		// addressed gives the Gateway its address before the cluster is
		// bootstrapped, the common case.
		addressed := func() {
			GinkgoHelper()
			reconcileCluster(resourceName)
			giveIP()
			bootstrapped()
		}

		// cleanupExternal removes every external object by hand: envtest runs no
		// garbage collector.
		cleanupExternal := func() {
			GinkgoHelper()
			for _, list := range []client.ObjectList{&corev1.ServiceList{}, &gatewayv1.GatewayList{}, &gatewayv1.TCPRouteList{}} {
				Expect(k8sClient.List(ctx, list, client.InNamespace(resourceNamespace), externalSelector())).To(Succeed())
				items, err := apimeta.ExtractList(list)
				Expect(err).NotTo(HaveOccurred())
				for _, item := range items {
					Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, item.(client.Object)))).To(Succeed())
				}
			}
		}

		BeforeEach(func() {
			resource := &memgraphcomv1alpha1.MemgraphCluster{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: resourceNamespace},
				Spec: memgraphcomv1alpha1.MemgraphClusterSpec{
					ExternalAccess: &memgraphcomv1alpha1.ExternalAccessSpec{
						Type: memgraphcomv1alpha1.ExternalAccessGateway,
						Gateway: memgraphcomv1alpha1.ExternalAccessGatewaySpec{
							GatewayClassName: "eg",
							DataPortBase:     ptr.To(portBase),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
			cleanupExternal()
			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			Expect(k8sClient.Delete(ctx, cluster)).To(Succeed())
			deleteOwned(resourceName)
		})

		It("should provision one Gateway, one route per way in and ClusterIP Services behind them", func() {
			reconcileCluster(resourceName)

			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			gateway := &gatewayv1.Gateway{}
			get(gatewayName, gateway)
			expectControlledBy(gateway, cluster)
			Expect(gateway.Spec.GatewayClassName).To(BeEquivalentTo("eg"))
			listeners := make([]string, 0, len(gateway.Spec.Listeners))
			for _, listener := range gateway.Spec.Listeners {
				listeners = append(listeners, fmt.Sprintf("%s:%d", listener.Name, listener.Port))
			}
			Expect(listeners).To(Equal([]string{
				fmt.Sprintf("coordinators-bolt:%d", memgraphcomv1alpha1.BoltPort),
				fmt.Sprintf("data-0-bolt:%d", portBase), fmt.Sprintf("data-1-bolt:%d", portBase+1),
			}))

			Expect(externalNames(&gatewayv1.TCPRouteList{})).To(ConsistOf(coordinatorsRoute, dataRoute(0), dataRoute(1)))
			route := &gatewayv1.TCPRoute{}
			get(dataRoute(1), route)
			expectControlledBy(route, cluster)
			Expect(route.Spec.ParentRefs[0].SectionName).To(HaveValue(BeEquivalentTo("data-1-bolt")))
			Expect(route.Spec.Rules[0].BackendRefs[0].Name).To(BeEquivalentTo(dataService(1)))

			Expect(externalNames(&corev1.ServiceList{})).To(ConsistOf(coordinatorsService, dataService(0), dataService(1)))
			Expect(serviceType(dataService(0))).To(Equal(corev1.ServiceTypeClusterIP))
		})

		It("should register at pod addresses while the Gateway has none, naming it once", func() {
			bootstrapped()

			Expect(announced(firstInstance)).To(Equal(podAddress(dataSuffix)))
			converged := convergedCondition()
			Expect(converged.Status).To(Equal(metav1.ConditionFalse))
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonExternalAddressPending))
			Expect(converged.Message).To(Equal("Waiting for an external address on "+gatewayName),
				"every member waits on the same Gateway, which is named once")
		})

		It("should announce every member at the Gateway's address on its own listener port", func() {
			bootstrapped()
			baseline := len(fake.executedCommands())

			giveIP()
			reconcileCluster(resourceName)

			leader := podAddress(coordinatorSuffix)
			Expect(fake.executedCommands()[baseline:]).To(Equal([]string{
				leader + ": UPDATE CONFIG FOR COORDINATOR 0 bolt_server=" + at(gatewayAddress, memgraphcomv1alpha1.BoltPort),
				leader + ": UPDATE CONFIG FOR COORDINATOR 1 bolt_server=" + at(gatewayAddress, memgraphcomv1alpha1.BoltPort),
				leader + ": UPDATE CONFIG FOR COORDINATOR 2 bolt_server=" + at(gatewayAddress, memgraphcomv1alpha1.BoltPort),
				leader + ": UPDATE CONFIG FOR INSTANCE instance_0 bolt_server=" + at(gatewayAddress, portBase),
				leader + ": UPDATE CONFIG FOR INSTANCE instance_1 bolt_server=" + at(gatewayAddress, portBase+1),
			}))
			reconcileCluster(resourceName)
			Expect(apimeta.IsStatusConditionTrue(status().Conditions, memgraphcomv1alpha1.ConditionConverged)).To(BeTrue())
			Expect(status().ExternalAccess).To(Equal(&memgraphcomv1alpha1.ExternalAccessStatus{
				Coordinators: at(gatewayAddress, memgraphcomv1alpha1.BoltPort),
				Data: []memgraphcomv1alpha1.ExternalAddress{
					{Name: firstInstance, Address: at(gatewayAddress, portBase)},
					{Name: secondInstance, Address: at(gatewayAddress, portBase+1)},
				},
			}))
		})

		It("should announce a route's external-dns hostname over the Gateway's address", func() {
			updateSpec(func(spec *memgraphcomv1alpha1.MemgraphClusterSpec) {
				spec.ExternalAccess.Data.Annotations = map[string]string{
					externalDNSAnnotation: dataHostnamePattern,
				}
			})
			addressed()

			route := &gatewayv1.TCPRoute{}
			get(dataRoute(1), route)
			Expect(route.Annotations).To(HaveKeyWithValue(externalDNSAnnotation, "data-1.memgraph.example.com"))
			svc := &corev1.Service{}
			get(dataService(1), svc)
			Expect(svc.Annotations).NotTo(HaveKey(externalDNSAnnotation),
				"behind a Gateway the hostname belongs on the route, not the ClusterIP Service")

			Expect(announced(secondInstance)).To(Equal(at("data-1.memgraph.example.com", portBase+1)))
			Expect(announced("coordinator_0")).To(Equal(at(gatewayAddress, memgraphcomv1alpha1.BoltPort)),
				"a role without a hostname is announced at the Gateway's address")
		})

		It("should switch to LoadBalancers and back, pruning what the other mode owned", func() {
			addressed()
			Expect(apimeta.IsStatusConditionTrue(status().Conditions, memgraphcomv1alpha1.ConditionConverged)).To(BeTrue())

			updateSpec(func(spec *memgraphcomv1alpha1.MemgraphClusterSpec) {
				spec.ExternalAccess.Type = memgraphcomv1alpha1.ExternalAccessLoadBalancer
				spec.ExternalAccess.Gateway = memgraphcomv1alpha1.ExternalAccessGatewaySpec{}
			})
			reconcileCluster(resourceName)

			Expect(externalNames(&gatewayv1.GatewayList{})).To(BeEmpty())
			Expect(externalNames(&gatewayv1.TCPRouteList{})).To(BeEmpty())
			Expect(serviceType(dataService(0))).To(Equal(corev1.ServiceTypeLoadBalancer))
			// The LoadBalancers have no address yet, so every member falls back to
			// its pod address for now.
			Expect(announced(firstInstance)).To(Equal(podAddress(dataSuffix)))
			reconcileCluster(resourceName)
			Expect(convergedCondition().Reason).To(Equal(memgraphcomv1alpha1.ReasonExternalAddressPending))

			updateSpec(func(spec *memgraphcomv1alpha1.MemgraphClusterSpec) {
				spec.ExternalAccess.Type = memgraphcomv1alpha1.ExternalAccessGateway
				spec.ExternalAccess.Gateway = memgraphcomv1alpha1.ExternalAccessGatewaySpec{GatewayClassName: "eg"}
			})
			reconcileCluster(resourceName)
			Expect(externalNames(&gatewayv1.GatewayList{})).To(ConsistOf(gatewayName))
			Expect(serviceType(dataService(0))).To(Equal(corev1.ServiceTypeClusterIP),
				"a LoadBalancer becomes a ClusterIP again, its node ports dropped by the API server")
		})

		It("should report a Gateway exposure as failed on a cluster without the Gateway API", func() {
			reconciler.GatewayAPI = false
			reconciler.GatewayAPIMissing = "TCPRoute is served only as gateway.networking.k8s.io/v1alpha2"
			bootstrapped()

			Expect(externalNames(&gatewayv1.GatewayList{})).To(BeEmpty(), "nothing is built for an API the cluster lacks")
			Expect(serviceType(dataService(0))).To(Equal(corev1.ServiceTypeClusterIP))
			Expect(announced(firstInstance)).To(Equal(podAddress(dataSuffix)))
			Expect(apimeta.IsStatusConditionTrue(status().Conditions, memgraphcomv1alpha1.ConditionReady)).To(BeTrue(),
				"the cluster still serves in-cluster")
			converged := convergedCondition()
			Expect(converged.Status).To(Equal(metav1.ConditionFalse))
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonApplyFailed))
			Expect(converged.Message).To(ContainSubstring("TCPRoute is served only as gateway.networking.k8s.io/v1alpha2"),
				"the message names what the cluster lacks, not merely that something does")
			Expect(converged.Message).To(ContainSubstring("Gateway API v1.6"),
				"the message names the release that serves what the operator builds")
		})
	})

	Context("when asked for a ServiceMonitor", func() {
		const resourceName = "mgc-monitored"

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
		updateSpec := func(mutate func(*memgraphcomv1alpha1.MemgraphClusterSpec)) {
			GinkgoHelper()
			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			mutate(&cluster.Spec)
			Expect(k8sClient.Update(ctx, cluster)).To(Succeed())
		}
		bootstrapped := func() {
			GinkgoHelper()
			reconcileCluster(resourceName)
			markWorkloadsReady(resourceName)
			reconcileCluster(resourceName)
			reconcileCluster(resourceName)
		}
		serviceMonitors := func() []monitoringv1.ServiceMonitor {
			GinkgoHelper()
			list := &monitoringv1.ServiceMonitorList{}
			Expect(k8sClient.List(ctx, list, client.InNamespace(resourceNamespace))).To(Succeed())
			return list.Items
		}
		// cleanupMonitoring removes the ServiceMonitor by hand: envtest runs no
		// garbage collector.
		cleanupMonitoring := func() {
			GinkgoHelper()
			for _, item := range serviceMonitors() {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &item))).To(Succeed())
			}
		}

		BeforeEach(func() {
			resource := &memgraphcomv1alpha1.MemgraphCluster{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: resourceNamespace},
				Spec: memgraphcomv1alpha1.MemgraphClusterSpec{
					Monitoring: &memgraphcomv1alpha1.MonitoringSpec{
						ServiceMonitor: &memgraphcomv1alpha1.ServiceMonitorSpec{
							Labels:   map[string]string{"release": "kube-prometheus-stack"},
							Interval: "15s",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
			cleanupMonitoring()
			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			Expect(k8sClient.Delete(ctx, cluster)).To(Succeed())
			deleteOwned(resourceName)
		})

		It("should create one ServiceMonitor selecting the cluster's Services on the metrics port", func() {
			reconcileCluster(resourceName)

			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			monitor := &monitoringv1.ServiceMonitor{}
			get(resourceName, monitor)
			expectControlledBy(monitor, cluster)
			Expect(monitor.Labels).To(HaveKeyWithValue("release", "kube-prometheus-stack"))
			Expect(monitor.Labels).To(HaveKeyWithValue(resources.MonitoringLabel, resources.MonitoringValue))
			Expect(monitor.Spec.Selector.MatchLabels).To(Equal(map[string]string{
				"app.kubernetes.io/name": "memgraph", instanceLabel: resourceName,
			}))
			Expect(monitor.Spec.Endpoints).To(HaveLen(1))
			Expect(monitor.Spec.Endpoints[0].Port).To(Equal("metrics"))
			Expect(monitor.Spec.Endpoints[0].Path).To(Equal("/metrics"))
			Expect(monitor.Spec.Endpoints[0].Interval).To(BeEquivalentTo("15s"))

			// The selector matches both headless Services, and the endpoint's
			// port name is one both publish.
			for _, name := range []string{resourceName + coordinatorSuffix, resourceName + dataSuffix} {
				svc := &corev1.Service{}
				get(name, svc)
				for key, value := range monitor.Spec.Selector.MatchLabels {
					Expect(svc.Labels).To(HaveKeyWithValue(key, value), name+" is selected")
				}
				Expect(svc.Spec.Ports).To(ContainElement(HaveField("Name", "metrics")), name+" publishes the port")
			}
		})

		It("should prune the ServiceMonitor when the block is removed and leave the cluster converged", func() {
			bootstrapped()
			Expect(serviceMonitors()).To(HaveLen(1))
			Expect(convergedCondition().Status).To(Equal(metav1.ConditionTrue))

			updateSpec(func(spec *memgraphcomv1alpha1.MemgraphClusterSpec) {
				spec.Monitoring = nil
			})
			reconcileCluster(resourceName)
			Expect(serviceMonitors()).To(BeEmpty(), "removing the block takes the object away")
			Expect(convergedCondition().Status).To(Equal(metav1.ConditionTrue))

			updateSpec(func(spec *memgraphcomv1alpha1.MemgraphClusterSpec) {
				spec.Monitoring = &memgraphcomv1alpha1.MonitoringSpec{
					ServiceMonitor: &memgraphcomv1alpha1.ServiceMonitorSpec{},
				}
			})
			reconcileCluster(resourceName)
			Expect(serviceMonitors()).To(HaveLen(1), "adding the block back recreates it")
			Expect(serviceMonitors()[0].Spec.Endpoints[0].Interval).To(BeEmpty(),
				"an empty block names no interval, so Prometheus's default applies")
		})

		It("should report the block as failed on a cluster without the ServiceMonitor CRD", func() {
			reconciler.ServiceMonitorAPI = false
			reconciler.ServiceMonitorAPIMissing = "ServiceMonitor is not served"
			bootstrapped()

			Expect(serviceMonitors()).To(BeEmpty(), "nothing is built for a kind the cluster lacks")
			Expect(apimeta.IsStatusConditionTrue(status().Conditions, memgraphcomv1alpha1.ConditionReady)).To(BeTrue(),
				"the cluster still serves")
			converged := convergedCondition()
			Expect(converged.Status).To(Equal(metav1.ConditionFalse))
			Expect(converged.Reason).To(Equal(memgraphcomv1alpha1.ReasonApplyFailed))
			Expect(converged.Message).To(ContainSubstring("ServiceMonitor is not served"),
				"the message names what the cluster lacks")
			Expect(converged.Message).To(ContainSubstring("Prometheus Operator"),
				"the message names what installs it")

			updateSpec(func(spec *memgraphcomv1alpha1.MemgraphClusterSpec) {
				spec.Monitoring = nil
			})
			reconcileCluster(resourceName)
			Expect(convergedCondition().Status).To(Equal(metav1.ConditionTrue),
				"dropping the block clears the failure")
		})
	})

	Context("when asked for the Grafana dashboard", func() {
		const resourceName = "mgc-dashboard"
		const dashboardName = resourceName + "-grafana-dashboard"

		updateSpec := func(mutate func(*memgraphcomv1alpha1.MemgraphClusterSpec)) {
			GinkgoHelper()
			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			mutate(&cluster.Spec)
			Expect(k8sClient.Update(ctx, cluster)).To(Succeed())
		}
		dashboards := func() []corev1.ConfigMap {
			GinkgoHelper()
			list := &corev1.ConfigMapList{}
			Expect(k8sClient.List(ctx, list, client.InNamespace(resourceNamespace),
				client.MatchingLabels{resources.MonitoringLabel: resources.MonitoringValue})).To(Succeed())
			return list.Items
		}
		cleanupDashboards := func() {
			GinkgoHelper()
			for _, item := range dashboards() {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &item))).To(Succeed())
			}
		}

		BeforeEach(func() {
			resource := &memgraphcomv1alpha1.MemgraphCluster{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: resourceNamespace},
				Spec: memgraphcomv1alpha1.MemgraphClusterSpec{
					Monitoring: &memgraphcomv1alpha1.MonitoringSpec{
						GrafanaDashboard: &memgraphcomv1alpha1.GrafanaDashboardSpec{},
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
			cleanupDashboards()
			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			Expect(k8sClient.Delete(ctx, cluster)).To(Succeed())
			deleteOwned(resourceName)
		})

		It("should create the dashboard ConfigMap with the sidecar's default label", func() {
			reconcileCluster(resourceName)

			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			// The CRD default applied at admission, so an empty block arrived
			// with the label already in it.
			Expect(cluster.Spec.Monitoring.GrafanaDashboard.Labels).To(Equal(map[string]string{"grafana_dashboard": "1"}))

			dashboard := &corev1.ConfigMap{}
			get(dashboardName, dashboard)
			expectControlledBy(dashboard, cluster)
			Expect(dashboard.Labels).To(HaveKeyWithValue("grafana_dashboard", "1"))
			Expect(dashboard.Labels).To(HaveKeyWithValue(resources.MonitoringLabel, resources.MonitoringValue))
			Expect(dashboard.Data).To(HaveKey(resources.GrafanaDashboardKey))
			Expect(dashboard.Data[resources.GrafanaDashboardKey]).To(ContainSubstring(`"title": "Memgraph OpenMetrics"`))
		})

		It("should replace the default label with the block's own and file it by annotation", func() {
			updateSpec(func(spec *memgraphcomv1alpha1.MemgraphClusterSpec) {
				spec.Monitoring.GrafanaDashboard.Labels = map[string]string{"my_sidecar": "yes"}
				spec.Monitoring.GrafanaDashboard.Annotations = map[string]string{"grafana_folder": "Memgraph"}
			})
			reconcileCluster(resourceName)

			dashboard := &corev1.ConfigMap{}
			get(dashboardName, dashboard)
			Expect(dashboard.Labels).To(HaveKeyWithValue("my_sidecar", "yes"))
			Expect(dashboard.Labels).NotTo(HaveKey("grafana_dashboard"),
				"a named label set replaces the default rather than adding to it")
			Expect(dashboard.Annotations).To(HaveKeyWithValue("grafana_folder", "Memgraph"))
		})

		It("should prune the ConfigMap when the block is removed", func() {
			reconcileCluster(resourceName)
			Expect(dashboards()).To(HaveLen(1))

			updateSpec(func(spec *memgraphcomv1alpha1.MemgraphClusterSpec) {
				spec.Monitoring.GrafanaDashboard = nil
			})
			reconcileCluster(resourceName)
			Expect(dashboards()).To(BeEmpty(), "removing the block takes the object away")

			updateSpec(func(spec *memgraphcomv1alpha1.MemgraphClusterSpec) {
				spec.Monitoring = nil
			})
			reconcileCluster(resourceName)
			Expect(dashboards()).To(BeEmpty(), "and so does removing the whole monitoring block")
		})
	})

	Context("when asked for a vmagent", func() {
		const resourceName = "mgc-vmagent"
		const vmagentName = resourceName + "-vmagent"
		const remoteWriteURL = "http://vmsingle.monitoring.svc.cluster.local:8428/api/v1/write"

		updateSpec := func(mutate func(*memgraphcomv1alpha1.MemgraphClusterSpec)) {
			GinkgoHelper()
			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			mutate(&cluster.Spec)
			Expect(k8sClient.Update(ctx, cluster)).To(Succeed())
		}
		marked := client.MatchingLabels{resources.MonitoringLabel: resources.MonitoringValue}
		deployments := func() []appsv1.Deployment {
			GinkgoHelper()
			list := &appsv1.DeploymentList{}
			Expect(k8sClient.List(ctx, list, client.InNamespace(resourceNamespace), marked)).To(Succeed())
			return list.Items
		}
		configMaps := func() []corev1.ConfigMap {
			GinkgoHelper()
			list := &corev1.ConfigMapList{}
			Expect(k8sClient.List(ctx, list, client.InNamespace(resourceNamespace), marked)).To(Succeed())
			return list.Items
		}
		serviceMonitors := func() []monitoringv1.ServiceMonitor {
			GinkgoHelper()
			list := &monitoringv1.ServiceMonitorList{}
			Expect(k8sClient.List(ctx, list, client.InNamespace(resourceNamespace), marked)).To(Succeed())
			return list.Items
		}
		// cleanupMonitoring removes the monitoring objects by hand: envtest runs
		// no garbage collector.
		cleanupMonitoring := func() {
			GinkgoHelper()
			for _, item := range deployments() {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &item))).To(Succeed())
			}
			for _, item := range configMaps() {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &item))).To(Succeed())
			}
			for _, item := range serviceMonitors() {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &item))).To(Succeed())
			}
		}

		BeforeEach(func() {
			resource := &memgraphcomv1alpha1.MemgraphCluster{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: resourceNamespace},
				Spec: memgraphcomv1alpha1.MemgraphClusterSpec{
					Monitoring: &memgraphcomv1alpha1.MonitoringSpec{
						ServiceMonitor: &memgraphcomv1alpha1.ServiceMonitorSpec{},
						VMAgentRemote: &memgraphcomv1alpha1.VMAgentRemoteSpec{
							RemoteWrite: memgraphcomv1alpha1.RemoteWriteSpec{
								URL:       remoteWriteURL,
								BasicAuth: &memgraphcomv1alpha1.BasicAuthSecretSpec{SecretName: "monitoring-basic-auth"},
							},
							ExternalLabels: map[string]string{"cluster": "envtest"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
			cleanupMonitoring()
			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			Expect(k8sClient.Delete(ctx, cluster)).To(Succeed())
			deleteOwned(resourceName)
		})

		It("should run one vmagent scraping every applied pod and writing to the endpoint", func() {
			reconcileCluster(resourceName)

			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			// The CRD defaults applied at admission, so the block arrived with
			// the image and the interval filled in.
			Expect(cluster.Spec.Monitoring.VMAgentRemote.Image.Tag).To(Equal(memgraphcomv1alpha1.DefaultVMAgentImageTag))
			Expect(cluster.Spec.Monitoring.VMAgentRemote.ScrapeInterval).To(Equal(memgraphcomv1alpha1.DefaultVMAgentScrapeInterval))

			deployment := &appsv1.Deployment{}
			get(vmagentName, deployment)
			expectControlledBy(deployment, cluster)
			Expect(deployment.Labels).To(HaveKeyWithValue(resources.MonitoringLabel, resources.MonitoringValue))
			Expect(deployment.Spec.Template.Spec.Containers).To(HaveLen(1))
			container := deployment.Spec.Template.Spec.Containers[0]
			Expect(container.Image).To(Equal(
				memgraphcomv1alpha1.DefaultVMAgentImageRepository + ":" + memgraphcomv1alpha1.DefaultVMAgentImageTag))
			Expect(container.Args).To(ContainElement("-remoteWrite.url=" + remoteWriteURL))
			Expect(container.Args).To(ContainElement("-remoteWrite.basicAuth.passwordFile=/etc/vmagent/basic-auth/password"),
				"the credentials are read from the mounted Secret, never passed on the command line")
			Expect(deployment.Spec.Template.Spec.Volumes).To(ContainElement(
				HaveField("Secret.SecretName", "monitoring-basic-auth")))

			config := &corev1.ConfigMap{}
			get(vmagentName+"-config", config)
			expectControlledBy(config, cluster)
			Expect(config.Labels).To(HaveKeyWithValue(resources.MonitoringLabel, resources.MonitoringValue))
			scrape := config.Data[resources.VMAgentConfigKey]
			Expect(scrape).To(ContainSubstring("scrape_interval: 15s"))
			Expect(scrape).To(ContainSubstring("cluster: envtest"))
			for ordinal := range memgraphcomv1alpha1.DefaultCoordinatorCount {
				Expect(scrape).To(ContainSubstring(fmt.Sprintf("%s-coordinator-%d.%s-coordinator.%s.svc.cluster.local:9091",
					resourceName, ordinal, resourceName, resourceNamespace)))
			}
			for ordinal := range memgraphcomv1alpha1.DefaultDataInstanceCount {
				Expect(scrape).To(ContainSubstring(fmt.Sprintf("%s-data-%d.%s-data.%s.svc.cluster.local:9091",
					resourceName, ordinal, resourceName, resourceNamespace)))
			}
		})

		It("should prune the vmagent when its block is removed and leave the ServiceMonitor alone", func() {
			reconcileCluster(resourceName)
			Expect(deployments()).To(HaveLen(1))
			Expect(configMaps()).To(HaveLen(1))
			Expect(serviceMonitors()).To(HaveLen(1))

			updateSpec(func(spec *memgraphcomv1alpha1.MemgraphClusterSpec) {
				spec.Monitoring.VMAgentRemote = nil
			})
			reconcileCluster(resourceName)
			Expect(deployments()).To(BeEmpty(), "removing the block takes the Deployment away")
			Expect(configMaps()).To(BeEmpty(), "and its scrape config with it")
			Expect(serviceMonitors()).To(HaveLen(1), "the sibling block's object is untouched")
		})
	})

	Context("when asked for the Vector sidecar", func() {
		const resourceName = "mgc-vector"
		const configName = resourceName + "-vector-config"
		const logsEndpoint = "http://victoria-logs.monitoring.svc.cluster.local:9428/insert"

		updateSpec := func(mutate func(*memgraphcomv1alpha1.MemgraphClusterSpec)) {
			GinkgoHelper()
			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			mutate(&cluster.Spec)
			Expect(k8sClient.Update(ctx, cluster)).To(Succeed())
		}
		configMaps := func() []corev1.ConfigMap {
			GinkgoHelper()
			list := &corev1.ConfigMapList{}
			Expect(k8sClient.List(ctx, list, client.InNamespace(resourceNamespace),
				client.MatchingLabels{resources.MonitoringLabel: resources.MonitoringValue})).To(Succeed())
			return list.Items
		}
		containerNames := func(suffix string) []string {
			GinkgoHelper()
			sts := &appsv1.StatefulSet{}
			get(resourceName+suffix, sts)
			names := make([]string, 0, len(sts.Spec.Template.Spec.Containers))
			for _, container := range sts.Spec.Template.Spec.Containers {
				names = append(names, container.Name)
			}
			return names
		}

		BeforeEach(func() {
			resource := &memgraphcomv1alpha1.MemgraphCluster{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: resourceNamespace},
				Spec: memgraphcomv1alpha1.MemgraphClusterSpec{
					Monitoring: &memgraphcomv1alpha1.MonitoringSpec{
						VectorRemote: &memgraphcomv1alpha1.VectorRemoteSpec{
							LogsEndpoint: logsEndpoint,
							Auth:         &memgraphcomv1alpha1.BasicAuthSecretSpec{SecretName: "logs-basic-auth"},
							ExtraLabels:  map[string]string{"cluster_id": "envtest"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
			for _, item := range configMaps() {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &item))).To(Succeed())
			}
			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			Expect(k8sClient.Delete(ctx, cluster)).To(Succeed())
			deleteOwned(resourceName)
		})

		It("should put the sidecar in both roles' pods on one shared configuration", func() {
			reconcileCluster(resourceName)

			cluster := &memgraphcomv1alpha1.MemgraphCluster{}
			get(resourceName, cluster)
			// The CRD defaults applied at admission, so the block arrived with
			// the image filled in.
			Expect(cluster.Spec.Monitoring.VectorRemote.Image.Tag).To(Equal(memgraphcomv1alpha1.DefaultVectorImageTag))

			config := &corev1.ConfigMap{}
			get(configName, config)
			expectControlledBy(config, cluster)
			Expect(config.Labels).To(HaveKeyWithValue(resources.MonitoringLabel, resources.MonitoringValue))
			vectorConfig := config.Data[resources.VectorConfigKey]
			Expect(vectorConfig).To(ContainSubstring("uri: ws://127.0.0.1:7444"))
			Expect(vectorConfig).To(ContainSubstring("endpoint: " + logsEndpoint))
			Expect(vectorConfig).To(ContainSubstring("cluster_id: envtest"))
			Expect(vectorConfig).To(ContainSubstring("user: ${LOGS_USERNAME}"))

			for _, suffix := range []string{coordinatorSuffix, dataSuffix} {
				Expect(containerNames(suffix)).To(Equal([]string{memgraphContainerName, "vector"}), resourceName+suffix)
				sts := &appsv1.StatefulSet{}
				get(resourceName+suffix, sts)
				vector := sts.Spec.Template.Spec.Containers[1]
				Expect(vector.Image).To(Equal(
					memgraphcomv1alpha1.DefaultVectorImageRepository + ":" + memgraphcomv1alpha1.DefaultVectorImageTag))
				Expect(vector.Env).To(ContainElement(corev1.EnvVar{Name: "ROLE", Value: strings.TrimPrefix(suffix, "-")}),
					"each role's sidecar labels its lines with its own role")
				Expect(vector.Env).To(ContainElement(HaveField("ValueFrom.SecretKeyRef.Key", "password")),
					"the credentials reach Vector by Secret reference")
				Expect(sts.Spec.Template.Spec.Volumes).To(ContainElement(HaveField("ConfigMap.Name", configName)))
			}
		})

		It("should take the sidecar and its configuration away when the block is removed", func() {
			reconcileCluster(resourceName)
			Expect(configMaps()).To(HaveLen(1))

			updateSpec(func(spec *memgraphcomv1alpha1.MemgraphClusterSpec) {
				spec.Monitoring = nil
			})
			reconcileCluster(resourceName)
			Expect(configMaps()).To(BeEmpty(), "removing the block prunes the configuration")
			for _, suffix := range []string{coordinatorSuffix, dataSuffix} {
				Expect(containerNames(suffix)).To(Equal([]string{memgraphContainerName}),
					"the pod template no longer carries the sidecar, which the roll then applies")
			}
		})
	})

	Context("when discovering ServiceMonitor", func() {
		It("should find it on a cluster that serves it", func() {
			served, missing, err := ServiceMonitorServed(k8sClient.RESTMapper())
			Expect(err).NotTo(HaveOccurred())
			Expect(served).To(BeTrue())
			Expect(missing).To(BeEmpty())
		})

		It("should report it absent on a cluster that does not, without failing", func() {
			served, missing, err := ServiceMonitorServed(apimeta.NewDefaultRESTMapper(nil))
			Expect(err).NotTo(HaveOccurred())
			Expect(served).To(BeFalse())
			Expect(missing).To(Equal("ServiceMonitor is not served"))
		})
	})

	Context("when discovering the Gateway API", func() {
		It("should find it on a cluster that serves it", func() {
			served, missing, err := GatewayAPIServed(k8sClient.RESTMapper())
			Expect(err).NotTo(HaveOccurred())
			Expect(served).To(BeTrue())
			Expect(missing).To(BeEmpty())
		})

		It("should report it absent on a cluster that does not, without failing", func() {
			served, missing, err := GatewayAPIServed(apimeta.NewDefaultRESTMapper(nil))
			Expect(err).NotTo(HaveOccurred())
			Expect(served).To(BeFalse())
			Expect(missing).To(Equal("Gateway is not served"))
		})

		// The case a cluster on Gateway API older than v1.6 presents: the CRDs
		// exist, but TCPRoute is served at v1alpha2 alone. That has to read as
		// "wrong version", not "no Gateway API", or the remedy is misread.
		It("should name the version served when it is not the one the operator builds", func() {
			// The default versions are what a version-less lookup consults, as
			// the manager's discovery-backed mapper consults the group's served
			// versions.
			mapper := apimeta.NewDefaultRESTMapper([]schema.GroupVersion{
				{Group: gatewayv1.GroupName, Version: "v1"},
				{Group: gatewayv1.GroupName, Version: "v1alpha2"},
			})
			mapper.Add(schema.GroupVersionKind{Group: gatewayv1.GroupName, Version: "v1", Kind: "Gateway"}, apimeta.RESTScopeNamespace)
			mapper.Add(schema.GroupVersionKind{Group: gatewayv1.GroupName, Version: "v1alpha2", Kind: "TCPRoute"}, apimeta.RESTScopeNamespace)

			served, missing, err := GatewayAPIServed(mapper)
			Expect(err).NotTo(HaveOccurred())
			Expect(served).To(BeFalse())
			Expect(missing).To(Equal("TCPRoute is served only as gateway.networking.k8s.io/v1alpha2"))
		})
	})
})
