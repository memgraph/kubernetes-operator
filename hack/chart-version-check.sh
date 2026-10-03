#!/usr/bin/env bash
#
# Chart versioning check, run on every pull request.
#
# The chart's version and its appVersion move independently: appVersion names
# the operator image the chart installs by default, version names the chart
# itself. Neither is derived from the other, so what has to hold is narrower --
# a pull request that changes the chart has to change the chart's version too.
# Without that, the published index keeps serving the previous package under a
# version number that no longer describes its contents, and `helm upgrade` has
# nothing to act on.
#
# Compares against the base branch, so it stays quiet on pull requests that do
# not touch the chart.

set -euo pipefail

CHART_DIR=${CHART_DIR:-charts/memgraph-operator}
CHART_FILE="${CHART_DIR}/Chart.yaml"
BASE_REF=${BASE_REF:-origin/main}

# Helm requires SemVer for the chart version, and the appVersion is the image
# tag the Deployment template falls back to, so both are checked the same way.
SEMVER_RE='^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'

fail() {
  echo "$*" >&2
  exit 1
}

# field prints a top-level scalar from a Chart.yaml on stdin, unquoted.
# Comments never match: the field name has to start the line.
field() {
  sed -n "s/^$1:[[:space:]]*//p" | tr -d '"' | head -1
}

[ -f "${CHART_FILE}" ] || fail "No chart at ${CHART_FILE}; set CHART_DIR."

version=$(field version <"${CHART_FILE}")
app_version=$(field appVersion <"${CHART_FILE}")

[[ ${version} =~ ${SEMVER_RE} ]] ||
  fail "${CHART_FILE}: version '${version}' is not a SemVer version (e.g. 0.2.0, 0.2.0-rc.1)."
[[ ${app_version} =~ ${SEMVER_RE} ]] ||
  fail "${CHART_FILE}: appVersion '${app_version}' is not a SemVer version; it is an operator image tag."

if ! git rev-parse --verify --quiet "${BASE_REF}^{commit}" >/dev/null; then
  echo "No ${BASE_REF} to compare against; checked the version fields only."
  exit 0
fi

if git diff --quiet "${BASE_REF}" -- "${CHART_DIR}"; then
  echo "Chart unchanged against ${BASE_REF} (version ${version}, appVersion ${app_version})."
  exit 0
fi

if ! git cat-file -e "${BASE_REF}:${CHART_FILE}" 2>/dev/null; then
  echo "${CHART_FILE} is new against ${BASE_REF}; nothing to compare (version ${version})."
  exit 0
fi

base_version=$(git show "${BASE_REF}:${CHART_FILE}" | field version)

if [ "${version}" = "${base_version}" ]; then
  fail "${CHART_DIR} changed but its version is still ${version}.
A published chart version is immutable, so every change to the chart needs a new
one. Bump 'version' in ${CHART_FILE}; leave 'appVersion' alone unless this
release also ships a new operator image."
fi

echo "Chart ${base_version} -> ${version} (appVersion ${app_version})."
