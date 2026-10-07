//go:build e2e
// +build e2e

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

package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/memgraph/kubernetes-operator/test/utils"
)

// The resize scenario proves what admission and envtest cannot: that growing
// a storage size on a live cluster restarts no pod. The operator patches the
// claims and then deletes each StatefulSet with its pods and claims orphaned
// and recreates it with the grown claim templates; only the real StatefulSet
// controller decides whether it adopts the running pods untouched, and only a
// real resizer and kubelet grow the volumes. The claims live on the CSI
// hostpath driver hack/kind-csi-hostpath.sh installs, which expands online,
// so not one pod UID may change.
//
// The cluster starts under the Delete retention policy, the one that hands
// the claims to the StatefulSet as owner: the orphaning delete must leave
// them, and the recreated StatefulSet must own them again. Then, under
// Retain, a claim left behind by a scale-down is grown while nothing mounts
// it, a re-grow mounts it at the new size, and the pod the re-grow adds gets
// its claim at the new size from the recreated template.
var _ = Describe("MemgraphCluster growing its storage", Ordered, func() {
	const resizeNamespace = "memgraph-e2e-resize"
	const resizeClusterName = "growing"
	const storageClass = "csi-hostpath-expandable"

	cluster := clusterUnderTest{
		namespace: resizeNamespace, name: resizeClusterName, coordinators: 3, dataInstances: 2,
	}

	BeforeAll(func() {
		license, organization := licenseFromEnv()
		preloadMemgraphImage()
		createClusterNamespace(resizeNamespace)

		By("creating the enterprise license Secret")
		createLicenseSecret(resizeNamespace, license, organization)

		By("applying a MemgraphCluster with every claim on the expandable class")
		manifest := fmt.Sprintf(`apiVersion: memgraph.com/v1alpha1
kind: MemgraphCluster
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  coordinators: 3
  dataInstances: 2
  image:
    repository: %[3]s
    tag: %[4]s
  secrets:
    name: %[5]s
  storage:
    retentionPolicy: Delete
    coordinators:
      libPVCSize: 1Gi
      libStorageClassName: %[6]s
    data:
      libPVCSize: 1Gi
      libStorageClassName: %[6]s
  coreDumps:
    storageClassName: %[6]s
    configureCorePattern: false
    coordinators:
      size: 1Gi
    data:
      size: 1Gi
  resources:
    coordinators:
      requests:
        cpu: 50m
        memory: 200Mi
    data:
      requests:
        cpu: 50m
        memory: 300Mi
`, resizeClusterName, resizeNamespace, example.Spec.Image.Repository, example.Spec.Image.Tag,
			licenseSecretName, storageClass)
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		_, err := utils.RunWithInput(cmd, manifest)
		Expect(err).NotTo(HaveOccurred(), "Failed to apply the MemgraphCluster")

		Eventually(cluster.verifyRegistered, 10*time.Minute, 10*time.Second).Should(Succeed())
		cluster.awaitConverged(5 * time.Minute)
	})

	AfterAll(func() {
		By("removing the cluster namespace and waiting for its pods to go")
		cmd := exec.Command("kubectl", "delete", "ns", resizeNamespace,
			"--ignore-not-found", "--wait=true", "--timeout=10m")
		_, _ = utils.Run(cmd)
	})

	AfterEach(func() {
		dumpDiagnosticsOnFailure(resizeNamespace)
	})

	It("grows every claim of both roles under running pods and restarts none of them", func() {
		pods, err := cluster.podUIDs()
		Expect(err).NotTo(HaveOccurred())
		Expect(pods).To(HaveLen(5))
		claimsBefore := listClaims(resizeNamespace, resizeClusterName)
		Expect(claimsBefore).To(HaveLen(10), "two claims for each of the five pods")
		statefulSetsBefore := statefulSetUIDs(resizeNamespace, resizeClusterName)

		By("growing the lib and core dumps sizes of both roles in one edit")
		cmd := exec.Command("kubectl", "patch", "memgraphcluster", resizeClusterName,
			"-n", resizeNamespace, "--type=merge", "-p", `{"spec":{
  "storage":{"coordinators":{"libPVCSize":"2Gi"},"data":{"libPVCSize":"2Gi"}},
  "coreDumps":{"coordinators":{"size":"2Gi"},"data":{"size":"2Gi"}}}}`)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "admission must accept a grown size")

		By("waiting for every claim to hold the grown size and both StatefulSets to carry it")
		Eventually(func(g Gomega) {
			for _, claim := range listClaims(resizeNamespace, resizeClusterName) {
				g.Expect(claim.request).To(Equal("2Gi"), "claim %s", claim.name)
				g.Expect(claim.capacity).To(Equal("2Gi"), "claim %s", claim.name)
			}
			for _, component := range []string{"coordinator", "data"} {
				g.Expect(claimTemplateSizes(resizeNamespace, resizeClusterName+"-"+component)).
					To(Equal("lib-storage=2Gi core-dumps=2Gi"))
			}
		}, 10*time.Minute, 10*time.Second).Should(Succeed())
		cluster.awaitConverged(5 * time.Minute)

		By("confirming both StatefulSets were recreated around the same pods and claims")
		statefulSetsAfter := statefulSetUIDs(resizeNamespace, resizeClusterName)
		for component, uid := range statefulSetsBefore {
			Expect(statefulSetsAfter[component]).NotTo(Equal(uid), "the %s StatefulSet was recreated", component)
		}
		Expect(cluster.podUIDs()).To(Equal(pods), "an online expansion restarts no pod")
		claimsAfter := listClaims(resizeNamespace, resizeClusterName)
		for name, before := range claimsBefore {
			after, ok := claimsAfter[name]
			Expect(ok).To(BeTrue(), "claim %s survived the orphaning delete", name)
			Expect(after.uid).To(Equal(before.uid), "claim %s is the same claim", name)
			component := "data"
			if strings.Contains(name, "-"+resizeClusterName+"-coordinator-") {
				component = "coordinator"
			}
			Expect(after.owner).To(Equal(statefulSetsAfter[component]),
				"claim %s is owned by the recreated StatefulSet under the Delete policy", name)
		}
		Expect(updatedStatus(resizeNamespace, resizeClusterName)).To(Equal("True"))
	})

	It("grows a claim a scale-down left behind, which a re-grow mounts at the new size", func() {
		By("keeping the claims of a scale-down")
		cmd := exec.Command("kubectl", "patch", "memgraphcluster", resizeClusterName,
			"-n", resizeNamespace, "--type=merge", "-p", `{"spec":{"storage":{"retentionPolicy":"Retain"},"dataInstances":1}}`)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		shrunk := cluster.withTopology(3, 1)
		Eventually(shrunk.verifyRegistered, 10*time.Minute, 10*time.Second).Should(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(shrunk.podExists("data", 1)).To(BeFalse())
		}, 5*time.Minute, 5*time.Second).Should(Succeed())
		shrunk.awaitConverged(5 * time.Minute)
		retained := fmt.Sprintf("lib-storage-%s-data-1", resizeClusterName)
		Expect(listClaims(resizeNamespace, resizeClusterName)).To(HaveKey(retained))
		pods, err := shrunk.podUIDs()
		Expect(err).NotTo(HaveOccurred())

		By("growing the data instances' lib storage while nothing mounts the retained claim")
		cmd = exec.Command("kubectl", "patch", "memgraphcluster", resizeClusterName,
			"-n", resizeNamespace, "--type=merge", "-p", `{"spec":{"storage":{"data":{"libPVCSize":"3Gi"}}}}`)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func(g Gomega) {
			claims := listClaims(resizeNamespace, resizeClusterName)
			g.Expect(claims[retained].request).To(Equal("3Gi"), "the retained claim is grown too")
			mounted := claims[fmt.Sprintf("lib-storage-%s-data-0", resizeClusterName)]
			g.Expect(mounted.capacity).To(Equal("3Gi"))
			g.Expect(claimTemplateSizes(resizeNamespace, resizeClusterName+"-data")).
				To(Equal("lib-storage=3Gi core-dumps=2Gi"))
		}, 10*time.Minute, 10*time.Second).Should(Succeed())
		shrunk.awaitConverged(5 * time.Minute)
		Expect(shrunk.podUIDs()).To(Equal(pods), "an online expansion restarts no pod")

		By("growing the data instances back past the retained claim")
		cmd = exec.Command("kubectl", "patch", "memgraphcluster", resizeClusterName,
			"-n", resizeNamespace, "--type=merge", "-p", `{"spec":{"dataInstances":3}}`)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		grown := cluster.withTopology(3, 3)
		Eventually(grown.verifyRegistered, 10*time.Minute, 10*time.Second).Should(Succeed())
		grown.awaitConverged(5 * time.Minute)
		claims := listClaims(resizeNamespace, resizeClusterName)
		Expect(claims[retained].capacity).To(Equal("3Gi"), "the re-grown pod mounts the retained claim at the new size")
		added := claims[fmt.Sprintf("lib-storage-%s-data-2", resizeClusterName)]
		Expect(added.request).To(Equal("3Gi"), "a claim the recreated template creates starts at the new size")
		Expect(added.capacity).To(Equal("3Gi"))
	})
})

