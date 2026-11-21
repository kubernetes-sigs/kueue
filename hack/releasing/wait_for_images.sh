#!/usr/bin/env bash

# Copyright 2025 The Kubernetes Authors.
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

set -o errexit
set -o nounset
set -o pipefail

function usage() {
  echo "${0} [-p|--prod] [-t|--timeout <duration>] <version>"
  echo
  echo "  Wait for images"
  echo
  echo "  Options:"
  echo "    -p, --prod               Check production registry (default: staging)"
  echo "    -t, --timeout <duration> Maximum time to wait (e.g., 3600 or 3600s). Default: no timeout."
  echo "    --check-provenance       Also wait until each staging digest has a SLSA provenance attestation"
  echo "                             for the release tag and its commit. Staging only."
  echo
  echo "  Example:"
  echo "    $0 v0.13.2"
  echo "    $0 -p v0.13.2"
  echo "    $0 --prod --timeout 3600s v0.13.2"
  echo "    $0 --check-provenance --timeout 3600s v0.13.2"
  echo
  exit 2
}

IMAGE_REGISTRY="us-central1-docker.pkg.dev/k8s-staging-images/kueue"
TIMEOUT_SECONDS=0
CHECK_PROVENANCE=false
TAG_COMMIT=""
LAST_PROVENANCE_STATE=""
LAST_LOGGED_COSIGN_ERROR=""

while [[ $# -gt 0 ]]; do
  case $1 in
    -p|--prod)
      IMAGE_REGISTRY="registry.k8s.io/kueue"
      shift
      ;;
    --check-provenance)
      CHECK_PROVENANCE=true
      shift
      ;;
    -t|--timeout)
      if [[ -z "${2:-}" ]]; then
        echo "!!! Error: --timeout requires an argument."
        usage
      fi
      # Strip trailing 's' if present (e.g., 3600s -> 3600)
      TIMEOUT_SECONDS="${2%s}"
      if [[ ! "$TIMEOUT_SECONDS" =~ ^[0-9]+$ ]]; then
        echo "!!! Error: Timeout must be a positive integer."
        exit 1
      fi
      shift 2
      ;;
    -*)
      usage
      ;;
    *)
      break
      ;;
  esac
done

if [[ "$#" -ne 1 ]]; then
  usage
fi

declare -r RELEASE_VERSION="$1"

