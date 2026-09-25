#!/usr/bin/env bash
#
# Give a Kind cluster the ServiceMonitor CRD, and nothing else of Prometheus
# Operator: the e2e suite proves the operator creates and prunes the object,
# and that the instances serve OpenMetrics; whether a Prometheus discovers and
# scrapes a ServiceMonitor is Prometheus Operator's contract, not this one's,
# so no Prometheus runs in the suite.
#
# The CRD applied is the copy under test/crds, the same file envtest loads, so
# there is one version to bump and it is the version of the prometheus-operator
# API module the operator builds against.
#
# Idempotent: re-running against a cluster that already has the CRD is a no-op.

set -euo pipefail

KUBECTL=${KUBECTL:-kubectl}
CRD_DIR=${CRD_DIR:-"$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/test/crds"}

echo "Installing the ServiceMonitor CRD from ${CRD_DIR}"
# Server-side: the CRD is too large for the client-side last-applied annotation.
"${KUBECTL}" apply --server-side -f "${CRD_DIR}/monitoring.coreos.com_servicemonitors.yaml"
"${KUBECTL}" wait --for=condition=Established crd/servicemonitors.monitoring.coreos.com --timeout=120s
