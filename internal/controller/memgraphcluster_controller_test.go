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
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
)

// Name suffixes of the per-role workload objects a reconcile creates.
const (
	coordinatorSuffix = "-coordinator"
	dataSuffix        = "-data"
)

var _ = Describe("MemgraphCluster Controller", func() {
	const resourceNamespace = "default"

	ctx := context.Background()

	var reconciler *MemgraphClusterReconciler

	BeforeEach(func() {
		reconciler = &MemgraphClusterReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	})

	reconcileCluster := func(name string) {
		GinkgoHelper()
		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: name, Namespace: resourceNamespace},
		})
		Expect(err).NotTo(HaveOccurred())
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
			resource := &memgraphcomv1alpha1.MemgraphCluster{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: resourceNamespace},
				Spec: memgraphcomv1alpha1.MemgraphClusterSpec{
					Coordinators:  ptr.To(int32(1)),
					DataInstances: ptr.To(int32(1)),
					Image: memgraphcomv1alpha1.ImageSpec{
						Repository: "registry.example.com/memgraph",
						Tag:        "3.13.0",
						PullPolicy: corev1.PullAlways,
					},
					Secrets: memgraphcomv1alpha1.SecretsSpec{
						Name:            "my-license",
						LicenseKey:      "license",
						OrganizationKey: "organization",
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

		It("should propagate spec values into the workload objects", func() {
			reconcileCluster(resourceName)

			for _, suffix := range []string{coordinatorSuffix, dataSuffix} {
				sts := &appsv1.StatefulSet{}
				get(resourceName+suffix, sts)
				Expect(sts.Spec.Replicas).To(HaveValue(Equal(int32(1))))

				container := sts.Spec.Template.Spec.Containers[0]
				Expect(container.Image).To(Equal("registry.example.com/memgraph:3.13.0"))
				Expect(container.ImagePullPolicy).To(Equal(corev1.PullAlways))

				licenseRef := container.Env[len(container.Env)-2].ValueFrom.SecretKeyRef
				Expect(licenseRef.Name).To(Equal("my-license"))
				Expect(licenseRef.Key).To(Equal("license"))
				organizationRef := container.Env[len(container.Env)-1].ValueFrom.SecretKeyRef
				Expect(organizationRef.Name).To(Equal("my-license"))
				Expect(organizationRef.Key).To(Equal("organization"))
			}
		})

		It("should reject a spec violating the schema", func() {
			invalid := &memgraphcomv1alpha1.MemgraphCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "mgc-invalid", Namespace: resourceNamespace},
				Spec: memgraphcomv1alpha1.MemgraphClusterSpec{
					Coordinators: ptr.To(int32(0)),
				},
			}
			Expect(k8sClient.Create(ctx, invalid)).NotTo(Succeed())
		})
	})
})
