#!/bin/bash

# Copyright 2026 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# attest-artifacts.sh runs after the staging push in cloudbuild.yaml. For each
# image and chart digest that the push recorded in DIGEST_DIR, it writes a SLSA v1
# provenance predicate with hack/releasing/provenance.py.
#
# SIGN=false is a dry run: it writes and logs the predicates. It does not run cosign
# and does not use the network.
# SIGN=true also attaches each predicate with cosign, then verifies that at least one
# verified attestation of the digest carries exactly the predicate this build wrote.
#
# Environment:
#   DIGEST_DIR      directory with the digests written by the push targets (required)
#   SIGN            true to attest and verify, false for a dry run (required)
#   PULL_BASE_REF   branch or tag that triggered the build (required)
#   BUILD_ID        Cloud Build build ID (required)
#   PROJECT_ID      Cloud Build project ID (required)
#   PROJECT_NUMBER  Cloud Build project number (required)
#   PREDICATE_DIR   where the predicates are written (default: DIGEST_DIR/predicates)
#   COSIGN_BIN      cosign to use instead of the pinned download; for tests only

set -o errexit
set -o nounset
set -o pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
PROVENANCE="${ROOT}/hack/releasing/provenance.py"

# The registry paths match IMAGE_REPO in the Makefile and HELM_CHART_REPO in
# hack/helm-chart-package.sh for the staging build.
STAGING_REGISTRY=us-central1-docker.pkg.dev/k8s-staging-images/kueue
IMAGE_NAMES=(kueue kueueviz-backend kueueviz-frontend kueue-populator kueue-priority-booster)
CHART_NAMES=(kueue kueue-populator kueue-priority-booster)

# The build identity from KEP-1466 and the Google OIDC issuer.
SIGNER_IDENTITY=kueue-sa@k8s-staging-images.iam.gserviceaccount.com
OIDC_ISSUER=https://accounts.google.com

# cosign v3.1.4 for linux-amd64. The sha256 is the one listed in the release's
# cosign_checksums.txt. Keep this version in sync with the cosign version that
# /wait-for-images uses to verify attestations (KEP-1466).
COSIGN_VERSION=v3.1.4
COSIGN_LINUX_AMD64_SHA256=424e2c97d0b45fd7a6b608a87f090f35633f4c23ece5f092d48bba11aaf4bbfa

# Parallel arrays with one entry per pushed artifact.
LABELS=()
REFS=()
DIGESTS=()

fail() {
  echo "attest-artifacts: $*" >&2
  exit 1
}

# require_tools fails before any predicate is written when a tool is missing.
# curl and sha256sum are needed only to download cosign.
require_tools() {
  local tool
  for tool in bash git python3; do
    command -v "${tool}" > /dev/null || fail "${tool} is required but not found on PATH"
  done
  if [[ "${SIGN}" == true && -z "${COSIGN_BIN:-}" ]]; then
    for tool in curl sha256sum; do
      command -v "${tool}" > /dev/null || fail "${tool} is required to download cosign but not found on PATH"
    done
  fi
}

