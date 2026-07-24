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
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/memgraph/kubernetes-operator/test/utils"
)

// The declared topology of the e2e cluster and the identities the operator
// derives from it: coordinator ordinal N registers as coordinator_N+1, data
// ordinal N as instance_N.
const (
	clusterNamespace = "memgraph-e2e"
	clusterName      = "memgraph"

	coordinatorCount  = 3
	dataInstanceCount = 2

	memgraphImageRepository = "docker.io/memgraph/memgraph"
	memgraphImageTag        = "3.12.0"

	// licenseSecretName and the env var names below follow the HA Helm chart's
	// CI convention: repository secrets of the same names are exported into the
	// job environment and materialize as one Kubernetes Secret the CR
	// references.
	licenseSecretName  = "memgraph-secrets"
	licenseEnvVar      = "MEMGRAPH_ENTERPRISE_LICENSE"
	organizationEnvVar = "MEMGRAPH_ORGANIZATION_NAME"
)

// roleMain is the MAIN data-instance role reported in the SHOW INSTANCES role
// column.
const roleMain = "main"

// declaredInstances returns the instance names every coordinator and data
// instance must appear under in SHOW INSTANCES once the operator has converged
// registration.
func declaredInstances() []string {
	names := make([]string, 0, coordinatorCount+dataInstanceCount)
	for ordinal := range coordinatorCount {
		names = append(names, fmt.Sprintf("coordinator_%d", ordinal+1))
	}
	for ordinal := range dataInstanceCount {
		names = append(names, fmt.Sprintf("instance_%d", ordinal))
	}
	return names
}

// MemgraphCluster is the end-to-end proof of the provisioning and bootstrap
// slices: a real multi-node Kind cluster, the deployed operator, real licensed
// Memgraph images, and assertions over SHOW INSTANCES.
//
// The container owns one cluster for all its specs: BeforeAll provisions the
// namespace, license Secret, and CR, so a new scenario against the same
// cluster is just another It block (with later Its observing earlier
// mutations, e.g. a deliberately wiped pod). A scenario needing a
// differently-shaped cluster gets its own Ordered container following this
// same pattern — no pipeline changes.
var _ = Describe("MemgraphCluster", Ordered, func() {
	BeforeAll(func() {
		license := os.Getenv(licenseEnvVar)
		organization := os.Getenv(organizationEnvVar)
		Expect(license).NotTo(BeEmpty(),
			"%s must be set: the e2e suite boots a licensed Memgraph HA cluster", licenseEnvVar)
		Expect(organization).NotTo(BeEmpty(),
			"%s must be set: the e2e suite boots a licensed Memgraph HA cluster", organizationEnvVar)

		By("preloading the Memgraph image into the Kind cluster")
		memgraphImage := memgraphImageRepository + ":" + memgraphImageTag
		cmd := exec.Command("docker", "pull", memgraphImage)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to pull the Memgraph image")
		Expect(utils.LoadImageToKindClusterWithName(memgraphImage)).To(Succeed(),
			"Failed to load the Memgraph image into Kind")

		By("creating the cluster namespace")
		cmd = exec.Command("kubectl", "create", "ns", clusterNamespace)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", clusterNamespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("creating the enterprise license Secret")
		createLicenseSecret(license, organization)

		By("applying the MemgraphCluster")
		applyMemgraphCluster()
	})

	AfterAll(func() {
		By("removing the cluster namespace")
		cmd := exec.Command("kubectl", "delete", "ns", clusterNamespace,
			"--ignore-not-found", "--wait=false")
		_, _ = utils.Run(cmd)
	})

	// On failure, dump everything needed to debug a broken bootstrap from CI
	// logs alone.
	AfterEach(func() {
		if !CurrentSpecReport().Failed() {
			return
		}
		for _, args := range [][]string{
			{"get", "pods", "-n", clusterNamespace, "-o", "wide"},
			{"get", "memgraphclusters", "-n", clusterNamespace, "-o", "yaml"},
			{"get", "events", "-n", clusterNamespace, "--sort-by=.lastTimestamp"},
			{"logs", "deploy/kubernetes-operator-controller-manager", "-n", namespace},
		} {
			cmd := exec.Command("kubectl", args...)
			output, err := utils.Run(cmd)
			if err != nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to collect diagnostics %v: %s\n", args, err)
				continue
			}
			_, _ = fmt.Fprintf(GinkgoWriter, "Diagnostics kubectl %v:\n%s\n", args, output)
		}
	})

	It("bootstraps every declared instance registered with exactly one MAIN", func() {
		verifyClusterRegistered := func(g Gomega) {
			view, err := leaderView()
			g.Expect(err).NotTo(HaveOccurred())

			names := make([]string, 0, len(view))
			mains := make([]string, 0, 1)
			for _, instance := range view {
				names = append(names, instance.name)
				g.Expect(instance.health).To(Equal("up"),
					"instance %s is registered but unhealthy", instance.name)
				if instance.role == roleMain {
					mains = append(mains, instance.name)
				}
			}
			g.Expect(names).To(ConsistOf(declaredInstances()))
			g.Expect(mains).To(HaveLen(1), "expected exactly one MAIN, got %v", mains)
		}
		Eventually(verifyClusterRegistered, 10*time.Minute, 10*time.Second).Should(Succeed())
	})
})

