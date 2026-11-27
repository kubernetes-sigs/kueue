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

KUBERNETES_SIGS_KUEUE_PATH="$(git rev-parse --show-toplevel)"
declare -r KUBERNETES_SIGS_KUEUE_PATH

cd "$KUBERNETES_SIGS_KUEUE_PATH"

# shellcheck source=hack/utils.sh
source "${KUBERNETES_SIGS_KUEUE_PATH}/hack/utils.sh"

# Build the pinned yq via the `yq` make target so the script does not rely on
# the user's environment.
make -C "${KUBERNETES_SIGS_KUEUE_PATH}" yq >/dev/null
YQ="${KUBERNETES_SIGS_KUEUE_PATH}/bin/yq"
declare -r YQ

KUBERNETES_SIGS_KUEUE_UPSTREAM_REMOTE=${KUBERNETES_SIGS_KUEUE_UPSTREAM_REMOTE:-upstream}
KUBERNETES_SIGS_KUEUE_FORK_REMOTE=${KUBERNETES_SIGS_KUEUE_FORK_REMOTE:-origin}
KUBERNETES_SIGS_KUEUE_MAIN_REPO_ORG=${KUBERNETES_SIGS_KUEUE_MAIN_REPO_ORG:-$(get_repo_org "$(git remote get-url "$KUBERNETES_SIGS_KUEUE_UPSTREAM_REMOTE")")}
KUBERNETES_SIGS_KUEUE_MAIN_REPO_NAME=${KUBERNETES_SIGS_KUEUE_MAIN_REPO_NAME:-$(get_repo_name "$(git remote get-url "$KUBERNETES_SIGS_KUEUE_UPSTREAM_REMOTE")")}
KUBERNETES_SIGS_KUEUE_MAIN_REPO="${KUBERNETES_SIGS_KUEUE_MAIN_REPO_ORG}/${KUBERNETES_SIGS_KUEUE_MAIN_REPO_NAME}"

if [[ -v KUBERNETES_REPOS_PATH ]]; then
  KUBERNETES_REPOS_PATH=$(resolve_path "${KUBERNETES_REPOS_PATH}")
  if [[ ! -d "${KUBERNETES_REPOS_PATH}" ]]; then
    echo "!!! Invalid value for KUBERNETES_REPOS_PATH: the path \"${KUBERNETES_REPOS_PATH}\" does not exist."
    exit 1
  fi
else
  KUBERNETES_REPOS_PATH="$(resolve_path "${KUBERNETES_SIGS_KUEUE_PATH}/../../kubernetes")"
fi
declare -r KUBERNETES_REPOS_PATH

if [[ -v KUBERNETES_K8S_IO_PATH ]]; then
  KUBERNETES_K8S_IO_PATH=$(resolve_path "${KUBERNETES_K8S_IO_PATH}")
  if [[ ! -d "${KUBERNETES_K8S_IO_PATH}" ]]; then
    echo "!!! Invalid value for KUBERNETES_K8S_IO_PATH: the path \"${KUBERNETES_K8S_IO_PATH}\" does not exist."
    exit 1
  fi
else
  KUBERNETES_K8S_IO_PATH="${KUBERNETES_REPOS_PATH}/k8s.io"
fi
declare -r KUBERNETES_K8S_IO_PATH

K8S_STAGING_KUEUE_PATH="${KUBERNETES_K8S_IO_PATH}/registry.k8s.io/images/k8s-staging-kueue"
if [[ ! -d "${K8S_STAGING_KUEUE_PATH}" ]]; then
  echo "!!! The path \"${K8S_STAGING_KUEUE_PATH}\" does not exist."
  exit 1
fi
declare -r K8S_STAGING_KUEUE_PATH

cd "${K8S_STAGING_KUEUE_PATH}"

