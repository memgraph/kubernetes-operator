#!/usr/bin/env bash
#
# Give a Kind cluster a working LoadBalancer implementation: install MetalLB in
# L2 mode and hand it a slice of the Kind Docker network to allocate from.
#
# Kind has no cloud controller, so a Service of type LoadBalancer stays pending
# forever without this — and the e2e suite's external access scenario is about
# exactly the moment that address appears. The addresses come from the Docker
# network Kind's nodes sit on, which the host routes to, so the test process on
# the host is a genuine client outside the cluster.
#
# Idempotent: re-running against a cluster that already has MetalLB is a no-op.

set -euo pipefail

METALLB_VERSION=${METALLB_VERSION:-v0.16.1}
KUBECTL=${KUBECTL:-kubectl}
CONTAINER_TOOL=${CONTAINER_TOOL:-docker}
KIND_NETWORK=${KIND_NETWORK:-kind}
MANIFEST="https://raw.githubusercontent.com/metallb/metallb/${METALLB_VERSION}/config/manifests/metallb-native.yaml"

echo "Installing MetalLB ${METALLB_VERSION}"
"${KUBECTL}" apply -f "${MANIFEST}"
"${KUBECTL}" wait --namespace metallb-system --for=condition=Available deployment/controller --timeout=300s
"${KUBECTL}" rollout status --namespace metallb-system daemonset/speaker --timeout=300s

# The IPv4 subnet of the Kind Docker network, e.g. 172.18.0.0/16. Docker lists an
# IPv6 subnet beside it on dual-stack hosts, so the IPv4 one is picked out.
subnet=$("${CONTAINER_TOOL}" network inspect "${KIND_NETWORK}" \
  -f '{{range .IPAM.Config}}{{.Subnet}}{{"\n"}}{{end}}' | grep -v ':' | head -n 1)
if [[ -z "${subnet}" ]]; then
  echo "Could not find the IPv4 subnet of the '${KIND_NETWORK}' Docker network" >&2
  exit 1
fi
# The top of the /16 is far from anything Docker hands out to nodes, which it
# allocates from the bottom.
prefix=$(echo "${subnet}" | cut -d. -f1-2)
pool="${prefix}.255.200-${prefix}.255.250"
echo "Advertising LoadBalancer addresses from ${pool}"

# The MetalLB webhook that validates these comes up a little after the
# controller reports Available, so the apply is retried.
for attempt in $(seq 1 30); do
  if "${KUBECTL}" apply -f - <<EOF
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata:
  name: kind
  namespace: metallb-system
spec:
  addresses:
    - ${pool}
---
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata:
  name: kind
  namespace: metallb-system
spec:
  ipAddressPools:
    - kind
EOF
  then
    exit 0
  fi
  echo "MetalLB is not accepting its configuration yet (attempt ${attempt}/30)"
  sleep 5
done
echo "MetalLB never accepted its IPAddressPool" >&2
exit 1
