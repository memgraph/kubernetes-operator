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
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/memgraph/kubernetes-operator/test/utils"
)

// The three optional init containers — the sysctl one raising the node's
// vm.max_map_count, the core pattern one pointing the kernel at the dumps
// volume, both privileged, and the ownership one chowning the volumes as root
// — cannot run under the restricted Pod Security Standard every other
// scenario's namespace enforces, which is exactly why the quickstart carries
// none. This scenario is the one place they run: a namespace without the
// label, a cluster asking for all three, and the proof read from inside the
// pods rather than from the operator, because what these containers change
// is the node and the volume. Kind's default storage class is
// rancher.io/local-path, the very driver that ignores fsGroup, so the
// ownership container has real work to do here. It never waits for
// registration: every container has done its work by the time Memgraph is
// listening.
var _ = Describe("MemgraphCluster with root init containers", Ordered, func() {
	const initNamespace = "memgraph-e2e-init"
	const initCluster = "tuned"

	// Above what a CI runner or a developer machine ships with, so the
	// container has to raise the value rather than keep it. A node already
	// above it passes too — the assertion is the floor, not the path — but on
	// the default 65530 of most kernels this is the raise path end to end.
	const maxMapCount = 2097152

	dataPod := initCluster + "-data-0"
	coordinatorPod := initCluster + "-coordinator-0"

	BeforeAll(func() {
		license, organization := licenseFromEnv()
		preloadMemgraphImage()

		// Deliberately not createClusterNamespace: that one enforces the
		// restricted policy, under which these pods must be rejected.
		By("creating a cluster namespace without the restricted policy")
		cmd := exec.Command("kubectl", "create", "ns", initNamespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("creating the enterprise license Secret")
		createLicenseSecret(initNamespace, license, organization)
	})

	AfterAll(func() {
		By("removing the cluster namespace")
		cmd := exec.Command("kubectl", "delete", "ns", initNamespace,
			"--ignore-not-found", "--wait=false")
		_, _ = utils.Run(cmd)
	})

	It("raises vm.max_map_count, sets the core pattern and fixes volume ownership before Memgraph starts", func() {
		By("applying a MemgraphCluster asking for all three init containers on the data role")
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
  secrets:
    name: %s
  sysctlInitContainer:
    maxMapCount: %d
  fixOwnershipInitContainer: {}
  coreDumps:
    data: {}
`, initCluster, initNamespace, example.Spec.Image.Repository, example.Spec.Image.Tag,
			licenseSecretName, maxMapCount)
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		_, err := utils.RunWithInput(cmd, manifest)
		Expect(err).NotTo(HaveOccurred(), "Failed to apply the MemgraphCluster")

		// Polled rather than `kubectl wait`, which fails at once on a pod that
		// does not exist yet, and right after the apply the operator has not
		// created it.
		By("waiting for the data instance to be ready, which means its init containers have finished")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "pod", dataPod, "-n", initNamespace, "-o",
				`jsonpath={.status.conditions[?(@.type=="Ready")].status}`)
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(strings.TrimSpace(output)).To(Equal("True"))
		}, 5*time.Minute, 5*time.Second).Should(Succeed(), func() string {
			return "the data pod never became ready; its events:\n" + describePod(initNamespace, dataPod)
		})

		By("confirming all three init containers ran, sysctl first and ownership last, and exited cleanly")
		Expect(initContainerResults(initNamespace, dataPod)).To(Equal(
			"init-sysctl=0 init-core-pattern=0 init-fix-perms=0"))

		By("reading vm.max_map_count from inside the Memgraph container")
		value, err := readInPod(initNamespace, dataPod, "/proc/sys/vm/max_map_count")
		Expect(err).NotTo(HaveOccurred())
		have, err := strconv.Atoi(value)
		Expect(err).NotTo(HaveOccurred(), "vm.max_map_count %q is not a number", value)
		Expect(have).To(BeNumerically(">=", maxMapCount),
			"the node was not raised to the floor; init-sysctl said:\n%s",
			containerLogs(initNamespace, dataPod, "init-sysctl"))

		By("reading the core pattern from inside the Memgraph container")
		pattern, err := readInPod(initNamespace, dataPod, "/proc/sys/kernel/core_pattern")
		Expect(err).NotTo(HaveOccurred())
		Expect(pattern).To(Equal("/var/core/memgraph/core.%e.%p.%t.%s"))

		By("reading the ownership of every mounted volume from inside the Memgraph container")
		for _, path := range []string{"/var/lib/memgraph", "/var/log/memgraph", "/var/core/memgraph"} {
			owner, err := execInPod(initNamespace, dataPod, "stat", "-c", "%u:%g", path)
			Expect(err).NotTo(HaveOccurred())
			Expect(owner).To(Equal("101:103"),
				"%s is not owned by the memgraph user; init-fix-perms said:\n%s",
				path, containerLogs(initNamespace, dataPod, "init-fix-perms"))
		}

		// The sysctl and ownership blocks are cluster-wide and the core dumps
		// one per role: the coordinators asked for no dumps and get no core
		// pattern container.
		By("confirming the coordinators run the sysctl and ownership containers alone")
		Expect(initContainerResults(initNamespace, coordinatorPod)).To(Equal("init-sysctl=0 init-fix-perms=0"))
	})
})

// initContainerResults renders a pod's init containers in the order they ran
// with each one's exit code, e.g. "init-sysctl=0 init-core-pattern=0", so one
// assertion pins both the order and the outcome.
func initContainerResults(namespace, pod string) string {
	GinkgoHelper()
	cmd := exec.Command("kubectl", "get", "pod", pod, "-n", namespace, "-o",
		`jsonpath={range .status.initContainerStatuses[*]}{.name}={.state.terminated.exitCode} {end}`)
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to read the init container statuses of %s", pod)
	return strings.TrimSpace(output)
}

// readInPod returns the trimmed contents of a file as the Memgraph container
// of a pod sees it.
func readInPod(namespace, pod, path string) (string, error) {
	return execInPod(namespace, pod, "cat", path)
}

// execInPod runs a command in the Memgraph container of a pod and returns its
// trimmed output.
func execInPod(namespace, pod string, command ...string) (string, error) {
	args := append([]string{"exec", pod, "-n", namespace, "-c", "memgraph", "--"}, command...)
	cmd := exec.Command("kubectl", args...)
	output, err := utils.Run(cmd)
	return strings.TrimSpace(output), err
}

// containerLogs returns one container's logs, or the error text when they
// cannot be read: it only ever decorates a failure.
func containerLogs(namespace, pod, container string) string {
	cmd := exec.Command("kubectl", "logs", pod, "-n", namespace, "-c", container)
	output, err := utils.Run(cmd)
	if err != nil {
		return err.Error()
	}
	return output
}

// describePod is the pod's events and state, for a failure message.
func describePod(namespace, pod string) string {
	cmd := exec.Command("kubectl", "describe", "pod", pod, "-n", namespace)
	output, err := utils.Run(cmd)
	if err != nil {
		return err.Error()
	}
	return output
}