STARTING_BRANCH=$(git symbolic-ref --short HEAD)
declare -r STARTING_BRANCH
declare -r REBASE_MAGIC=".git/rebase-apply"
DRY_RUN=${DRY_RUN:-""}
KUBERNETES_K8S_IO_UPSTREAM_REMOTE=${KUBERNETES_K8S_IO_UPSTREAM_REMOTE:-upstream}
KUBERNETES_K8S_IO_FORK_REMOTE=${KUBERNETES_K8S_IO_FORK_REMOTE:-origin}
KUBERNETES_K8S_IO_MAIN_REPO_ORG=${KUBERNETES_K8S_IO_MAIN_REPO_ORG:-$(get_repo_org "$(git remote get-url "${KUBERNETES_K8S_IO_UPSTREAM_REMOTE}")")}
KUBERNETES_K8S_IO_MAIN_REPO_NAME=${KUBERNETES_K8S_IO_MAIN_REPO_NAME:-$(get_repo_name "$(git remote get-url "${KUBERNETES_K8S_IO_UPSTREAM_REMOTE}")")}
KUBERNETES_K8S_IO_MAIN_REPO="${KUBERNETES_K8S_IO_MAIN_REPO_ORG}/${KUBERNETES_K8S_IO_MAIN_REPO_NAME}"

if [[ -z ${GITHUB_USER:-} ]]; then
  echo "Please export GITHUB_USER=<your-user> (or GH organization, if that's where your fork lives)"
  exit 1
fi

if ! command -v gh > /dev/null; then
  echo "Can't find 'gh' tool in PATH, please install from https://github.com/cli/cli"
  exit 1
fi

if ! command -v python3 > /dev/null; then
  echo "Can't find 'python3' tool in PATH, please install Python 3"
  exit 1
fi

CHECK_PROVENANCE=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --check-provenance)
      CHECK_PROVENANCE=1
      shift
      ;;
    *)
      break
      ;;
  esac
done

if [[ "$#" -ne 1 ]]; then
  echo "${0} [--check-provenance] <version>"
  echo
  echo "  Create promote PR"
  echo
  echo "  Example:"
  echo "    $0 v0.13.2"
  echo
  echo "  Set the DRY_RUN environment var to skip git push, PR creation and the release issue update."
  echo "  When DRY_RUN is set the script will leave you in a branch containing the commits."
  echo
  echo "  Set --check-provenance to verify the SLSA provenance of each staging image and chart digest against"
  echo "  the release tag before recording it. The script exits without recording any digest if one fails."
  echo "  Requires cosign v3.0.1 or later."
  echo
  echo "  Set KUBERNETES_K8S_IO_UPSTREAM_REMOTE (default: upstream) and KUBERNETES_K8S_IO_FORK_REMOTE (default: origin)"
  echo "  to override the default remote names to what you have locally."
  echo
  echo "  Set KUBERNETES_REPOS_PATH (default: ../../kubernetes) and KUBERNETES_K8S_IO_PATH (default: ../../kubernetes/k8s.io)"
  echo "  to override the default kubernetes paths to what you have locally."
  exit 2
fi

if [[ -n "${CHECK_PROVENANCE:-}" ]] && ! command -v cosign > /dev/null; then
  echo "Can't find 'cosign' tool in PATH, please install cosign v3.0.1 or later from https://github.com/sigstore/cosign (needed by --check-provenance)"
  exit 1
fi

# gtimeout is GNU timeout as installed on macOS by coreutils.
TIMEOUT_CMD=""
if [[ -n "${CHECK_PROVENANCE:-}" ]]; then
  if command -v timeout > /dev/null; then
    TIMEOUT_CMD="timeout"
  elif command -v gtimeout > /dev/null; then
    TIMEOUT_CMD="gtimeout"
  else
    echo "!!! Warning: neither timeout nor gtimeout found (macOS: brew install coreutils); cosign and git calls run without a time limit." >&2
  fi
fi

# Checks if you are logged in. Will error/bail if you are not.
gh auth status

if git_status=$(git status --porcelain --untracked=no 2>/dev/null) && [[ -n "${git_status}" ]]; then
  echo "!!! Dirty tree. Clean up and try again."
  exit 1
fi

if [[ -e "${REBASE_MAGIC}" ]]; then
  echo "!!! 'git rebase' or 'git am' in progress. Clean up and try again."
  exit 1
fi

declare -r RELEASE_VERSION="$1"

