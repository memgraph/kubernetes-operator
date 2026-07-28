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
	"path/filepath"
	"slices"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
	"github.com/memgraph/kubernetes-operator/test/utils"
)

const (
	clusterNamespace = "memgraph-e2e"

	// exampleManifest is the quickstart manifest the README walks a newcomer
	// through, applied here verbatim (only the namespace is supplied on the
	// command line). Everything the suite needs to know about the cluster —
	// its name, its topology, its image — is read out of the file below, so
	// the example cannot drift from what CI proves works.
	exampleManifest = "examples/minimal-cluster.yaml"

	// The env vars carrying the enterprise license into the suite follow the
	// HA Helm chart's CI convention: repository secrets of these names are
	// exported into the job environment and materialize as the one Kubernetes
	// Secret the example references.
	licenseEnvVar      = "MEMGRAPH_ENTERPRISE_LICENSE"
	organizationEnvVar = "MEMGRAPH_ORGANIZATION_NAME"

	// roleMain is the MAIN data-instance role reported in the SHOW INSTANCES role
	// column.
	roleMain = "main"
)

// example is the parsed quickstart manifest and the source of truth for the
// topology the specs assert on.
var example = loadExample()

// The declared topology of the e2e cluster, as the example declares it.
var (
	clusterName       = example.Name
	coordinatorCount  = declaredCount("coordinators", example.Spec.Coordinators)
	dataInstanceCount = declaredCount("dataInstances", example.Spec.DataInstances)

	memgraphImage = example.Spec.Image.Repository + ":" + example.Spec.Image.Tag

	licenseSecretName = example.Spec.Secrets.Name

	// quickstartCluster is the cluster the example manifest boots, which most of
	// the specs below observe.
	quickstartCluster = clusterUnderTest{
		namespace:     clusterNamespace,
		name:          clusterName,
		coordinators:  coordinatorCount,
		dataInstances: dataInstanceCount,
	}
)

// clusterUnderTest is one MemgraphCluster a spec observes, together with the
// topology it declares. The suite runs several differently-shaped clusters —
// the quickstart one, the retention one, the one that is scaled — so every
// helper below takes its cluster rather than reaching for the quickstart
// globals.
type clusterUnderTest struct {
	namespace     string
	name          string
	coordinators  int32
	dataInstances int32
}

// withTopology returns the same cluster with a different declared topology,
// which is what the assertions switch to after a scale in either direction.
func (c clusterUnderTest) withTopology(coordinators, dataInstances int32) clusterUnderTest {
	c.coordinators = coordinators
	c.dataInstances = dataInstances
	return c
}

// declaredCount reads a replica count the example must state outright: the
// counts drive the assertions, and a count left to the CRD's default would
// leave the suite asserting on a topology the file never declared.
func declaredCount(field string, count *int32) int32 {
	if count == nil {
		panic(fmt.Sprintf("%s must declare spec.%s", exampleManifest, field))
	}
	return *count
}

// loadExample reads and decodes the quickstart manifest. Decoding is strict,
// so a field the example misspells fails the suite instead of being silently
// defaulted away by the API server.
func loadExample() *memgraphcomv1alpha1.MemgraphCluster {
	projectDir, err := utils.GetProjectDir()
	if err != nil {
		panic(fmt.Sprintf("locating the project directory: %v", err))
	}
	manifest, err := os.ReadFile(filepath.Join(projectDir, exampleManifest))
	if err != nil {
		panic(fmt.Sprintf("reading %s: %v", exampleManifest, err))
	}
	cluster := &memgraphcomv1alpha1.MemgraphCluster{}
	if err := yaml.UnmarshalStrict(manifest, cluster); err != nil {
		panic(fmt.Sprintf("decoding %s: %v", exampleManifest, err))
	}
	return cluster
}

