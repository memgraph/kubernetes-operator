#!/usr/bin/env bash
#
# Test for the cross-publish path, run on every pull request.
#
# hack/chart-publish.sh writes into another repository, so the one thing that
# must not happen is discovering a mistake in it during a release. This runs it
# end to end against a local stand-in for memgraph/helm-charts -- a bare git
# repository holding a gh-pages branch with a realistic index.yaml, and a `gh`
# stub that records what it was asked to do -- and asserts on what came out:
# the index the charts repository ends up with, and the release that was cut.
#
# Offline and side-effect free: no network, nothing touched outside a temp
# directory.

set -euo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
PUBLISH=${PUBLISH:-"${REPO_ROOT}/hack/chart-publish.sh"}
HELM=${HELM:-helm}

WORK=$(mktemp -d)
trap 'rm -rf "${WORK}"' EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

# assert_indexed fails unless the published index lists exactly one entry for
# the version, pointing at the release asset that would serve it.
assert_indexed() {
  local version=$1 index=$2
  local url="https://github.com/memgraph/helm-charts/releases/download/memgraph-operator-${version}/memgraph-operator-${version}.tgz"
  local count
  count=$(grep -cF "${url}" "${index}" || true)
  [ "${count}" = "1" ] ||
    fail "expected exactly one index entry for ${version}, found ${count}"
}

# published_index checks out the stand-in's gh-pages branch as the world sees it.
published_index() {
  rm -rf "${WORK}/published"
  git clone --quiet --branch gh-pages "${CHARTS_URL}" "${WORK}/published"
  echo "${WORK}/published/index.yaml"
}

echo "==> Building a stand-in for memgraph/helm-charts"
# file:// rather than a bare path, so the clone behaves like a remote one --
# git ignores --depth on local clones.
CHARTS_URL="file://${WORK}/helm-charts.git"
git init --quiet --bare --initial-branch=gh-pages "${WORK}/helm-charts.git"
git init --quiet --initial-branch=gh-pages "${WORK}/seed"

# An abridged copy of the real index: one entry for an unrelated chart, which
# the merge has to carry through untouched.
cat >"${WORK}/seed/index.yaml" <<'EOF'
apiVersion: v1
entries:
  memgraph:
  - apiVersion: v2
    appVersion: 3.12.0
    created: "2026-07-22T11:28:55.491533439Z"
    description: MemgraphDB Helm Chart
    digest: bef59ed4e202c17d8f84f5262e819d7b96d3ddc2fc92835c949ca9c21aa25dcb
    name: memgraph
    type: application
    urls:
    - https://github.com/memgraph/helm-charts/releases/download/memgraph-1.0.5/memgraph-1.0.5.tgz
    version: 1.0.5
generated: "2026-07-22T11:28:55.490899258Z"
EOF

git -C "${WORK}/seed" add index.yaml
git -C "${WORK}/seed" -c user.name=test -c user.email=test@example.com \
  commit --quiet -m "Seed the index"
git -C "${WORK}/seed" push --quiet "${WORK}/helm-charts.git" gh-pages

echo "==> Installing a gh stub"
mkdir -p "${WORK}/bin" "${WORK}/releases"
cat >"${WORK}/bin/gh" <<'EOF'
#!/usr/bin/env bash
# Records every invocation, and answers `release view` from the releases it has
# been asked to create, so the script's resume path is exercised for real.
set -euo pipefail
# One invocation per line: release notes are multi-line, and an argument that
# broke the log into several lines would break every assertion on it.
{ printf '%s ' "$@" | tr '\n' ' '; printf '\n'; } >>"${GH_LOG}"
case "${1:-} ${2:-}" in
  "release view")
    [ -f "${GH_RELEASES}/$3" ] || exit 1
    ;;
  "release create")
    tag=$3
    asset=$4
    [ -f "${asset}" ] || { echo "gh stub: no such asset: ${asset}" >&2; exit 1; }
    printf '%s\n' "$*" >"${GH_RELEASES}/${tag}"
    ;;
  "release upload")
    [ -f "${GH_RELEASES}/$3" ] || { echo "gh stub: no such release: $3" >&2; exit 1; }
    ;;
esac
exit 0
EOF
chmod +x "${WORK}/bin/gh"

export GH_LOG="${WORK}/gh.log"
export GH_RELEASES="${WORK}/releases"
: >"${GH_LOG}"

run_publish() {
  env \
    CHARTS_REPO_URL="${CHARTS_URL}" \
    OUT_DIR="${WORK}/dist" \
    GH="${WORK}/bin/gh" \
    GH_TOKEN=stub-token \
    HELM="${HELM}" \
    "${PUBLISH}" "$@"
}

echo
echo "==> A dry run publishes nothing"
run_publish --version 9.9.9 >"${WORK}/dry-run.log"
[ -f "${WORK}/dist/memgraph-operator-9.9.9.tgz" ] ||
  fail "the dry run did not package the chart"
[ ! -s "${GH_LOG}" ] ||
  fail "the dry run called gh: $(cat "${GH_LOG}")"
if grep -q "9.9.9" "$(published_index)"; then
  fail "the dry run pushed to the charts repository"
fi
grep -q "Dry run; nothing was published" "${WORK}/dry-run.log" ||
  fail "the dry run did not say so"

echo
echo "==> Publishing a release cuts it and indexes it"
run_publish --version 9.9.9 --publish >/dev/null
grep -qF "release create memgraph-operator-9.9.9 " "${GH_LOG}" ||
  fail "no release was created: $(cat "${GH_LOG}")"
if grep -F "release create memgraph-operator-9.9.9 " "${GH_LOG}" | grep -q -- "--prerelease"; then
  fail "a stable version was released as a prerelease"
fi

index=$(published_index)
assert_indexed 9.9.9 "${index}"
grep -qF "memgraph-1.0.5.tgz" "${index}" ||
  fail "the merge dropped the pre-existing memgraph entry"
grep -q "^  memgraph-operator:" "${index}" ||
  fail "the index has no memgraph-operator entry"

echo
echo "==> Republishing the same version changes nothing"
run_publish --version 9.9.9 --publish >/dev/null
assert_indexed 9.9.9 "$(published_index)"

echo
echo "==> A prerelease version is marked as one"
run_publish --version 9.9.9-rc.1 --publish >/dev/null
grep -F "release create memgraph-operator-9.9.9-rc.1" "${GH_LOG}" | grep -q -- "--prerelease" ||
  fail "the release candidate was not marked as a prerelease"

index=$(published_index)
assert_indexed 9.9.9-rc.1 "${index}"
assert_indexed 9.9.9 "${index}"

echo
echo "==> Publishing without a token is refused"
if env CHARTS_REPO_URL="${WORK}/helm-charts.git" OUT_DIR="${WORK}/dist" \
  GH="${WORK}/bin/gh" GH_TOKEN="" HELM="${HELM}" \
  "${PUBLISH}" --version 9.9.8 --publish >/dev/null 2>&1; then
  fail "publishing without GH_TOKEN succeeded"
fi

echo
echo "==> Cross-publish test passed"