if [[ ! "$RELEASE_VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "!!! Invalid release version. It should be semantic version like v0.13.2"
  exit 1
fi

echo "+++ Updating upstream remote..."
git remote update "${KUBERNETES_K8S_IO_UPSTREAM_REMOTE}"

IFS='.' read -r _ MINOR _ <<< "${RELEASE_VERSION#v}"

LAST_SUPPORT_MINOR=$((MINOR - 1))
if [ "$LAST_SUPPORT_MINOR" -lt 0 ]; then
  LAST_SUPPORT_MINOR=0  # Prevent negative minors
fi

RELEASE_ISSUE_NAME="Release ${RELEASE_VERSION}"

RELEASE_ISSUE_NUMBER=$(gh issue list --repo="${KUBERNETES_SIGS_KUEUE_MAIN_REPO}" --search "in:title ${RELEASE_ISSUE_NAME}" | awk '{print $1}' || true)
if [ -z "$RELEASE_ISSUE_NUMBER" ]; then
  echo "!!! No release issue found for version ${RELEASE_VERSION}. Please create 'Release ${RELEASE_VERSION}' issue first."
  exit 1
fi

RELEASE_ISSUE=$(gh issue view "${RELEASE_ISSUE_NUMBER}" --repo="${KUBERNETES_SIGS_KUEUE_MAIN_REPO}" --json body || true)
if [ -z "$RELEASE_ISSUE" ]; then
  echo "!!! No release issue found for version ${RELEASE_VERSION}. Please create 'Release ${RELEASE_VERSION}' issue first."
  exit 1
fi

RELEASE_ISSUE_BODY=$(echo "${RELEASE_ISSUE}" | jq -r '.body')

clean_branches=()
PROVENANCE_FAILED=""
function cleanup {
  # Return to the starting branch and delete specified branches
  echo
  echo "+++ Returning to the ${STARTING_BRANCH} branch."
  git checkout -f "${STARTING_BRANCH}" >/dev/null 2>&1 || {
    echo "!!! Failed to return to ${STARTING_BRANCH}. Please check your git status."
    return 1
  }

  if [[ ${#clean_branches[@]} -eq 0 ]]; then
    echo "!!! No branches specified for cleanup."
    return 0
  fi

  for branch in "${clean_branches[@]}"; do
    if [[ -n "${branch}" && ( -z "${DRY_RUN}" || -n "${PROVENANCE_FAILED}" ) ]]; then
      echo "!!! Deleting branch ${branch}."
      git branch -D "${branch}" >/dev/null 2>&1 || {
        echo "!!! Failed to delete branch ${branch}. It may not exist or is currently checked out."
      }
    else
      echo "!!! Skipping deletion of branch ${branch} because DRY_RUN is set."
      echo "To delete this branch manually:"
      echo "  git branch -D ${branch}"
    fi
  done
}
trap cleanup EXIT

# $1 - base branch
# $2 - remote branch
# $3 - pr name
function make_pr() {
  local rel
  rel="$(basename "$1")"

  echo
  echo "+++ Creating a pull request on GitHub repo ${KUBERNETES_K8S_IO_MAIN_REPO} at ${GITHUB_USER}:$2 for ${rel}"

  pr_text=$(cat <<EOF
#### What this PR does / why we need it:
$3.

#### Which issue(s) this PR fixes:
Part of ${KUBERNETES_SIGS_KUEUE_MAIN_REPO}#${RELEASE_ISSUE_NUMBER}
EOF
)

   gh pr create --title="$3" --body="${pr_text}" --head "${GITHUB_USER}:$2" --base "${rel}" --repo="${KUBERNETES_K8S_IO_MAIN_REPO}"
}

# $1 - images file
# $2 - release version
# $3 - digest
# $4 - component
function insert_image() {
  local images_file="$1"
  local release_version="$2"
  local digest="$3"
  local component="$4"

  echo "+++ Insert image $component@$release_version:$digest"

  tmp_file=$(mktemp)

  awk -v component="${component}" -v release_version="${release_version}" -v digest="${digest}" '
    BEGIN {
      new_line = sprintf("    \"%s\": [\"%s\"]", digest, release_version);
      found_component = 0;
      in_component = 0;
      inserted = 0;

      # Remove "v" prefix
      core_release_version = gsub(/^v/, "", version);

      # Split release_version into major, minor, patch
      split(release_version, vref, ".");

      ref_major = vref[1] + 0;  # Convert to number
      ref_minor = vref[2] + 0;
      ref_patch = vref[3] + 0;
    }

    # Skip empty lines
    /^[[:space:]]*$/ {
       next;
    }

    # Match lines starting with "- name: " followed by the component name
    /^[[:space:]]*- name: / {
      if (in_component && !inserted) {
        print new_line;
      }

      found_component = 1;
      in_component = ($3 == component) ? 1 : 0;
      inserted = 0;

      print;
      next;
    }

    # Insert the new entry before the line containing the version reference
    in_component && !inserted && $0 ~ /v?[0-9]+\.[0-9]+\.[0-9]+/ {
      line = $0;

      # Remove everything before the array
      sub(/.*:[[:space:]]*\[/, "", line);
      # Remove trailing ] and anything after
      sub(/\][[:space:]]*.*$/, "", line);

      # Split into versions (e.g., "v0.1.0", "v0.1.12")
      n = split(line, versions, /,[[:space:]]*/);

      for (i = 1; i <= n; i++) {
        version = versions[i];

        gsub(/^"/, "", version);
        gsub(/"$/, "", version);

        # Skip empty versions
        if (version == "") {
          continue;
        }

        # Check if version matches vMAJOR.MINOR.PATCH
        if (version ~ /^v?[0-9]+\.[0-9]+\.[0-9]+$/) {
          # Remove "v" prefix
          gsub(/^v/, "", version);

          # Split version into components
          split(version, v, "\.");

          major = v[1] + 0;
          minor = v[2] + 0;
          patch = v[3] + 0;

          # Compare with release_version
          if (major < ref_major ||
              (major == ref_major && minor < ref_minor) ||
              (major == ref_major && minor == ref_minor && patch < ref_patch)) {
              print new_line;
              inserted = 1;
              break;
          }
        }
      }
      print;
      next;
    }
    # Print all other lines unchanged
    { print; }
  ' "${images_file}" > "$tmp_file"

  mv "$tmp_file" "$IMAGES_FILE"
}

declare -r IMAGES_FILE="images.yaml"
declare -r STAGING_IMAGE_REGISTRY="us-central1-docker.pkg.dev/k8s-staging-images/kueue"
declare -r STAGING_BUILD_IDENTITY="kueue-sa@k8s-staging-images.iam.gserviceaccount.com"
declare -r OIDC_ISSUER="https://accounts.google.com"
declare -r PROVENANCE_SCRIPT="${KUBERNETES_SIGS_KUEUE_PATH}/hack/releasing/provenance.py"

# $1 - image name
# $1 - version
function get_image_full_name() {
  local image_name="$1"
  local version="$2"
  local full_image_name="${STAGING_IMAGE_REGISTRY}/${image_name}:${version}"
  echo "${full_image_name}"
}

# $1 - full image name
function get_image_details() {
  local full_image_name="$1"
  gcloud container images describe "${full_image_name}" --verbosity error --format json || true
}

# Runs a command with a time limit using TIMEOUT_CMD (timeout, or gtimeout on macOS).
# Without either tool, it runs without a time limit.
# $1 - seconds
# $@ - command
function run_with_timeout() {
  local seconds="$1"
  shift
  if [[ -n "${TIMEOUT_CMD}" ]]; then
    "${TIMEOUT_CMD}" "${seconds}" "$@"
  else
    "$@"
  fi
}

# Prints the reason and exits. The promote branch is removed even under DRY_RUN.
# $1 - reason
function fail_provenance() {
  PROVENANCE_FAILED=1
  echo "!!! $1"
  exit 1
}

# Verifies the attestations of one staging image or chart digest against the release tag.
# The digest passes when at least one attestation passes and none fails.
# $1 - image name
# $2 - version
# $3 - digest
function verify_provenance() {
  local name="$1"
  local version="$2"
  local digest="$3"
  local full_image_name target
  full_image_name=$(get_image_full_name "$name" "$version")
  target="${full_image_name}@${digest}"

  echo "+++ Verify provenance of ${target} at commit ${RELEASE_COMMIT}"

  local cosign_stderr attestations cosign_error status=0
  cosign_stderr=$(mktemp)
  attestations=$(run_with_timeout 120 cosign verify-attestation --type slsaprovenance1 \
    --certificate-identity "${STAGING_BUILD_IDENTITY}" \
    --certificate-oidc-issuer "${OIDC_ISSUER}" \
    "${STAGING_IMAGE_REGISTRY}/${name}@${digest}" 2> "${cosign_stderr}") || status=$?
  cosign_error=$(< "${cosign_stderr}")
  rm -f "${cosign_stderr}"
  cosign_error="${cosign_error//$'\n'/ }"
  if [[ "${status}" -ne 0 || -z "${attestations}" ]]; then
    fail_provenance "No verified attestation for ${target} (commit ${RELEASE_COMMIT}), cosign exit ${status}: ${cosign_error:-no output}"
  fi

  local attestation result error outcome stderr_file
  local passes=0 failures=0 reasons=""
  while IFS= read -r attestation; do
    stderr_file=$(mktemp)
    status=0
    result=$(jq -r '.payload' <<< "${attestation}" | run_with_timeout 60 python3 "${PROVENANCE_SCRIPT}" check \
      --tag "${RELEASE_VERSION}" --commit "${RELEASE_COMMIT}" 2> "${stderr_file}") || status=$?
    error=$(< "${stderr_file}")
    rm -f "${stderr_file}"
    result="${result//$'\n'/ }"
    error="${error//$'\n'/ }"
    outcome="${result:-exit ${status}}"
    if [[ "${status}" -gt 2 && -n "${result}" ]]; then
      outcome="exit ${status}: ${result}"
    fi
    case "${status}" in
      0) passes=$((passes + 1)) ;;
      2) ;;  # NOT_READY: a stale attestation from an earlier build of a moved tag
      *) failures=$((failures + 1)) ;;
    esac
    echo "    ${outcome}${error:+ ${error}}"
    reasons+="${outcome}${error:+ (${error})}; "
  done <<< "${attestations}"

  if [[ "${failures}" -ne 0 || "${passes}" -eq 0 ]]; then
    fail_provenance "Provenance of ${target} does not match commit ${RELEASE_COMMIT}: ${reasons%; }"
  fi
  echo "    ${passes} attestation(s) PASS"
}

# $1 - base branch
# $2 - local branch
# $3 - commit message
function prepare_local_branch() {
  echo "+++ Creating local branch $2"
  git checkout -b "$2" "${KUBERNETES_K8S_IO_UPSTREAM_REMOTE}/$1"
  clean_branches+=("$2")

  local names=()
  local versions=()
  local digests=()
  while IFS= read -r name; do
    if should_skip_image "${name}" "${RELEASE_VERSION}" "${IMAGES_FILE}"; then
      echo "+++ Skipping ${name} on historical branch release v0.${MINOR}"
      continue
    fi

    version="$RELEASE_VERSION"
    if [[ "${name}" == "charts/kueue" || "${name}" == "charts/kueue-populator" || "${name}" == "charts/kueue-priority-booster" ]]; then
      version="${version#v}"
    fi

    full_image_name=$(get_image_full_name "$name" "$version")
    image_details=$(get_image_details "$full_image_name")
    if [ -z "$image_details" ]; then
      echo " !!! Image \"${full_image_name}\" is not found."
      exit 1
    fi
    digest=$(echo "$image_details" | jq -r '.image_summary.digest')
    names+=("$name")
    versions+=("$version")
    digests+=("$digest")
  done < <("${YQ}" e '.[] | .name' "${IMAGES_FILE}")

  # Verify every digest before the first insert, so a failure leaves the manifest unchanged.
  local i
  if [[ -n "${CHECK_PROVENANCE:-}" ]]; then
    for ((i = 0; i < ${#names[@]}; i++)); do
      verify_provenance "${names[i]}" "${versions[i]}" "${digests[i]}"
    done
  fi

  for ((i = 0; i < ${#names[@]}; i++)); do
    insert_image "$IMAGES_FILE" "${versions[i]}" "${digests[i]}" "${names[i]}"
  done

  git add .
  git commit -m "$3"
}

# $1 - base branch
# $2 - remote branch
# $3 - local branch
# $4 - pr name
function push_and_create_pr() {
  echo
  echo "+++ I'm about to do the following to push to GitHub (and I'm assuming ${KUBERNETES_K8S_IO_FORK_REMOTE} is your personal fork):"
  echo
  echo "  git push ${KUBERNETES_K8S_IO_FORK_REMOTE} ${3}:${2}"
  echo

  read -p "+++ Proceed (anything other than 'y' aborts it)? [y/N] " -r
  if ! [[ "${REPLY}" =~ ^[yY]$ ]]; then
    echo "Aborting." >&2
    exit 1
  else
    git push "${KUBERNETES_K8S_IO_FORK_REMOTE}" -f "${3}:${2}"
    make_pr "$1" "$2" "$4"
  fi
}

K8S_IO_BRANCH="kueue-promote-${RELEASE_VERSION}"
declare -r K8S_IO_BRANCH
K8S_IO_BRANCH_UNIQUE="${K8S_IO_BRANCH}-$(date +%s)"
declare -r K8S_IO_BRANCH_UNIQUE
K8S_IO_PR_NAME="Kueue: Promote ${RELEASE_VERSION}"
declare -r K8S_IO_PR_NAME

if [[ -n "${CHECK_PROVENANCE:-}" ]]; then
  # ls-remote lists an annotated tag's commit on a line ending in ^{}, so prefer that line.
  # The glob is needed because a plain tag pattern does not match the ^{} line.
  refs=$(run_with_timeout 60 git -C "${KUBERNETES_SIGS_KUEUE_PATH}" ls-remote --tags "${KUBERNETES_SIGS_KUEUE_UPSTREAM_REMOTE}" "refs/tags/${RELEASE_VERSION}*") || refs=""
  RELEASE_COMMIT=$(awk -v tag="refs/tags/${RELEASE_VERSION}" '$2 == tag "^{}" { peeled = $1 } $2 == tag { direct = $1 } END { print (peeled != "" ? peeled : direct) }' <<< "${refs}")
  if [[ ! "${RELEASE_COMMIT}" =~ ^[0-9a-f]{40}$ ]]; then
    echo "!!! Could not resolve release tag ${RELEASE_VERSION} to a commit on ${KUBERNETES_SIGS_KUEUE_UPSTREAM_REMOTE}."
    exit 1
  fi
fi

prepare_local_branch main "${K8S_IO_BRANCH_UNIQUE}" "${K8S_IO_PR_NAME}"

if [[ -n "${DRY_RUN}" ]]; then
  echo "!!! Skipping git push, PR creation and issue update because you set DRY_RUN."
  exit 0
fi

push_and_create_pr main "${K8S_IO_BRANCH}" "${K8S_IO_BRANCH_UNIQUE}" "${K8S_IO_PR_NAME}"
K8S_IO_PR_NUMBER=$(gh pr list --repo="${KUBERNETES_K8S_IO_MAIN_REPO}" | grep "${K8S_IO_PR_NAME}" | awk '{print $1}' || true)
if [ -n "$K8S_IO_PR_NUMBER" ]; then
  NEW_RELEASE_ISSUE_BODY=${RELEASE_ISSUE_BODY//<!-- K8S_IO_PULL -->/${KUBERNETES_K8S_IO_MAIN_REPO}#${K8S_IO_PR_NUMBER}}
  NEW_RELEASE_ISSUE_BODY=$(printf '%s' "${NEW_RELEASE_ISSUE_BODY}" | python3 "${KUBERNETES_SIGS_KUEUE_PATH}/hack/releasing/mark_step_done.py" promote-pull)
  echo "+++ Editing release issue ${RELEASE_ISSUE_NUMBER} on GitHub repo ${KUBERNETES_SIGS_KUEUE_MAIN_REPO}"
  gh issue edit "${RELEASE_ISSUE_NUMBER}" --body "${NEW_RELEASE_ISSUE_BODY}" --repo="${KUBERNETES_SIGS_KUEUE_MAIN_REPO}" || {
    echo "!!! Failed to edit release issue \"${RELEASE_ISSUE_NAME}\": gh issue edit command failed."
  }
fi