// observedClaim is one PersistentVolumeClaim of a cluster as kubectl reports
// it, sizes normalised so 2Gi and 2048Mi read alike.
type observedClaim struct {
	name     string
	uid      string
	request  string
	capacity string
	// owner is the UID of the StatefulSet owning the claim, or empty.
	owner string
}

// listClaims reads every claim of a cluster, by name.
func listClaims(namespace, cluster string) map[string]observedClaim {
	GinkgoHelper()
	cmd := exec.Command("kubectl", "get", "pvc", "-n", namespace,
		"-l", "app.kubernetes.io/instance="+cluster,
		"-o", `jsonpath={range .items[*]}{.metadata.name}{" "}{.metadata.uid}{" "}`+
			`{.spec.resources.requests.storage}{" "}{.status.capacity.storage}{" "}`+
			`{.metadata.ownerReferences[?(@.kind=="StatefulSet")].uid}{"\n"}{end}`)
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to list the claims")
	claims := map[string]observedClaim{}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		claim := observedClaim{name: fields[0], uid: fields[1], request: normalisedSize(fields[2])}
		if len(fields) > 3 {
			claim.capacity = normalisedSize(fields[3])
		}
		if len(fields) > 4 {
			claim.owner = fields[4]
		}
		claims[claim.name] = claim
	}
	return claims
}

