#!/usr/bin/env bash
#
# Install/uninstall test for the operator install chart: helm install on a
# clean Kind cluster, then assert the operator reconciles a MemgraphCluster
# under the chart's least-privilege RBAC, then uninstall everything again.
#
# This is deliberately license-free — it asserts what the chart is responsible
# for (the operator runs, its RBAC is sufficient, install and uninstall are
# clean), not what a booted Memgraph cluster does. The e2e suite covers that
# on top of the same chart install.
#
# Set CHART_TEST_KEEP=true to keep the Kind cluster for inspection.

set -euo pipefail

CLUSTER=${CHART_TEST_CLUSTER:-memgraph-operator-chart-test}
CHART_DIR=${CHART_DIR:-charts/memgraph-operator}
CHART_RELEASE=${CHART_RELEASE:-memgraph-operator}
CHART_NAMESPACE=${CHART_NAMESPACE:-memgraph-operator-system}
CLUSTER_NAMESPACE=${CLUSTER_NAMESPACE:-memgraph-chart-test}
IMG=${IMG:-example.com/kubernetes-operator:chart-test}
IMAGE_REPOSITORY=${IMG%:*}
IMAGE_TAG=${IMG##*:}
CRD_NAME=memgraphclusters.memgraph.com

KIND=${KIND:-kind}
KUBECTL=${KUBECTL:-kubectl}
HELM=${HELM:-helm}
CONTAINER_TOOL=${CONTAINER_TOOL:-docker}

# wait_for_object polls until an object exists, so the script does not depend
# on a kubectl new enough for `wait --for=create`.
wait_for_object() {
  local object=$1
  for _ in $(seq 1 60); do
    if "${KUBECTL}" get "${object}" -n "${CLUSTER_NAMESPACE}" >/dev/null 2>&1; then
      echo "${object} exists"
      return 0
    fi
    sleep 2
  done
  echo "FAIL: ${object} was not created in ${CLUSTER_NAMESPACE}" >&2
  "${KUBECTL}" get memgraphcluster chart-test -n "${CLUSTER_NAMESPACE}" -o yaml >&2 || true
  "${KUBECTL}" logs -n "${CHART_NAMESPACE}" \
    "deployment/${CHART_RELEASE}-controller-manager" --tail=100 >&2 || true
  return 1
}

cleanup() {
  if [ "${CHART_TEST_KEEP:-false}" = "true" ]; then
    echo "Keeping Kind cluster ${CLUSTER} (CHART_TEST_KEEP=true)"
    return
  fi
  echo "==> Deleting Kind cluster ${CLUSTER}"
  "${KIND}" delete cluster --name "${CLUSTER}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "==> Creating Kind cluster ${CLUSTER}"
"${KIND}" delete cluster --name "${CLUSTER}" >/dev/null 2>&1 || true
"${KIND}" create cluster --name "${CLUSTER}"

echo "==> Building and loading the operator image ${IMG}"
"${CONTAINER_TOOL}" build -t "${IMG}" .
"${KIND}" load docker-image "${IMG}" --name "${CLUSTER}"

# The restricted Pod Security Standard is what makes the operator's security
# context load-bearing: a pod that is not non-root is rejected outright.
echo "==> Creating namespace ${CHART_NAMESPACE} enforcing the restricted policy"
"${KUBECTL}" create namespace "${CHART_NAMESPACE}"
"${KUBECTL}" label --overwrite namespace "${CHART_NAMESPACE}" \
  pod-security.kubernetes.io/enforce=restricted

echo "==> helm install ${CHART_RELEASE} from ${CHART_DIR}"
"${HELM}" install "${CHART_RELEASE}" "${CHART_DIR}" \
  --namespace "${CHART_NAMESPACE}" \
  --set-string "image.repository=${IMAGE_REPOSITORY}" \
  --set-string "image.tag=${IMAGE_TAG}" \
  --wait --timeout 5m

echo "==> Asserting the operator runs as non-root"
run_as_non_root=$("${KUBECTL}" get pods -n "${CHART_NAMESPACE}" \
  -l control-plane=controller-manager \
  -o jsonpath='{.items[0].spec.securityContext.runAsNonRoot}')
if [ "${run_as_non_root}" != "true" ]; then
  echo "FAIL: operator pod does not set runAsNonRoot" >&2
  exit 1
fi

# A MemgraphCluster with no license Secret never boots Memgraph, but the
# operator still has to provision the workloads and report status — which is
# exactly the set of API calls the chart's RBAC has to cover.
echo "==> Applying a MemgraphCluster in ${CLUSTER_NAMESPACE}"
"${KUBECTL}" create namespace "${CLUSTER_NAMESPACE}"
"${KUBECTL}" apply -f - <<EOF
apiVersion: memgraph.com/v1alpha1
kind: MemgraphCluster
metadata:
  name: chart-test
  namespace: ${CLUSTER_NAMESPACE}
spec:
  coordinators: 1
  dataInstances: 1
  image:
    repository: docker.io/memgraph/memgraph
    tag: 3.12.0
  secrets:
    name: memgraph-secrets
    licenseKey: MEMGRAPH_ENTERPRISE_LICENSE
    organizationKey: MEMGRAPH_ORGANIZATION_NAME
EOF

echo "==> Waiting for the operator to provision the workloads"
for object in \
  statefulset/chart-test-coordinator statefulset/chart-test-data \
  service/chart-test-coordinator service/chart-test-data; do
  wait_for_object "${object}"
done

echo "==> Waiting for the operator to report status on the MemgraphCluster"
"${KUBECTL}" wait --for=condition=Converged=false --timeout=2m \
  memgraphcluster/chart-test -n "${CLUSTER_NAMESPACE}"

# Any authorization failure means the generated ClusterRole is missing
# something the controller actually issues.
echo "==> Asserting the operator hit no authorization error"
logs=$("${KUBECTL}" logs -n "${CHART_NAMESPACE}" \
  "deployment/${CHART_RELEASE}-controller-manager" --tail=-1)
if grep -qi "is forbidden" <<<"${logs}"; then
  echo "FAIL: the operator was denied an API call under the chart's RBAC:" >&2
  grep -i "is forbidden" <<<"${logs}" >&2
  exit 1
fi

echo "==> helm uninstall ${CHART_RELEASE}"
"${KUBECTL}" delete memgraphcluster chart-test -n "${CLUSTER_NAMESPACE}" --timeout=2m
"${HELM}" uninstall "${CHART_RELEASE}" --namespace "${CHART_NAMESPACE}" --wait

# The release's own objects are gone when uninstall returns; the controller pod
# follows once it finishes terminating.
for _ in $(seq 1 30); do
  remaining=$("${KUBECTL}" get all -n "${CHART_NAMESPACE}" \
    --no-headers --ignore-not-found | wc -l)
  [ "${remaining}" -eq 0 ] && break
  sleep 2
done
if [ "${remaining}" -ne 0 ]; then
  echo "FAIL: helm uninstall left objects behind in ${CHART_NAMESPACE}:" >&2
  "${KUBECTL}" get all -n "${CHART_NAMESPACE}" >&2
  exit 1
fi

# Helm leaves CRDs behind by design, so the documented uninstall removes them
# explicitly. Both halves are asserted: still present after uninstall, gone
# after the delete.
"${KUBECTL}" get crd "${CRD_NAME}" >/dev/null
"${KUBECTL}" delete -f "${CHART_DIR}/crds"
if "${KUBECTL}" get crd "${CRD_NAME}" >/dev/null 2>&1; then
  echo "FAIL: ${CRD_NAME} survived the CRD deletion" >&2
  exit 1
fi

echo "==> Chart install/uninstall test passed"
