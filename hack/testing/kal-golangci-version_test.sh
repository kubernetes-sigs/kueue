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

# Runs verify-kal-golangci-version from hack/make/verify.mk against fixture
# .custom-gcl.yaml files.

set -o errexit
set -o nounset
set -o pipefail

REPO_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
TEST_DIR=$(mktemp -d)
trap 'rm -r "$TEST_DIR"' EXIT

# Usage: expect_check NAME CUSTOM_GCL_CONTENTS GOLANGCI_LINT_VERSION WANT_STATUS WANT_STDERR_SUBSTRING
expect_check() {
  local name=$1 config=$2 version=$3 want_status=$4 want_stderr=$5
  local case_dir="$TEST_DIR/$name"
  mkdir -p "$case_dir/hack/testing/kal-linter"
  printf '%s\n' "$config" > "$case_dir/hack/testing/kal-linter/.custom-gcl.yaml"

  local got_status=0
  make --no-print-directory -s -f "$REPO_DIR/hack/make/verify.mk" verify-kal-golangci-version \
    PROJECT_DIR="$case_dir" TESTING_DIR="$REPO_DIR/hack/testing" GOLANGCI_LINT_VERSION="$version" \
    > /dev/null 2> "$case_dir/stderr" || got_status=$?
  if [[ "$got_status" -ne 0 ]]; then got_status=1; fi

  local got_stderr
  got_stderr=$(cat "$case_dir/stderr")
  if [[ "$got_status" != "$want_status" || "$got_stderr" != *"$want_stderr"* ]]; then
    printf '%s: want status=%s stderr containing %q; got status=%s stderr:\n' \
      "$name" "$want_status" "$want_stderr" "$got_status" >&2
    printf '%s\n' "$got_stderr" >&2
    exit 1
  fi
}

expect_check "in-sync" "version: v2.15.0" v2.15.0 0 ""
expect_check "drift" "version: v2.14.0" v2.15.0 1 \
  "(v2.14.0) does not match hack/tools/go.mod (v2.15.0); run make sync-kal-golangci-version"
expect_check "newer-kal" "version: v2.16.0" v2.15.0 1 "(v2.16.0) does not match"
expect_check "no-version-line" "name: golangci-lint-kube-api-linter" v2.15.0 1 "() does not match"
expect_check "both-empty" "version:" "" 1 "does not match"

echo "kal-golangci-version tests passed"
