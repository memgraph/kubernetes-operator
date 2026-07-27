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

	// roleMain is the MAIN data-instance role reported in the SHOW INSTANCES role
	// column.
	roleMain = "main"
)

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
		Eventually(verifyClusterRegistered, 10*time.Minute, 10*time.Second).Should(Succeed())
	})

	// The operator's reason to exist over the chart's one-shot Job: a data
	// instance that loses its registration is re-registered with no human
	// action. This runs after the bootstrap spec (Ordered) against the same
	// converged cluster.
	It("re-registers a data instance whose registration was wiped", func() {
		const wiped = "instance_1"

		By("confirming the cluster is converged before wiping a registration")
		Eventually(verifyClusterRegistered, 10*time.Minute, 10*time.Second).Should(Succeed())

		By("unregistering a data instance on the coordinator leader")
		Expect(wipeInstanceRegistration(wiped)).To(Succeed())

		By("confirming the instance really left the cluster view")
		view, err := leaderView()
		Expect(err).NotTo(HaveOccurred())
		names := make([]string, 0, len(view))
		for _, instance := range view {
			names = append(names, instance.name)
		}
		Expect(names).NotTo(ContainElement(wiped),
			"the wipe must actually remove the registration for the test to be meaningful")

		By("waiting for the operator to converge the cluster back to fully registered")
		Eventually(verifyClusterRegistered, 10*time.Minute, 10*time.Second).Should(Succeed())
	})

	// The coordinator analogue of the data-instance re-registration: a
	// coordinator removed from the Raft cluster is re-added by the operator's
	// continuous ADD COORDINATOR reconciliation, with no human action. This
	// proves the re-registration loop covers coordinators, not just data
	// instances. Runs after the preceding specs (Ordered) against the same
	// converged cluster.
	It("re-adds a coordinator that was removed from the cluster", func() {
		By("confirming the cluster is converged before removing a coordinator")
		Eventually(verifyClusterRegistered, 10*time.Minute, 10*time.Second).Should(Succeed())

		By("removing a follower coordinator on the coordinator leader")
		removed, err := removeCoordinatorRegistration()
		Expect(err).NotTo(HaveOccurred())

		By("confirming the coordinator really left the cluster view")
		view, err := leaderView()
		Expect(err).NotTo(HaveOccurred())
		names := make([]string, 0, len(view))
		for _, instance := range view {
			names = append(names, instance.name)
		}
		Expect(names).NotTo(ContainElement(removed),
			"the removal must actually drop the coordinator for the test to be meaningful")

		By("waiting for the operator to converge the cluster back to fully registered")
		Eventually(verifyClusterRegistered, 10*time.Minute, 10*time.Second).Should(Succeed())
	})

	// Storage survives the cluster under the default retention policy: an
	// accidental `kubectl delete mgc` must not take a production database with
	// it. This deletes the CR, so it runs last in this Ordered container.
	It("leaves the PVCs behind when the default-retention CR is deleted", func() {
		By("confirming the cluster is converged before deleting it")
		Eventually(verifyClusterRegistered, 10*time.Minute, 10*time.Second).Should(Succeed())

		By("recording the provisioned PVCs")
		before, err := listPVCs(clusterNamespace)
		Expect(err).NotTo(HaveOccurred())
		// Two claims (lib and log) per coordinator and data instance pod.
		Expect(before).To(HaveLen(2 * (coordinatorCount + dataInstanceCount)))

		By("deleting the MemgraphCluster")
		cmd := exec.Command("kubectl", "delete", "memgraphcluster", clusterName,
			"-n", clusterNamespace, "--wait=true")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete the MemgraphCluster")

		By("waiting for garbage collection to remove the workloads")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "statefulsets", "-n", clusterNamespace,
				"-o", "jsonpath={.items[*].metadata.name}")
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(strings.TrimSpace(output)).To(BeEmpty())
		}, 5*time.Minute, 5*time.Second).Should(Succeed())

		By("confirming every PVC is still there")
		// Consistently, not Eventually: the failure mode is a delayed deletion,
		// which a single post-condition check would race straight past.
		Consistently(func(g Gomega) {
			after, err := listPVCs(clusterNamespace)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(after).To(ConsistOf(before))
		}, 30*time.Second, 5*time.Second).Should(Succeed())
	})
})