// declaredInstances returns the instance names every coordinator and data
// instance must appear under in SHOW INSTANCES once the operator has converged
// registration: coordinator ordinal N registers as coordinator_N+1, data
// ordinal N as instance_N.
func (c clusterUnderTest) declaredInstances() []string {
	names := make([]string, 0, c.coordinators+c.dataInstances)
	for ordinal := range c.coordinators {
		names = append(names, fmt.Sprintf("coordinator_%d", ordinal+1))
	}
	for ordinal := range c.dataInstances {
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
		license, organization := licenseFromEnv()

		preloadMemgraphImage()
		createClusterNamespace(clusterNamespace)

		By("creating the enterprise license Secret")
		createLicenseSecret(clusterNamespace, license, organization)

		By("applying the MemgraphCluster")
		applyMemgraphCluster()
	})

	AfterAll(func() {
		By("removing the cluster namespace")
		cmd := exec.Command("kubectl", "delete", "ns", clusterNamespace,
			"--ignore-not-found", "--wait=false")
		_, _ = utils.Run(cmd)
	})

	AfterEach(func() {
		dumpDiagnosticsOnFailure(clusterNamespace)
	})

	It("bootstraps every declared instance registered with exactly one MAIN", func() {
		Eventually(quickstartCluster.verifyRegistered, 10*time.Minute, 10*time.Second).Should(Succeed())
	})

	// The operator's reason to exist over the chart's one-shot Job: a data
	// instance that loses its registration is re-registered with no human
	// action. This runs after the bootstrap spec (Ordered) against the same
	// converged cluster.
	It("re-registers a data instance whose registration was wiped", func() {
		const wiped = "instance_1"

		By("confirming the cluster is converged before wiping a registration")
		Eventually(quickstartCluster.verifyRegistered, 10*time.Minute, 10*time.Second).Should(Succeed())

		By("unregistering a data instance on the coordinator leader")
		Expect(wipeInstanceRegistration(wiped)).To(Succeed())

		By("confirming the instance really left the cluster view")
		view, err := quickstartCluster.leaderView()
		Expect(err).NotTo(HaveOccurred())
		names := make([]string, 0, len(view))
		for _, instance := range view {
			names = append(names, instance.name)
		}
		Expect(names).NotTo(ContainElement(wiped),
			"the wipe must actually remove the registration for the test to be meaningful")

		By("waiting for the operator to converge the cluster back to fully registered")
		Eventually(quickstartCluster.verifyRegistered, 10*time.Minute, 10*time.Second).Should(Succeed())
	})

	// The coordinator analogue of the data-instance re-registration: a
	// coordinator removed from the Raft cluster is re-added by the operator's
	// continuous ADD COORDINATOR reconciliation, with no human action. This
	// proves the re-registration loop covers coordinators, not just data
	// instances. Runs after the preceding specs (Ordered) against the same
	// converged cluster.
	It("re-adds a coordinator that was removed from the cluster", func() {
		By("confirming the cluster is converged before removing a coordinator")
		Eventually(quickstartCluster.verifyRegistered, 10*time.Minute, 10*time.Second).Should(Succeed())

		By("removing a follower coordinator on the coordinator leader")
		removed, err := removeCoordinatorRegistration()
		Expect(err).NotTo(HaveOccurred())

		By("confirming the coordinator really left the cluster view")
		view, err := quickstartCluster.leaderView()
		Expect(err).NotTo(HaveOccurred())
		names := make([]string, 0, len(view))
		for _, instance := range view {
			names = append(names, instance.name)
		}
		Expect(names).NotTo(ContainElement(removed),
			"the removal must actually drop the coordinator for the test to be meaningful")

		By("waiting for the operator to converge the cluster back to fully registered")
		Eventually(quickstartCluster.verifyRegistered, 10*time.Minute, 10*time.Second).Should(Succeed())
	})

	// Storage survives the cluster under the default retention policy: an
	// accidental `kubectl delete mgc` must not take a production database with
	// it. This deletes the CR, so it runs last in this Ordered container.
	It("leaves the PVCs behind when the default-retention CR is deleted", func() {
		By("confirming the cluster is converged before deleting it")
		Eventually(quickstartCluster.verifyRegistered, 10*time.Minute, 10*time.Second).Should(Succeed())

		By("recording the provisioned PVCs")
		before, err := listPVCs(clusterNamespace)
		Expect(err).NotTo(HaveOccurred())
		// Two claims (lib and log) per coordinator and data instance pod.
		Expect(before).To(HaveLen(int(2 * (coordinatorCount + dataInstanceCount))))

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
  coordinators: 3
  dataInstances: 1
  image:
    repository: %s
    tag: %s
  storage:
    retentionPolicy: Delete
`, retentionCluster, retentionNamespace,
			example.Spec.Image.Repository, example.Spec.Image.Tag)
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		_, err := utils.RunWithInput(cmd, manifest)
		Expect(err).NotTo(HaveOccurred(), "Failed to apply the MemgraphCluster")

		By("waiting for the claims to be provisioned")
		// One lib and one log claim per pod: three coordinators and one data
		// instance, the smallest topology admission accepts.
		Eventually(func(g Gomega) {
			claims, err := listPVCs(retentionNamespace)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(claims).To(HaveLen(8))
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

// Scaling a live cluster in both directions: the end-to-end proof that raising a
// count registers the members it adds, and that lowering the data-instance count
// retires the members it drops safely — MAIN moved off them, unregistered before
// their pods go. It gets its own container and namespace because it needs a
// cluster it may reshape, and its teardown is awaited — eight Memgraph pods are a
// large share of a Kind cluster's capacity, which the scenarios that may run
// after it need back.
var _ = Describe("MemgraphCluster topology scaling", Ordered, func() {
	const scalingNamespace = "memgraph-e2e-scaling"
	const scalingClusterName = "scaling"

	// The cluster starts at the default topology and grows to five coordinators
	// and three data instances.
	initial := clusterUnderTest{
		namespace: scalingNamespace, name: scalingClusterName, coordinators: 3, dataInstances: 2,
	}
	grown := initial.withTopology(5, 3)

	BeforeAll(func() {
		license, organization := licenseFromEnv()

		preloadMemgraphImage()
		createClusterNamespace(scalingNamespace)

		By("creating the enterprise license Secret")
		createLicenseSecret(scalingNamespace, license, organization)

		By("applying the MemgraphCluster to scale")
		// No log claim and explicit small requests: this cluster runs up to eight
		// pods on the same Kind nodes as the other scenarios', so it asks for as
		// little as it can while still being a real HA cluster.
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
`, scalingClusterName, scalingNamespace, initial.coordinators, initial.dataInstances,
			example.Spec.Image.Repository, example.Spec.Image.Tag, licenseSecretName)
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		_, err := utils.RunWithInput(cmd, manifest)
		Expect(err).NotTo(HaveOccurred(), "Failed to apply the MemgraphCluster")
	})

	AfterAll(func() {
		By("removing the cluster namespace and waiting for its pods to go")
		cmd := exec.Command("kubectl", "delete", "ns", scalingNamespace,
			"--ignore-not-found", "--wait=true", "--timeout=5m")
		_, _ = utils.Run(cmd)
	})

	AfterEach(func() {
		dumpDiagnosticsOnFailure(scalingNamespace)
	})

	It("bootstraps the initial topology", func() {
		Eventually(initial.verifyRegistered, 10*time.Minute, 10*time.Second).Should(Succeed())
		initial.awaitConverged(2 * time.Minute)
	})

	// Both counts are raised in one edit, in different step sizes, which is the
	// whole contract: nothing constrains a change beyond the target counts
	// themselves.
	It("grows both roles in one edit and registers every added member", func() {
		By("raising both counts on the live cluster")
		cmd := exec.Command("kubectl", "patch", "memgraphcluster", scalingClusterName,
			"-n", scalingNamespace, "--type=merge", "-p",
			fmt.Sprintf(`{"spec":{"coordinators":%d,"dataInstances":%d}}`,
				grown.coordinators, grown.dataInstances))
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "the operator must accept a raised topology count")

		By("waiting for every member of the grown topology to be registered")
		Eventually(grown.verifyRegistered, 10*time.Minute, 10*time.Second).Should(Succeed())

		By("confirming the resource reports the scale as finished")
		grown.awaitConverged(5 * time.Minute)
		Expect(grown.replicas("coordinator")).To(Equal("5"))
		Expect(grown.replicas("data")).To(Equal("3"))
		coordinators, dataInstances := grown.registeredCounts()
		Expect(coordinators).To(Equal("5"))
		Expect(dataInstances).To(Equal("3"))
	})

	// The shrink, against the 5/3 cluster the growth spec left behind (Ordered),
	// with MAIN deliberately parked on the ordinal that has to go — the case the
	// whole safety argument is about: Memgraph refuses to unregister a MAIN, so the
	// operator has to move it first, and it has to unregister before the pod goes
	// or the coordinators are left expecting an instance that is not there.
	It("retires a data instance holding MAIN and only then sheds its pod", func() {
		shrunk := grown.withTopology(5, 2)
		const retiring = "instance_2"

		By("parking MAIN on the data instance the shrink retires")
		// Retried as a whole: the operator promotes a survivor itself if it
		// observes the cluster MAIN-less between the two statements.
		Eventually(func(g Gomega) {
			g.Expect(grown.makeMain(retiring)).To(Succeed())
			view, err := grown.leaderView()
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(mainOf(view)).To(Equal(retiring))
		}, 5*time.Minute, 10*time.Second).Should(Succeed())

		By("lowering the data-instance count on the live cluster")
		cmd := exec.Command("kubectl", "patch", "memgraphcluster", scalingClusterName,
			"-n", scalingNamespace, "--type=merge", "-p",
			fmt.Sprintf(`{"spec":{"dataInstances":%d}}`, shrunk.dataInstances))
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "the operator must accept a lowered topology count")

		By("waiting for the retiring instance to leave the cluster while its pod is still there")
		Eventually(func(g Gomega) {
			// The replica count is read before the registration view, and that
			// order carries the whole assertion. A view read afterwards that still
			// lists the retiring instance proves it was registered at a moment the
			// StatefulSet had already been shrunk — the reverse order the operator
			// must never produce, because the coordinators would be left expecting
			// an instance whose pod is gone. Reading the view first would prove
			// nothing: the operator's own UNREGISTER can land between the two
			// reads, so a pre-unregistration view paired with a post-shrink count
			// is the correct sequence misread as a violation.
			replicas, err := shrunk.replicas("data")
			g.Expect(err).NotTo(HaveOccurred())

			view, err := shrunk.leaderView()
			g.Expect(err).NotTo(HaveOccurred())
			names := instanceNames(view)

			if replicas != "3" && slices.Contains(names, retiring) {
				// Not something to retry: the ordering this catches is broken for
				// good by the time it is observable.
				StopTrying(fmt.Sprintf(
					"the data StatefulSet was scaled to %s replicas while %s was still registered",
					replicas, retiring)).Now()
			}
			g.Expect(names).NotTo(ContainElement(retiring))
		}, 10*time.Minute, 5*time.Second).Should(Succeed())

		By("waiting for its pod to be shed")
		Eventually(func(g Gomega) {
			g.Expect(shrunk.replicas("data")).To(Equal("2"))
			g.Expect(shrunk.podExists("data", 2)).To(BeFalse())
		}, 5*time.Minute, 5*time.Second).Should(Succeed())

		By("confirming the shrunk cluster is registered, converged, and led by a survivor")
		Eventually(shrunk.verifyRegistered, 10*time.Minute, 10*time.Second).Should(Succeed())
		shrunk.awaitConverged(5 * time.Minute)
		_, dataInstances := shrunk.registeredCounts()
		Expect(dataInstances).To(Equal("2"))
		view, err := shrunk.leaderView()
		Expect(err).NotTo(HaveOccurred())
		Expect(mainOf(view)).To(BeElementOf("instance_0", "instance_1"))

		By("confirming the retired instance's claims are kept by the default retention policy")
		claims, err := listPVCs(scalingNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(claims).To(ContainElement(fmt.Sprintf("lib-storage-%s-data-2", scalingClusterName)),
			"whenScaled follows spec.storage.retentionPolicy, which defaults to Retain")
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

// verifyRegistered asserts the coordinator leader reports every declared
// instance registered and healthy with exactly one MAIN — the converged steady
// state the bootstrap, re-registration and scaling specs all check for.
func (c clusterUnderTest) verifyRegistered(g Gomega) {
	view, err := c.leaderView()
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
	g.Expect(names).To(ConsistOf(c.declaredInstances()))
	g.Expect(mains).To(HaveLen(1), "expected exactly one MAIN, got %v", mains)
}

// awaitConverged waits for the operator to report the declared topology as
// realized: every declared instance registered and both StatefulSets at the
// declared replica count. It is what a user gates a scale on.
func (c clusterUnderTest) awaitConverged(timeout time.Duration) {
	GinkgoHelper()
	cmd := exec.Command("kubectl", "wait", "--for=condition=Converged",
		"memgraphcluster/"+c.name, "-n", c.namespace, "--timeout="+timeout.String())
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "the MemgraphCluster never reported Converged")
}

// registeredCounts reads the registered coordinator and data-instance counts the
// operator publishes on the resource's status.
func (c clusterUnderTest) registeredCounts() (string, string) {
	GinkgoHelper()
	read := func(field string) string {
		cmd := exec.Command("kubectl", "get", "memgraphcluster", c.name, "-n", c.namespace,
			"-o", fmt.Sprintf("jsonpath={.status.%s}", field))
		output, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to read status.%s", field)
		return strings.TrimSpace(output)
	}
	return read("coordinators"), read("dataInstances")
}

// replicas reads the replica count the operator applied to a role's StatefulSet.
// The error is returned rather than asserted so the value can be read inside a
// polled assertion, where a transient kubectl failure has to retry.
func (c clusterUnderTest) replicas(component string) (string, error) {
	cmd := exec.Command("kubectl", "get", "statefulset", c.name+"-"+component, "-n", c.namespace,
		"-o", "jsonpath={.spec.replicas}")
	output, err := utils.Run(cmd)
	if err != nil {
		return "", fmt.Errorf("reading the %s StatefulSet's replicas: %w", component, err)
	}
	return strings.TrimSpace(output), nil
}

// podExists reports whether the pod with the given ordinal of a role's
// StatefulSet is still there at all — the state a shed pod leaves behind once the
// StatefulSet controller is done with it.
func (c clusterUnderTest) podExists(component string, ordinal int32) (bool, error) {
	cmd := exec.Command("kubectl", "get", "pod", fmt.Sprintf("%s-%s-%d", c.name, component, ordinal),
		"-n", c.namespace, "--ignore-not-found", "-o", "name")
	output, err := utils.Run(cmd)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(output) != "", nil
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
		view, err := quickstartCluster.showInstances(pod)
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
		view, err := quickstartCluster.showInstances(pod)
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

// dumpDiagnosticsOnFailure dumps everything needed to debug a broken cluster
// from the CI logs alone: the pods, the resource itself, the namespace's events
// and the operator's log.
// The operator's log comes from the namespace the operator is installed in, not
// the cluster's — a parameter named namespace would shadow that constant.
func dumpDiagnosticsOnFailure(clusterNamespace string) {
	if !CurrentSpecReport().Failed() {
		return
	}
	for _, args := range [][]string{
		{"get", "pods", "-n", clusterNamespace, "-o", "wide"},
		{"get", "memgraphclusters", "-n", clusterNamespace, "-o", "yaml"},
		{"get", "events", "-n", clusterNamespace, "--sort-by=.lastTimestamp"},
		{"logs", "deploy/" + controllerDeploymentName, "-n", namespace, "--tail=200"},
	} {
		cmd := exec.Command("kubectl", args...)
		output, err := utils.Run(cmd)
		if err != nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "Failed to collect diagnostics %v: %s\n", args, err)
			continue
		}
		_, _ = fmt.Fprintf(GinkgoWriter, "Diagnostics kubectl %v:\n%s\n", args, output)
	}
}

