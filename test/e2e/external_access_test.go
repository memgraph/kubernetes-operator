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
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
	"github.com/memgraph/kubernetes-operator/internal/resources"
	"github.com/memgraph/kubernetes-operator/test/utils"
)

// The external access scenario is the one place the suite is itself a client
// outside the cluster: the test process runs on the host, the LoadBalancer
// addresses come from the Kind Docker network the host routes to (see
// hack/kind-metallb.sh), and the routing driver below is handed nothing but the
// coordinators' external address. What it proves is the whole point of the
// feature — that the routing table the coordinators hand a client names
// addresses that client can reach, and that it stops doing so when the block is
// removed.
var _ = Describe("MemgraphCluster exposed through LoadBalancers", Ordered, func() {
	const externalNamespace = "memgraph-e2e-external"
	const externalClusterName = "exposed"

	// ordinalAnnotation is a per-instance annotation the scenario asks for, to
	// prove the placeholder is substituted on the real Services.
	const ordinalAnnotation = "e2e.memgraph.com/ordinal"

	exposed := clusterUnderTest{
		namespace: externalNamespace, name: externalClusterName, coordinators: 3, dataInstances: 2,
	}

	BeforeAll(func() {
		license, organization := licenseFromEnv()

		preloadMemgraphImage()
		createClusterNamespace(externalNamespace)

		By("creating the enterprise license Secret")
		createLicenseSecret(externalNamespace, license, organization)

		By("applying a MemgraphCluster exposed through LoadBalancers")
		manifest := fmt.Sprintf(`apiVersion: memgraph.com/v1alpha1
kind: MemgraphCluster
metadata:
  name: %s
  namespace: %s
spec:
  coordinators: %d
  dataInstances: %d
  image:
    repository: %s
    tag: %s
  secrets:
    name: %s
  storage:
    coordinators:
      createLogStorageClaim: false
    data:
      createLogStorageClaim: false
  resources:
    coordinators:
      requests:
        cpu: 50m
        memory: 200Mi
    data:
      requests:
        cpu: 50m
        memory: 300Mi
  externalAccess:
    type: LoadBalancer
    coordinators:
      labels:
        e2e.memgraph.com/role: coordinators
    data:
      annotations:
        %s: "{ordinal}"
`, externalClusterName, externalNamespace, exposed.coordinators, exposed.dataInstances,
			example.Spec.Image.Repository, example.Spec.Image.Tag, licenseSecretName, ordinalAnnotation)
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		_, err := utils.RunWithInput(cmd, manifest)
		Expect(err).NotTo(HaveOccurred(), "Failed to apply the MemgraphCluster")
	})

	AfterAll(func() {
		By("removing the cluster namespace and waiting for its pods to go")
		cmd := exec.Command("kubectl", "delete", "ns", externalNamespace,
			"--ignore-not-found", "--wait=true", "--timeout=10m")
		_, _ = utils.Run(cmd)
	})

	AfterEach(func() {
		dumpDiagnosticsOnFailure(externalNamespace)
	})

	It("bootstraps and reports Converged once every LoadBalancer has an address", func() {
		Eventually(exposed.verifyRegistered, 10*time.Minute, 10*time.Second).Should(Succeed())
		exposed.awaitConverged(5 * time.Minute)

		By("confirming the status reports the LoadBalancers' addresses")
		status := exposed.externalAccessStatus()
		Expect(status.Coordinators).To(Equal(exposed.loadBalancerAddress(exposed.coordinatorsExternalService())))
		Expect(status.Data).To(HaveLen(int(exposed.dataInstances)))
		for ordinal, instance := range status.Data {
			Expect(instance.Name).To(Equal(resources.DataInstanceName(int32(ordinal))))
			Expect(instance.Address).To(Equal(exposed.loadBalancerAddress(exposed.dataExternalService(int32(ordinal)))))
		}

		By("confirming the per-instance annotation carries each instance's ordinal")
		for ordinal := range exposed.dataInstances {
			cmd := exec.Command("kubectl", "get", "service", exposed.dataExternalService(ordinal),
				"-n", externalNamespace, "-o", fmt.Sprintf(`jsonpath={.metadata.annotations.%s}`,
					strings.ReplaceAll(ordinalAnnotation, ".", `\.`)))
			got, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(got)).To(Equal(fmt.Sprint(ordinal)))
		}
	})

	// The routing table is what the whole feature is for: every member has to be
	// announced at the address a client outside the cluster reaches it through,
	// and the operator's own way in has to stay the pod address.
	It("announces every instance at its LoadBalancer address", func() {
		status := exposed.externalAccessStatus()
		view, err := exposed.leaderView()
		Expect(err).NotTo(HaveOccurred())

		announced := map[string]string{}
		for _, instance := range view {
			announced[instance.name] = instance.boltServer
		}
		for ordinal := range exposed.coordinators {
			Expect(announced).To(HaveKeyWithValue(resources.CoordinatorInstanceName(ordinal), status.Coordinators),
				"every coordinator is announced at the shared LoadBalancer")
		}
		for _, instance := range status.Data {
			Expect(announced).To(HaveKeyWithValue(instance.Name, instance.Address),
				"every data instance is announced at its own LoadBalancer")
		}
	})

	It("serves a client outside the cluster through the routing table", func() {
		ctx := context.Background()
		status := exposed.externalAccessStatus()

		By("connecting with the routing driver to the coordinators' external address only")
		driver, err := neo4j.NewDriverWithContext("neo4j://"+status.Coordinators, neo4j.NoAuth())
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = driver.Close(ctx) }()
		Expect(driver.VerifyConnectivity(ctx)).To(Succeed(),
			"the coordinators' LoadBalancer must be reachable from outside the cluster")

		By("writing through the routing table, which sends the write to MAIN")
		write := driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
		_, err = write.Run(ctx, "CREATE (:E2E {source: 'external'})", nil)
		Expect(err).NotTo(HaveOccurred(), "a write routed to MAIN through its external address must succeed")
		Expect(write.Close(ctx)).To(Succeed())

		By("reading it back through the routing table, which sends the read to a replica")
		Eventually(func(g Gomega) {
			read := driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
			defer func() { _ = read.Close(ctx) }()
			result, err := read.Run(ctx, "MATCH (n:E2E) RETURN count(n) AS n", nil)
			g.Expect(err).NotTo(HaveOccurred())
			record, err := result.Single(ctx)
			g.Expect(err).NotTo(HaveOccurred())
			count, _ := record.Get("n")
			g.Expect(count).To(BeNumerically(">=", 1))
		}, 2*time.Minute, 5*time.Second).Should(Succeed(), "the write must be readable through an external replica address")
	})

	It("takes the LoadBalancers away and re-announces pod addresses when the block is removed", func() {
		By("removing the externalAccess block")
		cmd := exec.Command("kubectl", "patch", "memgraphcluster", externalClusterName,
			"-n", externalNamespace, "--type=merge", "-p", `{"spec":{"externalAccess":null}}`)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for the external Services to go")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "services", "-n", externalNamespace,
				"-l", resources.ExternalAccessLabel+"="+resources.ExternalAccessValue, "-o", "name")
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(strings.TrimSpace(out)).To(BeEmpty())
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("waiting for every instance to be announced at its pod address again")
		Eventually(func(g Gomega) {
			view, err := exposed.leaderView()
			g.Expect(err).NotTo(HaveOccurred())
			for _, instance := range view {
				g.Expect(instance.boltServer).To(HaveSuffix(
					fmt.Sprintf(".%s.svc.cluster.local:%d", externalNamespace, memgraphcomv1alpha1.BoltPort)),
					"%s is still announced externally", instance.name)
			}
		}, 3*time.Minute, 5*time.Second).Should(Succeed())

		exposed.awaitConverged(3 * time.Minute)
		cmd = exec.Command("kubectl", "get", "memgraphcluster", externalClusterName, "-n", externalNamespace,
			"-o", "jsonpath={.status.externalAccess}")
		out, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(out)).To(BeEmpty(), "an unexposed cluster reports no external addresses")
	})
})