if [[ ! "$RELEASE_VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9]+\.[0-9]+)?$ ]]; then
  echo "!!! Invalid release version. It should be semantic version like v0.13.2 or v0.13.2-rc.0"
  exit 1
fi

if ! command -v crane >/dev/null 2>&1; then
  echo "!!! crane is not installed. Please install it or use the github action step."
  exit 1
fi

if [[ "${CHECK_PROVENANCE}" == true && "${IMAGE_REGISTRY}" == registry.k8s.io/* ]]; then
  echo "!!! Error: --check-provenance applies to staging images only and cannot be used with --prod."
  exit 1
fi

if [[ "${CHECK_PROVENANCE}" == true ]]; then
  for tool in cosign git python3; do
    if ! command -v "${tool}" >/dev/null 2>&1; then
      echo "!!! ${tool} is not installed. It is required by --check-provenance."
      exit 1
    fi
  done
fi

SCRIPT_DIR="$(dirname "${BASH_SOURCE[0]}")"
# shellcheck source=hack/utils.sh
source "${SCRIPT_DIR}/../utils.sh"

KUEUE_REPO_URL="https://github.com/kubernetes-sigs/kueue"
ATTESTATION_IDENTITY="kueue-sa@k8s-staging-images.iam.gserviceaccount.com"
ATTESTATION_OIDC_ISSUER="https://accounts.google.com"
IMAGES_YAML_URL="https://raw.githubusercontent.com/kubernetes/k8s.io/main/registry.k8s.io/images/k8s-staging-kueue/images.yaml"
TMP_IMAGES_FILE=$(mktemp)
declare -r TMP_IMAGES_FILE

function cleanup {
  rm -f "${TMP_IMAGES_FILE}"
}
trap cleanup EXIT

if ! curl -sSLo "${TMP_IMAGES_FILE}" "${IMAGES_YAML_URL}"; then
  echo "!!! Warning: Failed to download images.yaml from github. Version checks may be skipped."
  rm -f "${TMP_IMAGES_FILE}"
fi

# Runs a command with a time limit, so that a hung registry or remote cannot stall a poll.
# The release runner provides GNU timeout. A machine without it runs the command unbounded.
# $1 - limit in seconds
# $2... - command and its arguments
function run_bounded() {
  local limit="$1"
  shift
  if command -v timeout >/dev/null 2>&1; then
    timeout "${limit}" "$@"
  else
    "$@"
  fi
}

# Returns 0 once --timeout has passed. Without --timeout it returns 1.
function timed_out() {
  [[ "${TIMEOUT_SECONDS}" -gt 0 && "${SECONDS}" -ge "${TIMEOUT_SECONDS}" ]]
}

# Prints the time limit for one call: the given limit, or the time left before --timeout
# when that is shorter. Without --timeout it prints the given limit.
# $1 - limit in seconds
function call_limit() {
  local limit="$1"
  if [[ "${TIMEOUT_SECONDS}" -gt 0 && $((TIMEOUT_SECONDS - SECONDS)) -lt "${limit}" ]]; then
    limit=$((TIMEOUT_SECONDS - SECONDS))
  fi
  echo "${limit}"
}

# Prints the commit that the release tag points to. An annotated tag resolves to its
# peeled commit, so the ^{} line is preferred over the tag object on the plain line.
# $1 - release tag
function resolve_tag_commit() {
  local tag="$1"
  local limit refs
  limit=$(call_limit 60)
  [[ "${limit}" -gt 0 ]] || return 1
  refs=$(run_bounded "${limit}" git ls-remote "${KUEUE_REPO_URL}" "refs/tags/${tag}" "refs/tags/${tag}^{}") || return 1

  local peeled plain
  peeled=$(awk -v ref="refs/tags/${tag}^{}" '$2 == ref { print $1 }' <<< "${refs}")
  plain=$(awk -v ref="refs/tags/${tag}" '$2 == ref { print $1 }' <<< "${refs}")
  local commit="${peeled:-${plain}}"
  [[ "${commit}" =~ ^[0-9a-f]{40}$ ]] || return 1
  echo "${commit}"
}

# Returns 0 when at least one verified attestation of the digest records the release tag
# and its commit, and 1 when the caller should poll again.
# A digest can have several attestations, for example from a rerun of the same build. A
# stale attestation that is NOT_READY does not block a digest with a PASS attestation.
# Any attestation that FAILs exits 1 at once, whatever the others say.
# $1 - image or chart reference without a digest
# $2 - digest of the reference
function verify_attestation() {
  local ref="$1"
  local digest="$2"
  local attestations errors cosign_error cosign_limit
  local cosign_status=0
  cosign_limit=$(call_limit 120)
  if [[ "${cosign_limit}" -le 0 ]]; then
    # The timeout has passed. Keep the last observed reason, which can come from an earlier poll.
    [[ -n "${LAST_PROVENANCE_STATE}" ]] || LAST_PROVENANCE_STATE="${ref}@${digest} for commit ${TAG_COMMIT}: not checked before the timeout"
    return 1
  fi
  errors=$(mktemp)
  attestations=$(run_bounded "${cosign_limit}" cosign verify-attestation --type slsaprovenance1 \
    --certificate-identity "${ATTESTATION_IDENTITY}" \
    --certificate-oidc-issuer "${ATTESTATION_OIDC_ISSUER}" \
    "${ref}@${digest}" 2>"${errors}") || cosign_status=$?
  if [[ "${cosign_status}" -ne 0 ]]; then
    attestations=""
  fi
  # cosign prints the reason for a missing attestation on stderr, such as "no matching
  # attestations". Keep its last lines, flattened, so that the log and timeout show why.
  cosign_error=$(tail -n 3 "${errors}" | tr '\n' ' ' | sed 's/ *$//')
  rm -f "${errors}"

  if [[ -z "${attestations}" ]]; then
    # 124 is the status of GNU timeout when it kills cosign.
    local reason="${cosign_error:-cosign printed no attestation}"
    if [[ "${cosign_status}" -eq 124 ]]; then
      reason="cosign verify-attestation timed out after ${cosign_limit}s"
    fi
    LAST_PROVENANCE_STATE="${ref}@${digest} for commit ${TAG_COMMIT}: no verified attestation: ${reason}"
    # Log the reason on the first attempt and when it changes, not on every poll.
    if [[ "${reason}" != "${LAST_LOGGED_COSIGN_ERROR}" ]]; then
      echo "    No verified SLSA provenance attestation yet: ${reason}"
      LAST_LOGGED_COSIGN_ERROR="${reason}"
    fi
    return 1
  fi

  local line payload status result
  local passed=0
  while IFS= read -r line; do
    # Each line is one DSSE envelope. Its base64 payload is the in-toto statement.
    payload=$(python3 -c 'import json, sys; print(json.load(sys.stdin).get("payload", ""))' 2>/dev/null <<< "${line}") || payload=
    status=0
    result=$(printf '%s' "${payload}" | python3 "${SCRIPT_DIR}/provenance.py" check \
      --tag "${RELEASE_VERSION}" --commit "${TAG_COMMIT}" --payload - 2>&1) || status=$?
    echo "    ${result}"
    case "${status}" in
      0)
        passed=$((passed + 1))
        ;;
      2)
        LAST_PROVENANCE_STATE="${ref}@${digest} for commit ${TAG_COMMIT}: ${result}"
        ;;
      *)
        echo "!!! Error: SLSA provenance check failed for ${ref}@${digest}: ${result}"
        exit 1
        ;;
    esac
  done <<< "${attestations}"

  if [[ "${passed}" -gt 0 ]]; then
    return 0
  fi
  return 1
}

# $1 - image name
# $2 - version
function check_image() {
  local image_name="$1"
  local version="$2"
  local full_image_name="${IMAGE_REGISTRY}/${image_name}:${version}"
  echo "  Checking if \"${full_image_name}\" is available."

  local digest
  if digest=$(crane digest "${full_image_name}" 2>/dev/null); then
    echo "    ✅ Image \"${full_image_name}@${digest}\" is available."
    echo "    ${digest}"
    if [[ "${CHECK_PROVENANCE}" == true ]] && ! verify_attestation "${full_image_name}" "${digest}"; then
      return 1
    fi
    return 0
  else
    echo "    🚫 Image \"${full_image_name}\" is not found."
    if [[ "${CHECK_PROVENANCE}" == true ]]; then
      LAST_PROVENANCE_STATE="${full_image_name} is not found"
    fi
    return 1
  fi
}

function check_images() {
  # Once --timeout has passed, a poll makes no calls, so the last reason is still reported.
  if [[ "${CHECK_PROVENANCE}" == true ]] && timed_out; then
    return 1
  fi
  if [[ "${CHECK_PROVENANCE}" == true ]]; then
    # The tag can move while waiting, so it is resolved again on every poll.
    if ! TAG_COMMIT=$(resolve_tag_commit "${RELEASE_VERSION}"); then
      LAST_PROVENANCE_STATE="tag ${RELEASE_VERSION} could not be resolved in ${KUEUE_REPO_URL}"
      echo "  Release tag \"${RELEASE_VERSION}\" could not be resolved in ${KUEUE_REPO_URL}."
      return 1
    fi
    echo "  Release tag \"${RELEASE_VERSION}\" points to commit ${TAG_COMMIT}."
  fi

  local images=(
      kueue
      kueueviz-backend
      kueueviz-frontend
      kueue-populator
      kueue-priority-booster
  )

  local charts=(
      kueue
      kueue-populator
      kueue-priority-booster
  )

  echo "Images:"
  for image in "${images[@]}"; do
    if should_skip_image "${image}" "${RELEASE_VERSION}" "${TMP_IMAGES_FILE}"; then
      echo "  Skipping \"${image}\" check (introduced in later version)."
      continue
    fi
    echo ""
    if ! check_image "${image}" "${RELEASE_VERSION}"; then
      return 1
    fi
  done

  echo ""
  echo "Charts:"
  for chart in "${charts[@]}"; do
    if should_skip_image "charts/${chart}" "${RELEASE_VERSION}" "${TMP_IMAGES_FILE}"; then
      echo "  Skipping \"charts/${chart}\" check (introduced in later version)."
      continue
    fi
    echo ""
    if ! check_image "charts/${chart}" "${RELEASE_VERSION#v}"; then
      return 1
    fi
  done

  if [[ "${CHECK_PROVENANCE}" == true ]]; then
    # The tag can move while the artifacts are verified. Accept the poll only when the tag
    # still points to the commit that the attestations were checked against.
    if timed_out; then
      LAST_PROVENANCE_STATE="artifacts verified for commit ${TAG_COMMIT}, but tag ${RELEASE_VERSION} was not rechecked before the timeout"
      return 1
    fi
    local recheck_commit
    recheck_commit=$(resolve_tag_commit "${RELEASE_VERSION}") || recheck_commit=""
    if [[ -z "${recheck_commit}" ]]; then
      LAST_PROVENANCE_STATE="tag ${RELEASE_VERSION} could not be resolved in ${KUEUE_REPO_URL} after verifying artifacts"
      echo "  Release tag \"${RELEASE_VERSION}\" could not be resolved while verifying; checking again."
      return 1
    fi
    if [[ "${recheck_commit}" != "${TAG_COMMIT}" ]]; then
      LAST_PROVENANCE_STATE="tag ${RELEASE_VERSION} moved from ${TAG_COMMIT} to ${recheck_commit} while verifying"
      echo "  Release tag \"${RELEASE_VERSION}\" moved from ${TAG_COMMIT} to ${recheck_commit} while verifying; checking again."
      return 1
    fi
  fi
}

# The built-in $SECONDS variable resets to 0 here to keep time tracking accurate
SECONDS=0

while true; do
  if check_images; then
    break
  fi

  # Verify if a timeout was requested and if we exceeded it
  if [[ "$TIMEOUT_SECONDS" -gt 0 && "$SECONDS" -ge "$TIMEOUT_SECONDS" ]]; then
    # Keep the message on one line: the release workflow reads the "!!! Error: " lines of the log.
    timeout_message="!!! Error: Timed out waiting for images after ${TIMEOUT_SECONDS}s."
    if [[ -n "${LAST_PROVENANCE_STATE}" ]]; then
      timeout_message+=" Last SLSA provenance state: ${LAST_PROVENANCE_STATE}."
    fi
    echo "${timeout_message}"
    exit 1
  fi

  echo "Try again in 5 seconds..."
  sleep 5
done