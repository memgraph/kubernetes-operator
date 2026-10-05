#!/usr/bin/env bash
#
# Give a Kind cluster a StorageClass whose volumes can grow: install the CSI
# hostpath driver and a StorageClass on it with allowVolumeExpansion.
#
# Kind's default provisioner, rancher.io/local-path, cannot expand a volume at
# all, and growing claims under running pods is exactly what the e2e resize
# scenario proves. The hostpath driver expands online, so the scenario also
# proves the resize restarts nothing. Its standard deployment runs the
# driver on one node, so a pod on a claim of this class is scheduled there;
# the distributed deployment would spread the volumes but carries no resizer.
# Only the resize scenario names the class; every other one stays on the
# default.
#
# The driver's snapshotter sidecar needs the VolumeSnapshot CRDs to start, so
# they are applied first; no snapshot controller runs.
#
# Idempotent: re-running against a cluster that already has the driver is a
# no-op.

set -euo pipefail

CSI_HOSTPATH_VERSION=${CSI_HOSTPATH_VERSION:-v1.18.0}
# The external-snapshotter release whose sidecar that driver release deploys.
SNAPSHOTTER_VERSION=${SNAPSHOTTER_VERSION:-v8.6.0}
KUBECTL=${KUBECTL:-kubectl}
STORAGE_CLASS=${STORAGE_CLASS:-csi-hostpath-expandable}

echo "Installing the VolumeSnapshot CRDs ${SNAPSHOTTER_VERSION}"
for crd in volumesnapshotclasses volumesnapshotcontents volumesnapshots; do
  "${KUBECTL}" apply -f "https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_VERSION}/client/config/crd/snapshot.storage.k8s.io_${crd}.yaml"
done

echo "Installing the CSI hostpath driver ${CSI_HOSTPATH_VERSION}"
workdir=$(mktemp -d)
trap 'rm -rf "${workdir}"' EXIT
curl -sSfL "https://github.com/kubernetes-csi/csi-driver-host-path/archive/refs/tags/${CSI_HOSTPATH_VERSION}.tar.gz" |
  tar -xz -C "${workdir}" --strip-components=1
# The deploy script applies into the current namespace and waits for the
# driver's StatefulSet itself.
KUBECTL="${KUBECTL}" "${workdir}/deploy/kubernetes-latest/deploy.sh"

"${KUBECTL}" apply -f - <<EOF
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: ${STORAGE_CLASS}
provisioner: hostpath.csi.k8s.io
allowVolumeExpansion: true
volumeBindingMode: WaitForFirstConsumer
reclaimPolicy: Delete
EOF