// licenseFromEnv reads the enterprise license every HA cluster in this suite
// needs, following the HA Helm chart's CI convention of repository secrets
// exported into the job environment.
func licenseFromEnv() (string, string) {
	GinkgoHelper()
	license := os.Getenv(licenseEnvVar)
	organization := os.Getenv(organizationEnvVar)
	Expect(license).NotTo(BeEmpty(),
		"%s must be set: the e2e suite boots a licensed Memgraph HA cluster", licenseEnvVar)
	Expect(organization).NotTo(BeEmpty(),
		"%s must be set: the e2e suite boots a licensed Memgraph HA cluster", organizationEnvVar)
	return license, organization
}

// preloadMemgraphImage puts the Memgraph image on the Kind nodes, so a cluster's
// pods do not each wait on a registry pull. It is idempotent, so every scenario
// container that boots Memgraph can call it without depending on another's
// setup.
func preloadMemgraphImage() {
	GinkgoHelper()
	By("preloading the Memgraph image into the Kind cluster")
	cmd := exec.Command("docker", "pull", memgraphImage)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to pull the Memgraph image")
	Expect(utils.LoadImageToKindClusterWithName(memgraphImage)).To(Succeed(),
		"Failed to load the Memgraph image into Kind")
}

// createClusterNamespace creates a namespace for a MemgraphCluster and enforces
// the restricted Pod Security Standard in it, so every scenario proves the
// operator's workloads run under the policy a security review demands.
func createClusterNamespace(namespace string) {
	GinkgoHelper()
	By("creating the cluster namespace " + namespace)
	cmd := exec.Command("kubectl", "create", "ns", namespace)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

	By("labeling the namespace to enforce the restricted security policy")
	cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
		"pod-security.kubernetes.io/enforce=restricted")
	_, err = utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")
}

