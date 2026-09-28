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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os/exec"
	"strings"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
	"github.com/memgraph/kubernetes-operator/test/utils"
)

// The Bolt TLS scenario proves the two things the block promises and the one
// thing it must survive. It boots a cluster in plaintext, exposed through
// LoadBalancers and asking for a ServiceMonitor, then adds spec.tls.bolt and
// watches the ordered roll complete — the roll is the hard part, because the
// coordinators are restarted last and the operator has to keep reaching them
// in whichever mode each still speaks. Afterwards a TLS routing driver on the
// host writes and reads through the coordinators' external address, a
// plaintext driver is refused, the ServiceMonitor names https, and the metrics
// port answers over TLS. The certificate is minted here, with the pod-DNS
// wildcards a verifying in-cluster client would need; no cert-manager runs.
var _ = Describe("MemgraphCluster serving Bolt over TLS", Ordered, func() {
	const tlsNamespace = "memgraph-e2e-tls"
	const tlsClusterName = "secured"
	const tlsSecretName = "bolt-tls"

	plain := clusterUnderTest{
		namespace: tlsNamespace, name: tlsClusterName, coordinators: 3, dataInstances: 2,
	}
	secured := plain
	secured.tls = true

	BeforeAll(func() {
		license, organization := licenseFromEnv()

		preloadMemgraphImage()
		createClusterNamespace(tlsNamespace)

		By("creating the enterprise license Secret")
		createLicenseSecret(tlsNamespace, license, organization)

		By("minting a CA and a Bolt certificate covering both roles' pod DNS names")
		cert, key := mintBoltCertificate(
			fmt.Sprintf("*.%s-coordinator.%s.svc.cluster.local", tlsClusterName, tlsNamespace),
			fmt.Sprintf("*.%s-data.%s.svc.cluster.local", tlsClusterName, tlsNamespace),
		)
		createTLSSecret(tlsNamespace, tlsSecretName, cert, key)

		By("applying a plaintext MemgraphCluster exposed through LoadBalancers with a ServiceMonitor")
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
  monitoring:
    serviceMonitor: {}
`, tlsClusterName, tlsNamespace, plain.coordinators, plain.dataInstances,
			example.Spec.Image.Repository, example.Spec.Image.Tag, licenseSecretName)
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		_, err := utils.RunWithInput(cmd, manifest)
		Expect(err).NotTo(HaveOccurred(), "Failed to apply the MemgraphCluster")
	})

	AfterAll(func() {
		By("removing the cluster namespace and waiting for its pods to go")
		cmd := exec.Command("kubectl", "delete", "ns", tlsNamespace,
			"--ignore-not-found", "--wait=true", "--timeout=10m")
		_, _ = utils.Run(cmd)
	})

	AfterEach(func() {
		dumpDiagnosticsOnFailure(tlsNamespace)
	})

	It("bootstraps in plaintext and converges", func() {
		Eventually(plain.verifyRegistered, 10*time.Minute, 10*time.Second).Should(Succeed())
		plain.awaitConverged(5 * time.Minute)
	})

	// Adding the block is a pod-template change and rolls the cluster. The
	// order is the same as for any other change; what is specific to TLS is
	// that for the whole data-instance phase every coordinator still speaks
	// plaintext while the spec says TLS, and the roll only completes if the
	// operator keeps reaching them regardless.
	It("rolls TLS on through the data instances before the coordinators", func() {
		By("recording which pods exist")
		before, err := plain.podUIDs()
		Expect(err).NotTo(HaveOccurred())
		Expect(before).To(HaveLen(int(plain.coordinators + plain.dataInstances)))

		By("adding the tls.bolt block")
		cmd := exec.Command("kubectl", "patch", "memgraphcluster", tlsClusterName,
			"-n", tlsNamespace, "--type=merge", "-p",
			fmt.Sprintf(`{"spec":{"tls":{"bolt":{"secretName":"%s"}}}}`, tlsSecretName))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "the operator must accept the tls.bolt block")

		By("watching the operator replace every pod, one at a time")
		order, maxDown := plain.watchRoll(before, 25*time.Minute)
		Expect(order).To(HaveLen(len(before)), "every pod must be replaced exactly once")
		Expect(maxDown).To(BeNumerically("<=", 1),
			"at most one pod of the cluster may be unready at a time; observed %d", maxDown)
		for i, pod := range order {
			if strings.Contains(pod, "-coordinator-") {
				Expect(i).To(BeNumerically(">=", int(plain.dataInstances)),
					"a coordinator pod (%s) was restarted before the data plane finished: %v", pod, order)
			}
		}

		By("confirming the cluster converges over TLS with every member registered")
		Eventually(secured.verifyRegistered, 10*time.Minute, 10*time.Second).Should(Succeed())
		cmd = exec.Command("kubectl", "wait", "--for=condition=Updated",
			"memgraphcluster/"+tlsClusterName, "-n", tlsNamespace, "--timeout=5m")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "the MemgraphCluster never reported Updated")
		secured.awaitConverged(5 * time.Minute)
	})

	It("serves a TLS client through the routing table and refuses a plaintext one", func() {
		ctx := context.Background()
		status := secured.externalAccessStatus()
		Expect(status.Coordinators).NotTo(BeEmpty(), "the coordinators' LoadBalancer must have an address")

		By("connecting with a TLS routing driver that does not verify the certificate")
		driver, err := neo4j.NewDriverWithContext("neo4j+ssc://"+status.Coordinators, neo4j.NoAuth())
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = driver.Close(ctx) }()
		Expect(driver.VerifyConnectivity(ctx)).To(Succeed(),
			"the coordinators' LoadBalancer must serve Bolt over TLS")

		By("writing through the routing table, which sends the write to MAIN")
		write := driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
		_, err = write.Run(ctx, "CREATE (:E2E {source: 'tls'})", nil)
		Expect(err).NotTo(HaveOccurred(), "a write over TLS must succeed")
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
		}, 2*time.Minute, 5*time.Second).Should(Succeed(), "the write must be readable over TLS through a replica")

		By("confirming a plaintext driver is refused")
		plaintext, err := neo4j.NewDriverWithContext("neo4j://"+status.Coordinators, neo4j.NoAuth())
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = plaintext.Close(ctx) }()
		Expect(plaintext.VerifyConnectivity(ctx)).NotTo(Succeed(),
			"a cluster serving Bolt over TLS must not accept a plaintext handshake")
	})

	// Memgraph serves metrics from the Bolt server context, so the block moves
	// 9091 to https too. The ServiceMonitor has to follow, and the port has to
	// answer: both are read here, the second through a throwaway curl pod
	// because the Memgraph image ships no TLS-capable client.
	It("moves the metrics endpoint and the ServiceMonitor to https", func() {
		By("reading the ServiceMonitor's endpoint")
		cmd := exec.Command("kubectl", "get", "servicemonitor", tlsClusterName, "-n", tlsNamespace,
			"-o", "jsonpath={.spec.endpoints[0].scheme} {.spec.endpoints[0].tlsConfig.insecureSkipVerify}")
		out, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(out)).To(Equal(string(monitoringv1.SchemeHTTPS) + " true"))

		By("fetching /metrics over TLS from a data pod")
		url := fmt.Sprintf("https://%s.%s-data.%s.svc.cluster.local:%d/metrics",
			secured.dataPod(0), tlsClusterName, tlsNamespace, memgraphcomv1alpha1.MetricsPort)
		body, err := curlInsecure(tlsNamespace, url)
		Expect(err).NotTo(HaveOccurred(), "curl over TLS to %s", url)
		Expect(body).To(ContainSubstring("# TYPE "), "the metrics port serves OpenMetrics over TLS")
	})
})

// mintBoltCertificate returns a PEM certificate and key for a throwaway CA's
// leaf covering the given DNS names, the shape a cert-manager Certificate would
// produce. The CA itself is discarded: nothing in the scenario verifies, which
// is exactly the operator's and the ServiceMonitor's contract, and a client
// that does verify only needs the CA on its own side.
func mintBoltCertificate(dnsNames ...string) (certPEM, keyPEM string) {
	GinkgoHelper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	Expect(err).NotTo(HaveOccurred())
	ca := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "memgraph e2e CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	Expect(err).NotTo(HaveOccurred())
	caCert, err := x509.ParseCertificate(caDER)
	Expect(err).NotTo(HaveOccurred())

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	Expect(err).NotTo(HaveOccurred())
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: dnsNames[0]},
		DNSNames:     dnsNames,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, caCert, &leafKey.PublicKey, caKey)
	Expect(err).NotTo(HaveOccurred())
	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	Expect(err).NotTo(HaveOccurred())

	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	return certPEM, keyPEM
}

// createTLSSecret applies a kubernetes.io/tls Secret, the shape the block
// documents. The manifest is piped over stdin so the key never reaches the
// logged command line.
func createTLSSecret(namespace, name, certPEM, keyPEM string) {
	GinkgoHelper()
	secret := map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"type":       "kubernetes.io/tls",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
		"stringData": map[string]string{"tls.crt": certPEM, "tls.key": keyPEM},
	}
	manifest, err := json.Marshal(secret)
	Expect(err).NotTo(HaveOccurred(), "Failed to marshal the TLS Secret")

	cmd := exec.Command("kubectl", "apply", "-f", "-")
	_, err = utils.RunWithInput(cmd, string(manifest))
	Expect(err).NotTo(HaveOccurred(), "Failed to apply the TLS Secret")
}

// curlInsecure fetches a URL from inside the cluster through a throwaway curl
// pod that skips certificate verification, and returns the body. The pod runs
// under the restricted Pod Security Standard the namespace enforces.
func curlInsecure(namespace, url string) (string, error) {
	overrides := `{"spec":{"containers":[{"name":"curl","image":"curlimages/curl:latest",` +
		`"command":["curl","-sSk","--max-time","30","` + url + `"],` +
		`"securityContext":{"readOnlyRootFilesystem":true,"allowPrivilegeEscalation":false,` +
		`"capabilities":{"drop":["ALL"]},"runAsNonRoot":true,"runAsUser":1000,` +
		`"seccompProfile":{"type":"RuntimeDefault"}}}]}}`
	cmd := exec.Command("kubectl", "run", "curl-tls-metrics", "--rm", "-i", "--restart=Never",
		"--namespace", namespace, "--image=curlimages/curl:latest", "--overrides", overrides)
	return utils.Run(cmd)
}
