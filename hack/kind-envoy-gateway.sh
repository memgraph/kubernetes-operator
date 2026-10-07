#!/usr/bin/env bash
#
# Give a Kind cluster a Gateway API implementation: install Envoy Gateway, which
# brings the Gateway API CRDs with it, and a GatewayClass named "eg" for it to
# program.
#
# The operator never bundles the Gateway API CRDs — they belong to whoever
# installs a Gateway controller — so the e2e suite plays that role here. Envoy's
# data plane is exposed through a Service of type LoadBalancer, so this runs
# after hack/kind-metallb.sh: that is where the Gateway's address comes from.
#
# Idempotent: re-running against a cluster that already has Envoy Gateway is a
# no-op.

set -euo pipefail

ENVOY_GATEWAY_VERSION=${ENVOY_GATEWAY_VERSION:-v1.9.1}
GATEWAY_CLASS=${GATEWAY_CLASS:-eg}
KUBECTL=${KUBECTL:-kubectl}
MANIFEST="https://github.com/envoyproxy/gateway/releases/download/${ENVOY_GATEWAY_VERSION}/install.yaml"

echo "Installing Envoy Gateway ${ENVOY_GATEWAY_VERSION}"
# Server-side: the CRDs in this manifest are too large for the client-side
# last-applied annotation.
"${KUBECTL}" apply --server-side -f "${MANIFEST}"
"${KUBECTL}" wait --namespace envoy-gateway-system --for=condition=Available deployment/envoy-gateway --timeout=300s

echo "Creating GatewayClass ${GATEWAY_CLASS}"
"${KUBECTL}" apply -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: ${GATEWAY_CLASS}
spec:
  controllerName: gateway.envoyproxy.io/gatewayclass-controller
EOF
"${KUBECTL}" wait --for=condition=Accepted "gatewayclass/${GATEWAY_CLASS}" --timeout=120s