// claimTemplateSizes renders a StatefulSet's claim templates in order with
// their sizes, e.g. "lib-storage=2Gi core-dumps=2Gi".
func claimTemplateSizes(namespace, statefulSet string) string {
	GinkgoHelper()
	cmd := exec.Command("kubectl", "get", "statefulset", statefulSet, "-n", namespace, "-o",
		`jsonpath={range .spec.volumeClaimTemplates[*]}{.metadata.name}={.spec.resources.requests.storage} {end}`)
	output, err := utils.Run(cmd)
	if err != nil {
		// Read inside a polled assertion, during the moment the StatefulSet is
		// being recreated.
		return err.Error()
	}
	var sizes []string
	for field := range strings.FieldsSeq(output) {
		name, size, _ := strings.Cut(field, "=")
		sizes = append(sizes, name+"="+normalisedSize(size))
	}
	return strings.Join(sizes, " ")
}

// statefulSetUIDs maps each role to the UID of its StatefulSet.
func statefulSetUIDs(namespace, cluster string) map[string]string {
	GinkgoHelper()
	uids := map[string]string{}
	for _, component := range []string{"coordinator", "data"} {
		cmd := exec.Command("kubectl", "get", "statefulset", cluster+"-"+component, "-n", namespace,
			"-o", "jsonpath={.metadata.uid}")
		output, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to read the %s StatefulSet", component)
		uids[component] = strings.TrimSpace(output)
	}
	return uids
}

// updatedStatus is the status of the resource's Updated condition.
func updatedStatus(namespace, cluster string) string {
	GinkgoHelper()
	cmd := exec.Command("kubectl", "get", "memgraphcluster", cluster, "-n", namespace, "-o",
		`jsonpath={.status.conditions[?(@.type=="Updated")].status}`)
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to read the Updated condition")
	return strings.TrimSpace(output)
}

// normalisedSize renders a quantity canonically, or returns it as given when
// it is not one.
func normalisedSize(size string) string {
	quantity, err := resource.ParseQuantity(size)
	if err != nil {
		return size
	}
	return quantity.String()
}