// The Delete retention policy is the dev-cluster counterpart of the spec
// above: the StatefulSet machinery takes the claims down with the CR. It gets
// its own container and namespace because it needs a differently-configured
// cluster, and it never waits for registration to converge — the StatefulSet
// controller provisions the claims as soon as the pods are created, so the
// retention behavior is observable long before Memgraph is.
var _ = Describe("MemgraphCluster with Delete storage retention", Ordered, func() {
	const retentionNamespace = "memgraph-e2e-retention"
	const retentionCluster = "retention"

	BeforeAll(func() {
		By("creating the cluster namespace")
		cmd := exec.Command("kubectl", "create", "ns", retentionNamespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")
	})

	AfterAll(func() {
		By("removing the cluster namespace")
		cmd := exec.Command("kubectl", "delete", "ns", retentionNamespace,
			"--ignore-not-found", "--wait=false")
		_, _ = utils.Run(cmd)
	})

	It("removes the PVCs when the CR is deleted", func() {
		By("applying a MemgraphCluster with Delete retention")
		manifest := fmt.Sprintf(`apiVersion: memgraph.com/v1alpha1
kind: MemgraphCluster
metadata:
  name: %s
  namespace: %s
spec:
  coordinators: 1
  dataInstances: 1
  image:
    repository: %s
    tag: %s
  storage:
    retentionPolicy: Delete
`, retentionCluster, retentionNamespace, memgraphImageRepository, memgraphImageTag)
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		_, err := utils.RunWithInput(cmd, manifest)
		Expect(err).NotTo(HaveOccurred(), "Failed to apply the MemgraphCluster")

		By("waiting for the claims to be provisioned")
		// One lib and one log claim for the single coordinator and the single
		// data instance.
		Eventually(func(g Gomega) {
			claims, err := listPVCs(retentionNamespace)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(claims).To(HaveLen(4))
		}, 5*time.Minute, 5*time.Second).Should(Succeed())

		By("deleting the MemgraphCluster")
		cmd = exec.Command("kubectl", "delete", "memgraphcluster", retentionCluster,
			"-n", retentionNamespace, "--wait=true")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete the MemgraphCluster")

		By("waiting for the StatefulSet machinery to take the claims down with it")
		Eventually(func(g Gomega) {
			claims, err := listPVCs(retentionNamespace)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(claims).To(BeEmpty())
		}, 5*time.Minute, 5*time.Second).Should(Succeed())
	})
})

// listPVCs returns the names of the PersistentVolumeClaims in a namespace,
// excluding any already marked for deletion — a claim with a deletion
// timestamp is gone as far as the retention contract is concerned, even while
// a finalizer keeps the object around.
func listPVCs(namespace string) ([]string, error) {
	cmd := exec.Command("kubectl", "get", "pvc", "-n", namespace, "-o",
		`jsonpath={range .items[*]}{.metadata.name}{"\t"}{.metadata.deletionTimestamp}{"\n"}{end}`)
	output, err := utils.Run(cmd)
	if err != nil {
		return nil, err
	}

	// A claim with no deletion timestamp yields a line of "<name>\t"; the
	// separator is always emitted, so the split is total.
	names := []string{}
	for _, line := range utils.GetNonEmptyLines(output) {
		name, deletionTimestamp, found := strings.Cut(line, "\t")
		if !found {
			return nil, fmt.Errorf("unexpected kubectl get pvc output line: %q", line)
		}
		if strings.TrimSpace(deletionTimestamp) != "" {
			continue
		}
		names = append(names, strings.TrimSpace(name))
	}
	return names, nil
}

// verifyClusterRegistered asserts the coordinator leader reports every declared
// instance registered and healthy with exactly one MAIN — the converged steady
// state both the bootstrap and re-registration specs check for.
func verifyClusterRegistered(g Gomega) {
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

// wipeInstanceRegistration unregisters the named data instance on the
// coordinator leader, simulating registration state a pod loses when it is
// rescheduled onto a fresh node. UNREGISTER INSTANCE must run on the leader —
// only it holds the authoritative cluster view — so the leader is located the
// same way leaderView does: the coordinator that reports a MAIN.
func wipeInstanceRegistration(name string) error {
	var errs []error
	for ordinal := range coordinatorCount {
		pod := fmt.Sprintf("%s-coordinator-%d", clusterName, ordinal)
		view, err := showInstances(pod)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		isLeader := false
		for _, instance := range view {
			if instance.role == roleMain {
				isLeader = true
				break
			}
		}
		if !isLeader {
			continue
		}
		cmd := exec.Command("kubectl", "exec", pod, "-n", clusterNamespace, "-c", "memgraph", "--",
			"bash", "-c", fmt.Sprintf("echo 'UNREGISTER INSTANCE %s;' | mgconsole", name))
		if _, err := utils.Run(cmd); err != nil {
			return fmt.Errorf("unregistering %s on %s: %w", name, pod, err)
		}
		return nil
	}
	return fmt.Errorf("no coordinator leader found to unregister %s: %w", name, errors.Join(errs...))
}

// removeCoordinatorRegistration removes a follower coordinator from the Raft
// cluster on the coordinator leader, simulating a coordinator that fell out of
// the cluster view (e.g. rescheduled onto a fresh node). REMOVE COORDINATOR
// mutates Raft membership, so it must run on the leader — located the same way
// leaderView does: the coordinator that reports a MAIN. A follower is chosen
// (never the leader itself) so the leader keeps the authoritative view it needs
// to accept the removal and observe the operator's re-ADD. It returns the
// instance name of the coordinator it removed.
func removeCoordinatorRegistration() (string, error) {
	var errs []error
	for ordinal := range coordinatorCount {
		pod := fmt.Sprintf("%s-coordinator-%d", clusterName, ordinal)
		view, err := showInstances(pod)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		isLeader := false
		for _, instance := range view {
			if instance.role == roleMain {
				isLeader = true
				break
			}
		}
		if !isLeader {
			continue
		}
		// The leader hosts coordinator_ordinal+1; remove a different
		// coordinator so the leader keeps quorum and its authoritative view.
		leaderID := ordinal + 1
		removeID := 1
		if leaderID == 1 {
			removeID = 2
		}
		name := fmt.Sprintf("coordinator_%d", removeID)
		cmd := exec.Command("kubectl", "exec", pod, "-n", clusterNamespace, "-c", "memgraph", "--",
			"bash", "-c", fmt.Sprintf("echo 'REMOVE COORDINATOR %d;' | mgconsole", removeID))
		if _, err := utils.Run(cmd); err != nil {
			return "", fmt.Errorf("removing coordinator %d on %s: %w", removeID, pod, err)
		}
		return name, nil
	}
	return "", fmt.Errorf("no coordinator leader found to remove a coordinator: %w", errors.Join(errs...))
}

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
