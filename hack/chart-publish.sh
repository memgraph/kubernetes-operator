#!/usr/bin/env bash
#
# Package the operator install chart and cross-publish it into the existing
# memgraph.github.io/helm-charts index.
#
# Chart source and CRDs stay in this repository, but users install from the
# helm repository they already have configured. That index is a plain Helm
# index.yaml served from the helm-charts repository's gh-pages branch, whose
# entries point at .tgz assets attached to GitHub releases in that same
# repository -- the layout chart-releaser produces for the charts that live
# there. Publishing from here means writing into both halves:
#
#   1. a GitHub release <chart>-<version> in the charts repository, with the
#      packaged chart attached, and
#   2. an index.yaml entry on gh-pages pointing at that asset.
#
# The release comes first: an index entry whose download URL 404s is worse than
# an asset nobody can find yet.
#
# Every step is skipped when its result already exists, so re-running after a
# half-finished release finishes the job instead of duplicating it.
#
# Nothing leaves the machine without --publish. Without it this packages the
# chart, works out the merged index, and prints what would change.
#
# Publishing needs GH_TOKEN to carry a token with contents write access to the
# charts repository, and git to be able to push there (the release workflow
# runs `gh auth setup-git` for that). See docs/releasing.md.

set -euo pipefail

CHART_DIR=${CHART_DIR:-charts/memgraph-operator}
CHART_NAME=${CHART_NAME:-memgraph-operator}
CHARTS_REPO=${CHARTS_REPO:-memgraph/helm-charts}
CHARTS_REPO_URL=${CHARTS_REPO_URL:-https://github.com/${CHARTS_REPO}.git}
PAGES_BRANCH=${PAGES_BRANCH:-gh-pages}
OUT_DIR=${OUT_DIR:-dist/chart}

HELM=${HELM:-helm}
GIT=${GIT:-git}
GH=${GH:-gh}

GIT_USER_NAME=${GIT_USER_NAME:-memgraph-operator release}
GIT_USER_EMAIL=${GIT_USER_EMAIL:-tech@memgraph.com}

PUBLISH=false
VERSION=""

usage() {
  cat >&2 <<EOF
usage: $0 [--version <version>] [--publish]

  --version   chart version to publish; defaults to the version in ${CHART_DIR}/Chart.yaml
  --publish   actually create the release and push the index (default: dry run)
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --publish) PUBLISH=true ;;
    --version)
      [ $# -ge 2 ] || { usage; exit 2; }
      VERSION=$2
      shift
      ;;
    --version=*) VERSION=${1#--version=} ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage
      exit 2
      ;;
  esac
  shift
done

fail() {
  echo "$*" >&2
  exit 1
}

if [ -z "${VERSION}" ]; then
  VERSION=$(sed -n 's/^version:[[:space:]]*//p' "${CHART_DIR}/Chart.yaml" | tr -d '"' | head -1)
fi
VERSION=${VERSION#v}
[ -n "${VERSION}" ] || fail "No chart version given and none found in ${CHART_DIR}/Chart.yaml."

RELEASE_TAG="${CHART_NAME}-${VERSION}"
PACKAGE="${CHART_NAME}-${VERSION}.tgz"
# Where the index entry will point. chart-releaser builds the same URL for the
# charts already in this index, so the operator's entries look like the rest.
DOWNLOAD_URL="https://github.com/${CHARTS_REPO}/releases/download/${RELEASE_TAG}"

# A SemVer prerelease is hidden from `helm install` unless --devel is passed,
# which is what makes a release candidate a safe rehearsal against the real
# index: everything is exercised, nobody installs it by accident.
case "${VERSION}" in
  *-*) PRERELEASE=true ;;
  *) PRERELEASE=false ;;
esac

if [ "${PUBLISH}" = true ]; then
  [ -n "${GH_TOKEN:-}" ] || fail "--publish needs GH_TOKEN set to a token with contents write access to ${CHARTS_REPO}."
fi

WORK=$(mktemp -d)
trap 'rm -rf "${WORK}"' EXIT