// createLicenseSecret applies the Secret the MemgraphCluster references. The
// manifest is piped over stdin so no secret material ever reaches the logged
// command line.
func createLicenseSecret(license, organization string) {
	secret := map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      licenseSecretName,
			"namespace": clusterNamespace,
		},
		"stringData": map[string]string{
			licenseEnvVar:      license,
			organizationEnvVar: organization,
		},
	}
	manifest, err := json.Marshal(secret)
	Expect(err).NotTo(HaveOccurred(), "Failed to marshal the license Secret")

	cmd := exec.Command("kubectl", "apply", "-f", "-")
	_, err = utils.RunWithInput(cmd, string(manifest))
	Expect(err).NotTo(HaveOccurred(), "Failed to apply the license Secret")
}

// applyMemgraphCluster applies the CR under test: the minimal spec of the PRD's
// first-contact story — image, counts, and a license secret reference.
func applyMemgraphCluster() {
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
    licenseKey: %s
    organizationKey: %s
`, clusterName, clusterNamespace, coordinatorCount, dataInstanceCount,
		memgraphImageRepository, memgraphImageTag,
		licenseSecretName, licenseEnvVar, organizationEnvVar)

	cmd := exec.Command("kubectl", "apply", "-f", "-")
	_, err := utils.RunWithInput(cmd, manifest)
	Expect(err).NotTo(HaveOccurred(), "Failed to apply the MemgraphCluster")
}

// instanceRow is one parsed row of SHOW INSTANCES.
type instanceRow struct {
	name   string
	health string
	role   string
}

// leaderView returns the SHOW INSTANCES view of the first coordinator that
// reports a MAIN. Only the coordinator leader health-checks data instances and
// reports their roles (followers show them as unknown), so a view containing a
// MAIN is the leader's authoritative view.
func leaderView() ([]instanceRow, error) {
	var errs []error
	for ordinal := range coordinatorCount {
		pod := fmt.Sprintf("%s-coordinator-%d", clusterName, ordinal)
		view, err := showInstances(pod)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, instance := range view {
			if instance.role == roleMain {
				return view, nil
			}
		}
		errs = append(errs, fmt.Errorf("%s reports no MAIN among %d instances", pod, len(view)))
	}
	return nil, errors.Join(errs...)
}

// showInstances runs SHOW INSTANCES through mgconsole inside the given
// coordinator pod (the Memgraph image ships the client) and parses the CSV
// output.
func showInstances(pod string) ([]instanceRow, error) {
	cmd := exec.Command("kubectl", "exec", pod, "-n", clusterNamespace, "-c", "memgraph", "--",
		"bash", "-c", "echo 'SHOW INSTANCES;' | mgconsole --output-format=csv")
	output, err := utils.Run(cmd)
	if err != nil {
		return nil, err
	}
	return parseInstances(output)
}

// parseInstances parses mgconsole CSV output into rows keyed by the header
// columns, so the assertion survives added or reordered columns across
// Memgraph versions.
func parseInstances(output string) ([]instanceRow, error) {
	lines := utils.GetNonEmptyLines(output)
	header := -1
	for i, line := range lines {
		if strings.Contains(line, "name") && strings.Contains(line, "bolt_server") {
			header = i
			break
		}
	}
	if header == -1 {
		return nil, fmt.Errorf("no SHOW INSTANCES header in mgconsole output: %q", output)
	}

	reader := csv.NewReader(strings.NewReader(strings.Join(lines[header:], "\n")))
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parsing mgconsole CSV output: %w", err)
	}

	columns := map[string]int{}
	for i, column := range records[0] {
		columns[strings.TrimSpace(column)] = i
	}
	for _, column := range []string{"name", "health", "role"} {
		if _, ok := columns[column]; !ok {
			return nil, fmt.Errorf("SHOW INSTANCES output has no %q column: %q", column, records[0])
		}
	}

	instances := make([]instanceRow, 0, len(records)-1)
	for _, record := range records[1:] {
		instances = append(instances, instanceRow{
			name:   unquoteCell(record[columns["name"]]),
			health: unquoteCell(record[columns["health"]]),
			role:   unquoteCell(record[columns["role"]]),
		})
	}
	return instances, nil
}

// unquoteCell strips the residual double quotes mgconsole wraps around string
// cells in CSV output. mgconsole emits string values already double-quoted, so
// after the CSV reader unwraps its own layer a value like main still arrives as
// "main"; the operator and assertions compare against the bare token.
func unquoteCell(cell string) string {
	return strings.Trim(strings.TrimSpace(cell), `"`)
}