add_artifact() {
  local label=$1 ref=$2 digest=$3
  [[ "${digest}" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "invalid digest for ${label}: ${digest}"
  LABELS+=("${label}")
  REFS+=("${ref}")
  DIGESTS+=("${digest}")
}

# collect_artifacts reads every expected digest. It fails on the first missing file,
# before any predicate is written.
collect_artifacts() {
  local name file digest
  for name in "${IMAGE_NAMES[@]}"; do
    file="${DIGEST_DIR}/${name}.json"
    [[ -f "${file}" ]] || fail "missing image digest file ${file}"
    digest=$(python3 -I -c 'import json, sys; print(json.load(open(sys.argv[1], encoding="utf-8"))["containerimage.digest"])' "${file}") ||
      fail "no containerimage.digest in ${file}"
    add_artifact "image-${name}" "${STAGING_REGISTRY}/${name}" "${digest}"
  done
  for name in "${CHART_NAMES[@]}"; do
    file="${DIGEST_DIR}/chart-${name}.digest"
    [[ -f "${file}" ]] || fail "missing chart digest file ${file}"
    digest=$(<"${file}")
    add_artifact "chart-${name}" "${STAGING_REGISTRY}/charts/${name}" "${digest}"
  done
}

# install_cosign downloads the pinned cosign release into $1 and checks its sha256.
install_cosign() {
  local dir=$1
  [[ "$(uname -s)-$(uname -m)" == Linux-x86_64 ]] || fail "cosign is pinned for linux-amd64, this host is $(uname -s)-$(uname -m)"
  COSIGN="${dir}/cosign"
  curl --fail --location --silent --show-error \
    --output "${COSIGN}" \
    "https://github.com/sigstore/cosign/releases/download/${COSIGN_VERSION}/cosign-linux-amd64"
  printf '%s  %s\n' "${COSIGN_LINUX_AMD64_SHA256}" "cosign" > "${dir}/cosign.sha256"
  (cd "${dir}" && sha256sum -c cosign.sha256) || fail "cosign ${COSIGN_VERSION} sha256 mismatch"
  chmod +x "${COSIGN}"
}

# attest_artifact attaches the predicate $3 to ref@digest $2, verifies the attestations
# against the build identity, and checks that one of them has the predicate.
attest_artifact() {
  local label=$1 ref_digest=$2 predicate=$3 work=$4
  local verified="${work}/${label}.verified.json"

  "${COSIGN}" attest --yes --type slsaprovenance1 --predicate "${predicate}" "${ref_digest}" ||
    fail "cosign attest failed for ${label}"
  "${COSIGN}" verify-attestation --type slsaprovenance1 \
    --certificate-identity "${SIGNER_IDENTITY}" \
    --certificate-oidc-issuer "${OIDC_ISSUER}" \
    "${ref_digest}" > "${verified}" || fail "cosign verify-attestation failed for ${label}"

  # A digest can hold several attestations, for example after a rebuild of the same ref.
  # Pass when at least one verified attestation has exactly the predicate that this build
  # wrote. The predicate includes the build ID, so an attestation from an earlier build
  # does not match.
  python3 -I - "${verified}" "${predicate}" <<'PY' || fail "self-check failed for ${label}"
import base64, json, sys
text = open(sys.argv[1], encoding="utf-8").read()
expected = json.load(open(sys.argv[2], encoding="utf-8"))
decoder = json.JSONDecoder()
envelopes, index = [], 0
while index < len(text):
    if text[index].isspace():
        index += 1
        continue
    envelope, index = decoder.raw_decode(text, index)
    envelopes.append(envelope)
if not envelopes:
    sys.exit("verify-attestation printed no attestation")
for envelope in envelopes:
    statement = json.loads(base64.b64decode(envelope["payload"]))
    if statement.get("predicate") == expected:
        sys.exit(0)
sys.exit(f"none of {len(envelopes)} verified attestation(s) has the predicate this build wrote")
PY
  echo "attested ${label} ${ref_digest}"
}

main() {
  : "${DIGEST_DIR:?DIGEST_DIR must be set}"
  : "${SIGN:?SIGN must be set to true or false}"
  : "${PULL_BASE_REF:?PULL_BASE_REF must be set}"
  : "${BUILD_ID:?BUILD_ID must be set}"
  : "${PROJECT_ID:?PROJECT_ID must be set}"
  : "${PROJECT_NUMBER:?PROJECT_NUMBER must be set}"
  case "${SIGN}" in
    true | false) ;;
    *) fail "SIGN must be true or false, got ${SIGN}" ;;
  esac
  [[ -d "${DIGEST_DIR}" ]] || fail "DIGEST_DIR ${DIGEST_DIR} does not exist"
  require_tools

  local commit i predicate
  commit=$(git -C "${ROOT}" rev-parse HEAD) || fail "cannot read the commit of ${ROOT}"
  PREDICATE_DIR="${PREDICATE_DIR:-${DIGEST_DIR}/predicates}"

  collect_artifacts
  mkdir -p "${PREDICATE_DIR}"
  for i in "${!LABELS[@]}"; do
    predicate="${PREDICATE_DIR}/${LABELS[$i]}.predicate.json"
    python3 -I "${PROVENANCE}" predicate \
      --ref "${PULL_BASE_REF}" \
      --commit "${commit}" \
      --build-id "${BUILD_ID}" \
      --project "${PROJECT_ID}" \
      --project-number "${PROJECT_NUMBER}" > "${predicate}"
    if [[ "${SIGN}" == false ]]; then
      echo "dry run (SIGN=false): ${LABELS[$i]} ${REFS[$i]}@${DIGESTS[$i]}"
      cat "${predicate}"
    fi
  done

  if [[ "${SIGN}" == false ]]; then
    return 0
  fi

  # WORK_DIR is global: the EXIT trap runs after main has returned.
  WORK_DIR=$(mktemp -d)
  trap 'rm -rf "${WORK_DIR}"' EXIT
  if [[ -n "${COSIGN_BIN:-}" ]]; then
    COSIGN="${COSIGN_BIN}"
  else
    install_cosign "${WORK_DIR}"
  fi
  for i in "${!LABELS[@]}"; do
    predicate="${PREDICATE_DIR}/${LABELS[$i]}.predicate.json"
    attest_artifact "${LABELS[$i]}" "${REFS[$i]}@${DIGESTS[$i]}" "${predicate}" "${WORK_DIR}"
  done
}

main "$@"