echo "==> Packaging ${CHART_NAME} ${VERSION}"
mkdir -p "${OUT_DIR}"
rm -f "${OUT_DIR}/${CHART_NAME}"-*.tgz
"${HELM}" package "${CHART_DIR}" --version "${VERSION}" --destination "${OUT_DIR}"
[ -f "${OUT_DIR}/${PACKAGE}" ] || fail "helm package did not produce ${OUT_DIR}/${PACKAGE}."

# helm repo index reads a whole directory, so the package to be indexed gets a
# directory of its own -- OUT_DIR is the caller's and may hold anything.
mkdir -p "${WORK}/packages"
cp "${OUT_DIR}/${PACKAGE}" "${WORK}/packages/"

echo "==> Fetching the ${PAGES_BRANCH} branch of ${CHARTS_REPO}"
"${GIT}" clone --quiet --depth 1 --branch "${PAGES_BRANCH}" "${CHARTS_REPO_URL}" "${WORK}/pages"
INDEX="${WORK}/pages/index.yaml"
[ -f "${INDEX}" ] || fail "${CHARTS_REPO} ${PAGES_BRANCH} has no index.yaml."

# The download URL contains the version twice over, so this matches the exact
# version and never a prefix of it: 0.2.0 does not match 0.2.0-rc.1.
if grep -qF "/${RELEASE_TAG}/${PACKAGE}" "${INDEX}"; then
  INDEX_CURRENT=true
  echo "    index already lists ${CHART_NAME} ${VERSION}"
else
  INDEX_CURRENT=false
fi

echo "==> Merging the index entry"
"${HELM}" repo index "${WORK}/packages" --url "${DOWNLOAD_URL}" --merge "${INDEX}"

if [ "${PUBLISH}" != true ]; then
  echo
  echo "==> Dry run; nothing was published."
  echo "    package:      ${OUT_DIR}/${PACKAGE}"
  echo "    would release ${RELEASE_TAG} in ${CHARTS_REPO} (prerelease: ${PRERELEASE})"
  echo "    would serve   ${DOWNLOAD_URL}/${PACKAGE}"
  echo
  echo "==> Index diff"
  diff -u "${INDEX}" "${WORK}/packages/index.yaml" || true
  exit 0
fi

echo "==> Publishing ${RELEASE_TAG} to ${CHARTS_REPO}"
if "${GH}" release view "${RELEASE_TAG}" --repo "${CHARTS_REPO}" >/dev/null 2>&1; then
  echo "    release exists; making sure the package is attached"
  "${GH}" release upload "${RELEASE_TAG}" "${OUT_DIR}/${PACKAGE}" --repo "${CHARTS_REPO}" --clobber
else
  notes="Memgraph Kubernetes operator install chart ${VERSION}.

Chart source, CRDs and RBAC live in https://github.com/memgraph/kubernetes-operator;
this release exists so the packaged chart is served from the Memgraph helm repository.

    helm repo add memgraph https://memgraph.github.io/helm-charts
    helm repo update
    helm install memgraph-operator memgraph/memgraph-operator --version ${VERSION}"

  prerelease_flag=()
  [ "${PRERELEASE}" = true ] && prerelease_flag=(--prerelease)

  "${GH}" release create "${RELEASE_TAG}" "${OUT_DIR}/${PACKAGE}" \
    --repo "${CHARTS_REPO}" \
    --title "${RELEASE_TAG}" \
    --notes "${notes}" \
    "${prerelease_flag[@]}"
fi

if [ "${INDEX_CURRENT}" = true ]; then
  echo "==> Index already current; leaving ${PAGES_BRANCH} alone"
  exit 0
fi

echo "==> Pushing the index to ${CHARTS_REPO} ${PAGES_BRANCH}"
cp "${WORK}/packages/index.yaml" "${INDEX}"
"${GIT}" -C "${WORK}/pages" add index.yaml
"${GIT}" -C "${WORK}/pages" \
  -c "user.name=${GIT_USER_NAME}" \
  -c "user.email=${GIT_USER_EMAIL}" \
  commit --quiet --message "Add ${CHART_NAME} ${VERSION} to the index

Published from memgraph/kubernetes-operator."
"${GIT}" -C "${WORK}/pages" push --quiet origin "${PAGES_BRANCH}"

echo "==> Published ${CHART_NAME} ${VERSION}"
echo "    GitHub Pages needs a moment to serve the new index."
