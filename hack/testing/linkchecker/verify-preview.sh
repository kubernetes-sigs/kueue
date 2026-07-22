#!/usr/bin/env bash

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

# Runs the website link checker against a PR's Netlify deploy preview.
#
# The deploy/netlify commit status on the PR head SHA gates freshness; once it
# succeeds, links are checked against the PR's deploy-preview alias. If no fresh
# same-commit preview can be resolved, this script skips successfully; otherwise
# the link checker runs and its result is preserved.
#
# The resolved Netlify URL is a mutable per-PR preview alias, not an immutable
# per-deploy URL, but the status itself is attached to the exact SHA under test.

set -o errexit
set -o nounset
set -o pipefail

SOURCE_DIR="$(cd "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT_DIR="${SOURCE_DIR}/../../.."

# Prow injects these on presubmits; default the repo coordinates for safety.
PULL_NUMBER="${PULL_NUMBER:-}"
PULL_PULL_SHA="${PULL_PULL_SHA:-}"
PULL_BASE_SHA="${PULL_BASE_SHA:-}"
REPO_OWNER="${REPO_OWNER:-kubernetes-sigs}"
REPO_NAME="${REPO_NAME:-kueue}"

GH_API="${GH_API:-https://api.github.com}"
STATUS_CONTEXT="deploy/netlify"
# Netlify site slug for the PR's deploy-preview URL (built below).
NETLIFY_SITE="${NETLIFY_SITE:-kubernetes-sigs-kueue}"
# Bounded wait for the preview build: 20 checks × 30s (~9.5 min), then skip.
PREVIEW_WAIT_ATTEMPTS="${PREVIEW_WAIT_ATTEMPTS:-20}"
PREVIEW_WAIT_DELAY="${PREVIEW_WAIT_DELAY:-30}"

log()  { echo "[verify-website-links-preview] $*"; }
skip() { log "SKIP: $*"; log "Treating as success (non-blocking link check)."; exit 0; }

# Outside a Prow presubmit (e.g. a local run) there is no PR preview to resolve.
if [[ -z "${PULL_NUMBER}" || -z "${PULL_PULL_SHA}" ]]; then
  skip "not running in a Prow presubmit (PULL_NUMBER/PULL_PULL_SHA unset)."
fi

# If the PR does not touch site/, Netlify builds no preview (the netlify.toml
# ignore rule), so skip instead of waiting out the timeout. If the diff can't be
# computed, fall through and let the resolution step decide.
if [[ -n "${PULL_BASE_SHA}" ]] && \
   git -C "${ROOT_DIR}" diff --quiet "${PULL_BASE_SHA}" "${PULL_PULL_SHA}" -- site/ 2>/dev/null; then
  skip "PR #${PULL_NUMBER} does not modify site/; no preview expected."
fi

# A missing binary won't appear mid-loop, so short-circuit before polling.
# Still skip (not exit 1) to keep the job non-blocking.
for bin in curl jq; do
  command -v "${bin}" >/dev/null 2>&1 || skip "${bin} is unavailable; cannot resolve deploy preview."
done

# Unauthenticated GitHub API calls are rate-limited (60/h per IP); set GH_TOKEN to
# raise the limit. API failures are treated as skip, so rate limits never fail a PR.
auth=()
if [[ -n "${GH_TOKEN:-}" ]]; then
  auth=(-H "Authorization: Bearer ${GH_TOKEN}")
fi

preview_url=""
for (( attempt=1; attempt<=PREVIEW_WAIT_ATTEMPTS; attempt++ )); do
  body=""
  if ! body="$(curl --connect-timeout 10 --max-time 20 -fsSL "${auth[@]+"${auth[@]}"}" \
      "${GH_API}/repos/${REPO_OWNER}/${REPO_NAME}/commits/${PULL_PULL_SHA}/status" 2>/dev/null)"; then
    log "GitHub API call failed (attempt ${attempt}/${PREVIEW_WAIT_ATTEMPTS})."
    body=""
  fi

  # Guard jq parsing: a non-JSON 200 body (curl -f does not reject it) must not
  # fail the job. Any jq error yields "", routing to the skip path below.
  state=""
  if [[ -n "${body}" ]]; then
    state="$(jq -r --arg c "${STATUS_CONTEXT}" \
      'first(.statuses[]? | select(.context==$c)) | .state // empty' <<<"${body}" 2>/dev/null || true)"
  fi

  case "${state}" in
    success)
      # target_url here is Netlify's admin deploy page, not the rendered preview,
      # so build the PR's deploy-preview alias instead.
      preview_url="https://deploy-preview-${PULL_NUMBER}--${NETLIFY_SITE}.netlify.app/"
      break
      ;;
    failure|error)
      skip "Netlify deploy preview '${state}' for commit ${PULL_PULL_SHA}; nothing fresh to check."
      ;;
    *)
      log "preview not ready (state='${state:-none}'), attempt ${attempt}/${PREVIEW_WAIT_ATTEMPTS}."
      ;;
  esac

  if (( attempt < PREVIEW_WAIT_ATTEMPTS )); then
    sleep "${PREVIEW_WAIT_DELAY}"
  fi
done

if [[ -z "${preview_url}" ]]; then
  skip "no ready same-commit deploy preview for ${PULL_PULL_SHA} after bounded wait."
fi

log "checking fresh preview for commit ${PULL_PULL_SHA}: ${preview_url}"

# Run the link checker against the resolved preview and preserve its result.
LINK_CHECK_URL="${preview_url}" exec "${SOURCE_DIR}/verify.sh"