// createLicenseSecret applies the Secret the MemgraphCluster references, under
// the name and keys the example points at. The manifest is piped over stdin so
// no secret material ever reaches the logged command line.
func createLicenseSecret(namespace, license, organization string) {
	secret := map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      licenseSecretName,
			"namespace": namespace,
		},
		"stringData": map[string]string{
			example.Spec.Secrets.LicenseKey:      license,
			example.Spec.Secrets.OrganizationKey: organization,
		},
	}
	manifest, err := json.Marshal(secret)
	Expect(err).NotTo(HaveOccurred(), "Failed to marshal the license Secret")

	cmd := exec.Command("kubectl", "apply", "-f", "-")
	_, err = utils.RunWithInput(cmd, string(manifest))
	Expect(err).NotTo(HaveOccurred(), "Failed to apply the license Secret")
}

// applyMemgraphCluster applies the CR under test: the README's quickstart
// manifest, unmodified, which is the minimal spec of the PRD's first-contact
// story — image, counts, and a license secret reference. The manifest declares
// no namespace, exactly as a newcomer applies it into their own.
func applyMemgraphCluster() {
	cmd := exec.Command("kubectl", "apply", "-n", clusterNamespace, "-f", exampleManifest)
	_, err := utils.Run(cmd)
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
func (c clusterUnderTest) leaderView() ([]instanceRow, error) {
	_, view, err := c.leaderPod()
	return view, err
}

// leaderPod locates the coordinator leader — the coordinator whose view reports a
// MAIN — and returns its pod name together with that view. Management queries a
// test issues by hand have to run there: only the leader holds the authoritative
// cluster state and accepts a mutation of it.
func (c clusterUnderTest) leaderPod() (string, []instanceRow, error) {
	var errs []error
	for ordinal := range c.coordinators {
		pod := fmt.Sprintf("%s-coordinator-%d", c.name, ordinal)
		view, err := c.showInstances(pod)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if mainOf(view) != "" {
			return pod, view, nil
		}
		errs = append(errs, fmt.Errorf("%s reports no MAIN among %d instances", pod, len(view)))
	}
	return "", nil, errors.Join(errs...)
}

// makeMain moves MAIN onto the named data instance by hand, which is how a spec
// arranges for the instance a scale-down retires to be the one holding MAIN.
//
// The demotion and the promotion go out as one mgconsole invocation because the
// operator promotes a survivor itself the moment it observes a MAIN-less cluster:
// the window between the two statements is the whole point of keeping them
// together. It is a no-op when the instance already is MAIN, so a caller can
// simply retry it.
func (c clusterUnderTest) makeMain(name string) error {
	pod, view, err := c.leaderPod()
	if err != nil {
		return err
	}
	main := mainOf(view)
	if main == name {
		return nil
	}
	query := fmt.Sprintf("DEMOTE INSTANCE %s; SET INSTANCE %s TO MAIN;", main, name)
	cmd := exec.Command("kubectl", "exec", pod, "-n", c.namespace, "-c", "memgraph", "--",
		"bash", "-c", fmt.Sprintf("echo '%s' | mgconsole", query))
	if _, err := utils.Run(cmd); err != nil {
		return fmt.Errorf("moving MAIN from %s to %s on %s: %w", main, name, pod, err)
	}
	return nil
}

// mainOf returns the name of the data instance a view reports as MAIN, or the
// empty string when it reports none.
func mainOf(view []instanceRow) string {
	for _, instance := range view {
		if instance.role == roleMain {
			return instance.name
		}
	}
	return ""
}

// instanceNames are the instance names a SHOW INSTANCES view lists.
func instanceNames(view []instanceRow) []string {
	names := make([]string, 0, len(view))
	for _, instance := range view {
		names = append(names, instance.name)
	}
	return names
}

// showInstances runs SHOW INSTANCES through mgconsole inside the given
// coordinator pod (the Memgraph image ships the client) and parses the CSV
// output.
func (c clusterUnderTest) showInstances(pod string) ([]instanceRow, error) {
	cmd := exec.Command("kubectl", "exec", pod, "-n", c.namespace, "-c", "memgraph", "--",
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
