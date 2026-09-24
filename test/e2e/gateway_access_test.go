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

// The Gateway scenario is the LoadBalancer one with a single way in: one
// Gateway, programmed by Envoy Gateway (hack/kind-envoy-gateway.sh) and given
// its address by MetalLB, with the coordinators on the bolt port and every data
// instance on a port of its own. The proof is the same — a routing driver on the
// host, handed the Gateway's address alone, writes and reads through it.
var _ = Describe("MemgraphCluster exposed through a Gateway", Ordered, func() {
	const gatewayNamespace = "memgraph-e2e-gateway"
	const gatewayClusterName = "gated"

	// gatewayClass is the class hack/kind-envoy-gateway.sh creates.
	const gatewayClass = "eg"

	// dataPortBase is deliberately not the default, so the scenario proves the
	// knob reaches the listeners and the announced addresses.
	const dataPortBase = int32(9500)

	gated := clusterUnderTest{
		namespace: gatewayNamespace, name: gatewayClusterName, coordinators: 3, dataInstances: 2,
	}

	BeforeAll(func() {
		license, organization := licenseFromEnv()

		preloadMemgraphImage()
		createClusterNamespace(gatewayNamespace)

		By("creating the enterprise license Secret")
		createLicenseSecret(gatewayNamespace, license, organization)

		By("applying a MemgraphCluster exposed through a Gateway")
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
    type: Gateway
    gateway:
      gatewayClassName: %s
      dataPortBase: %d
`, gatewayClusterName, gatewayNamespace, gated.coordinators, gated.dataInstances,
			example.Spec.Image.Repository, example.Spec.Image.Tag, licenseSecretName, gatewayClass, dataPortBase)
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		_, err := utils.RunWithInput(cmd, manifest)
		Expect(err).NotTo(HaveOccurred(), "Failed to apply the MemgraphCluster")
	})

	AfterAll(func() {
		By("removing the cluster namespace and waiting for its pods to go")
		cmd := exec.Command("kubectl", "delete", "ns", gatewayNamespace,
			"--ignore-not-found", "--wait=true", "--timeout=10m")
		_, _ = utils.Run(cmd)
	})

	AfterEach(func() {
		dumpDiagnosticsOnFailure(gatewayNamespace)
		if CurrentSpecReport().Failed() {
			for _, args := range [][]string{
				{"get", "gateway,tcproute", "-n", gatewayNamespace, "-o", "yaml"},
				{"get", "pods", "-n", "envoy-gateway-system", "-o", "wide"},
			} {
				cmd := exec.Command("kubectl", args...)
				output, _ := utils.Run(cmd)
				_, _ = fmt.Fprintf(GinkgoWriter, "Diagnostics kubectl %v:\n%s\n", args, output)
			}
		}
	})

	It("bootstraps and reports Converged once the Gateway has an address", func() {
		Eventually(gated.verifyRegistered, 10*time.Minute, 10*time.Second).Should(Succeed())
		gated.awaitConverged(5 * time.Minute)

		By("confirming the status announces every member at the Gateway's address on its own port")
		host := gated.gatewayHost()
		status := gated.externalAccessStatus()
		Expect(status.Coordinators).To(Equal(fmt.Sprintf("%s:%d", host, memgraphcomv1alpha1.BoltPort)))
		Expect(status.Data).To(HaveLen(int(gated.dataInstances)))
		for ordinal, instance := range status.Data {
			Expect(instance.Name).To(Equal(resources.DataInstanceName(int32(ordinal))))
			Expect(instance.Address).To(Equal(fmt.Sprintf("%s:%d", host, dataPortBase+int32(ordinal))))
		}
	})

	It("announces every instance at its Gateway listener", func() {
		status := gated.externalAccessStatus()
		view, err := gated.leaderView()
		Expect(err).NotTo(HaveOccurred())

		announced := map[string]string{}
		for _, instance := range view {
			announced[instance.name] = instance.boltServer
		}
		for ordinal := range gated.coordinators {
			Expect(announced).To(HaveKeyWithValue(resources.CoordinatorInstanceName(ordinal), status.Coordinators))
		}
		for _, instance := range status.Data {
			Expect(announced).To(HaveKeyWithValue(instance.Name, instance.Address))
		}
	})

	It("serves a client outside the cluster through the Gateway", func() {
		ctx := context.Background()
		status := gated.externalAccessStatus()

		By("connecting with the routing driver to the Gateway's coordinators listener only")
		driver, err := neo4j.NewDriverWithContext("neo4j://"+status.Coordinators, neo4j.NoAuth())
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = driver.Close(ctx) }()
		// Envoy programs the listeners a moment after the Gateway reports its
		// address, so the first connection may land before they are open.
		Eventually(func() error { return driver.VerifyConnectivity(ctx) }, 2*time.Minute, 5*time.Second).Should(Succeed(),
			"the Gateway must be reachable from outside the cluster")

		By("writing through the routing table, which sends the write to MAIN")
		write := driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
		_, err = write.Run(ctx, "CREATE (:E2E {source: 'gateway'})", nil)
		Expect(err).NotTo(HaveOccurred(), "a write routed to MAIN through its Gateway listener must succeed")
		Expect(write.Close(ctx)).To(Succeed())

		By("reading it back through the routing table")
		Eventually(func(g Gomega) {
			read := driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
			defer func() { _ = read.Close(ctx) }()
			result, err := read.Run(ctx, "MATCH (n:E2E) RETURN count(n) AS n", nil)
			g.Expect(err).NotTo(HaveOccurred())
			record, err := result.Single(ctx)
			g.Expect(err).NotTo(HaveOccurred())
			count, _ := record.Get("n")
			g.Expect(count).To(BeNumerically(">=", 1))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())
	})

	It("takes the Gateway away and re-announces pod addresses when the block is removed", func() {
		By("removing the externalAccess block")
		cmd := exec.Command("kubectl", "patch", "memgraphcluster", gatewayClusterName,
			"-n", gatewayNamespace, "--type=merge", "-p", `{"spec":{"externalAccess":null}}`)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for the Gateway, its routes and the Services to go")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "gateway,tcproute,service", "-n", gatewayNamespace,
				"-l", resources.ExternalAccessLabel+"="+resources.ExternalAccessValue, "-o", "name")
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(strings.TrimSpace(out)).To(BeEmpty())
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("waiting for every instance to be announced at its pod address again")
		Eventually(func(g Gomega) {
			view, err := gated.leaderView()
			g.Expect(err).NotTo(HaveOccurred())
			for _, instance := range view {
				g.Expect(instance.boltServer).To(HaveSuffix(
					fmt.Sprintf(".%s.svc.cluster.local:%d", gatewayNamespace, memgraphcomv1alpha1.BoltPort)),
					"%s is still announced externally", instance.name)
			}
		}, 3*time.Minute, 5*time.Second).Should(Succeed())

		gated.awaitConverged(3 * time.Minute)
	})
})

// gatewayHost is the address the cluster's Gateway reports, read off its status
// as the operator reads it.
func (c clusterUnderTest) gatewayHost() string {
	GinkgoHelper()
	cmd := exec.Command("kubectl", "get", "gateway", c.name+"-gateway", "-n", c.namespace,
		"-o", "jsonpath={.status.addresses[0].value}")
	host, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(strings.TrimSpace(host)).NotTo(BeEmpty(), "Gateway %s-gateway has no address", c.name)
	return strings.TrimSpace(host)
}
