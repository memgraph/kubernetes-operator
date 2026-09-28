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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/memgraph/kubernetes-operator/test/utils"
)

// The two privileged init containers — the sysctl one raising the node's
// vm.max_map_count and the core pattern one pointing the kernel at the dumps
// volume — cannot run under the restricted Pod Security Standard every other
// scenario's namespace enforces, which is exactly why the quickstart carries
// neither. This scenario is the one place they run: a namespace without the
// label, a cluster asking for both, and the proof read from inside the pods
// rather than from the operator, because what these containers change is the
// node. It never waits for registration: both containers have done their work
// by the time Memgraph is listening.
var _ = Describe("MemgraphCluster with privileged init containers", Ordered, func() {
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

	It("raises vm.max_map_count and sets the core pattern before Memgraph starts", func() {
		By("applying a MemgraphCluster asking for both init containers on the data role")
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
  coreDumps:
    data: {}
`, initCluster, initNamespace, example.Spec.Image.Repository, example.Spec.Image.Tag,
			licenseSecretName, maxMapCount)
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		_, err := utils.RunWithInput(cmd, manifest)
		Expect(err).NotTo(HaveOccurred(), "Failed to apply the MemgraphCluster")

		By("waiting for the data instance to be ready, which means its init containers have finished")
		cmd = exec.Command("kubectl", "wait", "--for=condition=Ready", "pod/"+dataPod,
			"-n", initNamespace, "--timeout=5m")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "the data pod never became ready; its events:\n%s",
			describePod(initNamespace, dataPod))

		By("confirming both init containers ran, sysctl first, and exited cleanly")
		Expect(initContainerResults(initNamespace, dataPod)).To(Equal(
			"init-sysctl=0 init-core-pattern=0"))

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

		// The sysctl block is cluster-wide and the core dumps one per role: the
		// coordinators asked for no dumps and get the sysctl container alone.
		By("confirming the coordinators run only the sysctl container")
		Expect(initContainerResults(initNamespace, coordinatorPod)).To(Equal("init-sysctl=0"))
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
	cmd := exec.Command("kubectl", "exec", pod, "-n", namespace, "-c", "memgraph", "--", "cat", path)
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