// coordinatorsExternalService and dataExternalService are the names of the
// LoadBalancer Services the operator builds for this cluster.
func (c clusterUnderTest) coordinatorsExternalService() string {
	return c.name + "-coordinator-external"
}

func (c clusterUnderTest) dataExternalService(ordinal int32) string {
	return fmt.Sprintf("%s-data-%d-external", c.name, ordinal)
}

// loadBalancerAddress is the "host:port" the named LoadBalancer Service is
// reachable at, read off the address MetalLB assigned it.
func (c clusterUnderTest) loadBalancerAddress(service string) string {
	GinkgoHelper()
	cmd := exec.Command("kubectl", "get", "service", service, "-n", c.namespace,
		"-o", "jsonpath={.status.loadBalancer.ingress[0].ip}")
	ip, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(strings.TrimSpace(ip)).NotTo(BeEmpty(), "Service %s has no LoadBalancer address", service)
	return fmt.Sprintf("%s:%d", strings.TrimSpace(ip), memgraphcomv1alpha1.BoltPort)
}

// externalAccessStatus reads the external addresses the operator publishes on the
// resource's status.
func (c clusterUnderTest) externalAccessStatus() memgraphcomv1alpha1.ExternalAccessStatus {
	GinkgoHelper()
	cmd := exec.Command("kubectl", "get", "memgraphcluster", c.name, "-n", c.namespace,
		"-o", "jsonpath={.status.externalAccess}")
	out, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(strings.TrimSpace(out)).NotTo(BeEmpty(), "the resource reports no external access status")

	var status memgraphcomv1alpha1.ExternalAccessStatus
	Expect(json.Unmarshal([]byte(out), &status)).To(Succeed())
	return status
}
