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

# Tests release note extraction in hack/cherry_pick_pull.sh.

set -o errexit
set -o nounset
set -o pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)

# shellcheck source=hack/cherry_pick_pull.sh
source "${ROOT_DIR}/hack/cherry_pick_pull.sh"

failures=0
current_case=""

function start_case() {
  current_case="$1"
}

function fail() {
  echo "FAIL: ${current_case}: $1" >&2
  failures=$((failures + 1))
}

function assert_eq() {
  local want="$1" got="$2" what="$3"
  if [[ "${want}" != "${got}" ]]; then
    fail "${what}: want [${want}], got [${got}]"
  fi
}

start_case "inline NONE block followed by multiline block"
body=$(cat <<'EOF'
```release-note NONE```
```release-note
Later note
```
EOF
)
got=$(extract_release_note <<<"${body}")
assert_eq "NONE" "${got}" "release note text"

start_case "inline note with text followed by multiline block"
body=$(cat <<'EOF'
```release-note Fix scheduling bug in Kueue```
```release-note
Later note
```
EOF
)
got=$(extract_release_note <<<"${body}")
assert_eq "Fix scheduling bug in Kueue" "${got}" "release note text"

start_case "inline empty block followed by multiline block"
body=$(cat <<'EOF'
```release-note```
```release-note
Later note
```
EOF
)
got=$(extract_release_note <<<"${body}")
assert_eq "" "${got}" "release note text"

start_case "two inline blocks on one line extracts the first block"
# shellcheck disable=SC2016 # Single quotes intentional to preserve literal backticks
body='```release-note NONE``` ```release-note Later note```'
got=$(extract_release_note <<<"${body}")
assert_eq "NONE" "${got}" "release note text"

start_case "note starting on opening fence line and closing on later line"
body=$(cat <<'EOF'
```release-note Fix X
second line
```
EOF
)
want=$(cat <<'EOF'
Fix X
second line
EOF
)
got=$(extract_release_note <<<"${body}")
assert_eq "${want}" "${got}" "release note text"

start_case "standard multiline release note followed by later block"
body=$(cat <<'EOF'
```release-note
First line of note
Second line of note
```
```release-note
Later note
```
EOF
)
want=$(cat <<'EOF'
First line of note
Second line of note
EOF
)
got=$(extract_release_note <<<"${body}")
assert_eq "${want}" "${got}" "release note text"

start_case "multiline NONE block followed by later block"
body=$(cat <<'EOF'
```release-note
NONE
```
```release-note
Later note
```
EOF
)
got=$(extract_release_note <<<"${body}")
assert_eq "NONE" "${got}" "release note text"

start_case "prior code blocks do not interfere with multiline note"
body=$(cat <<'EOF'
Here is an example config:
```yaml
key: value
```
```release-note
Valid note content
```
```release-note
Bot note
```
EOF
)
got=$(extract_release_note <<<"${body}")
assert_eq "Valid note content" "${got}" "release note text"

start_case "no release note block returns empty"
body=$(cat <<'EOF'
Just a regular PR description
without any release notes.
EOF
)
got=$(extract_release_note <<<"${body}")
assert_eq "" "${got}" "release note text"

start_case "inline note with surrounding spaces"
# shellcheck disable=SC2016 # Single quotes intentional to preserve literal backticks
body='```release-note    NONE    ```'
got=$(extract_release_note <<<"${body}")
assert_eq "NONE" "${got}" "release note text"

start_case "inline note with release-notes plural tag"
# shellcheck disable=SC2016 # Single quotes intentional to preserve literal backticks
body='```release-notes NONE```'
got=$(extract_release_note <<<"${body}")
assert_eq "NONE" "${got}" "release note text"

if [[ "${failures}" -gt 0 ]]; then
  echo "cherry_pick_pull_test: ${failures} failure(s)" >&2
  exit 1
fi
echo "cherry_pick_pull_test: all tests passed"
