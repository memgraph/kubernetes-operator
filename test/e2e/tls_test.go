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

// The TLS scenario proves what the two blocks promise and the one thing each
// must survive. It boots a cluster with intra-cluster TLS from the start —
// the mode cannot be added later, and admission saying so is checked here
// too — exposed through LoadBalancers and asking for a ServiceMonitor, with
// Bolt in plaintext. That it bootstraps at all proves the members reach each
// other over mutual TLS: the coordinator leader health-checks every instance
// over the management RPC and the replicas replicate from MAIN. Then
// spec.tls.bolt is added and the ordered roll watched: the roll is the hard
// part for Bolt, because the coordinators are restarted last and the operator
// has to keep reaching them in whichever mode each still speaks, and it is
// the roll intra-cluster TLS has to carry, because every step waits on
// replication over the TLS channel. Afterwards a TLS routing driver on the
// host writes and reads through the coordinators' external address, a
// plaintext driver is refused, the ServiceMonitor names https, and the metrics
// port answers over TLS. Both certificates are minted here from one
// throwaway CA, with the pod-DNS wildcards a verifying client would need; no
// cert-manager runs.
var _ = Describe("MemgraphCluster serving TLS", Ordered, func() {
	const tlsNamespace = "memgraph-e2e-tls"
	const tlsClusterName = "secured"
	const tlsSecretName = "bolt-tls"
	const intraSecretName = "intra-cluster-tls"

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

		By("minting a CA and one certificate per mode covering both roles' pod DNS names")
		ca := mintCA()
		dnsNames := []string{
			fmt.Sprintf("*.%s-coordinator.%s.svc.cluster.local", tlsClusterName, tlsNamespace),
			fmt.Sprintf("*.%s-data.%s.svc.cluster.local", tlsClusterName, tlsNamespace),
		}
		boltCert, boltKey := ca.mintLeaf(dnsNames, x509.ExtKeyUsageServerAuth)
		createTLSSecret(tlsNamespace, tlsSecretName, boltCert, boltKey, "")
		// The intra-cluster leaf is presented as both server and client on
		// every member-to-member connection.
		intraCert, intraKey := ca.mintLeaf(dnsNames, x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth)
		createTLSSecret(tlsNamespace, intraSecretName, intraCert, intraKey, ca.certPEM)

		By("applying a MemgraphCluster with intra-cluster TLS, plaintext Bolt, LoadBalancers and a ServiceMonitor")
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
  tls:
    intraCluster:
      secretName: %s
`, tlsClusterName, tlsNamespace, plain.coordinators, plain.dataInstances,
			example.Spec.Image.Repository, example.Spec.Image.Tag, licenseSecretName, intraSecretName)
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

	It("bootstraps over intra-cluster TLS with plaintext Bolt and converges", func() {
		Eventually(plain.verifyRegistered, 10*time.Minute, 10*time.Second).Should(Succeed())
		plain.awaitConverged(5 * time.Minute)
	})

	It("refuses to drop intra-cluster TLS from the live cluster", func() {
		cmd := exec.Command("kubectl", "patch", "memgraphcluster", tlsClusterName,
			"-n", tlsNamespace, "--type=merge", "-p", `{"spec":{"tls":null}}`)
		out, err := utils.Run(cmd)
		Expect(err).To(HaveOccurred(), "admission must refuse removing intraCluster on a live cluster")
		Expect(out).To(ContainSubstring("tls.intraCluster cannot be added or removed on a live cluster"))
	})

	// Adding the block is a pod-template change and rolls the cluster. The
	// order is the same as for any other change; what is specific to TLS is
	// that for the whole data-instance phase every coordinator still speaks
	// plaintext while the spec says TLS, and the roll only completes if the
	// operator keeps reaching them regardless.
	It("rolls Bolt TLS on through the data instances before the coordinators", func() {
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

		By("confirming the cluster converges with every member registered, reached over TLS")
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

// testCA is a throwaway CA the scenario mints both leaves from, the way one
// cert-manager issuer would. Only the intra-cluster Secret carries its
// certificate: that is the one place a member verifies a peer. Nothing on the
// Bolt side verifies, which is exactly the operator's and the ServiceMonitor's
// contract, and a client that does only needs the CA on its own side.
type testCA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM string
}

func mintCA() testCA {
	GinkgoHelper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	Expect(err).NotTo(HaveOccurred())
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "memgraph e2e CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	Expect(err).NotTo(HaveOccurred())
	cert, err := x509.ParseCertificate(der)
	Expect(err).NotTo(HaveOccurred())
	return testCA{
		cert:    cert,
		key:     key,
		certPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
	}
}

// mintLeaf returns a PEM certificate and key signed by the CA, covering the
// given DNS names and usable for the given purposes.
func (ca testCA) mintLeaf(dnsNames []string, usages ...x509.ExtKeyUsage) (certPEM, keyPEM string) {
	GinkgoHelper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	Expect(err).NotTo(HaveOccurred())
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: dnsNames[0]},
		DNSNames:     dnsNames,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  usages,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	Expect(err).NotTo(HaveOccurred())
	keyDER, err := x509.MarshalECPrivateKey(key)
	Expect(err).NotTo(HaveOccurred())
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	return certPEM, keyPEM
}

// createTLSSecret applies a kubernetes.io/tls Secret, the shape both blocks
// document, with ca.crt beside the pair when given — what a cert-manager
// Certificate from a private CA issuer writes. The manifest is piped over
// stdin so the key never reaches the logged command line.
func createTLSSecret(namespace, name, certPEM, keyPEM, caPEM string) {
	GinkgoHelper()
	data := map[string]string{"tls.crt": certPEM, "tls.key": keyPEM}
	if caPEM != "" {
		data["ca.crt"] = caPEM
	}
	secret := map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"type":       "kubernetes.io/tls",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
		"stringData": data,
	}
	manifest, err := json.Marshal(secret)
	Expect(err).NotTo(HaveOccurred(), "Failed to marshal the TLS Secret")

	cmd := exec.Command("kubectl", "apply", "-f", "-")
	_, err = utils.RunWithInput(cmd, string(manifest))
	Expect(err).NotTo(HaveOccurred(), "Failed to apply the TLS Secret")
}

// curlInsecure fetches a URL from inside the cluster through a throwaway curl
// pod that skips certificate verification, and returns the body. The pod runs
// under the restricted Pod Security Standard the namespace enforces. The body
// is read from the pod's logs once it has finished rather than through an
// attached kubectl run: curl is done in well under a second, and an attach
// that arrives after the container exited sees nothing.
func curlInsecure(namespace, url string) (string, error) {
	const pod = "curl-insecure"
	overrides := `{"spec":{"containers":[{"name":"curl","image":"curlimages/curl:latest",` +
		`"command":["curl","-sSk","--max-time","30","` + url + `"],` +
		`"securityContext":{"readOnlyRootFilesystem":true,"allowPrivilegeEscalation":false,` +
		`"capabilities":{"drop":["ALL"]},"runAsNonRoot":true,"runAsUser":1000,` +
		`"seccompProfile":{"type":"RuntimeDefault"}}}]}}`
	deletePod := func() {
		cmd := exec.Command("kubectl", "delete", "pod", pod, "--namespace", namespace,
			"--ignore-not-found", "--wait=true")
		_, _ = utils.Run(cmd)
	}
	deletePod()
	defer deletePod()

	cmd := exec.Command("kubectl", "run", pod, "--restart=Never", "--namespace", namespace,
		"--image=curlimages/curl:latest", "--overrides", overrides)
	if _, err := utils.Run(cmd); err != nil {
		return "", err
	}
	cmd = exec.Command("kubectl", "wait", "pod/"+pod, "--namespace", namespace,
		"--for=jsonpath={.status.phase}=Succeeded", "--timeout=2m")
	if _, err := utils.Run(cmd); err != nil {
		return "", err
	}
	cmd = exec.Command("kubectl", "logs", pod, "--namespace", namespace)
	return utils.Run(cmd)
}
