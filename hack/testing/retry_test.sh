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

set -o errexit
set -o nounset
set -o pipefail

PROJECT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
TEST_DIR=$(mktemp -d)
trap 'rm -r "$TEST_DIR"' EXIT
export TEST_DIR

# The fake sleep records each delay instead of waiting.
cat > "$TEST_DIR/sleep" <<'EOF'
#!/usr/bin/env bash
printf '%s ' "$1" >> "$TEST_DIR/delays"
EOF
# The fake command fails until call number OK_ON, then prints "ok".
cat > "$TEST_DIR/flaky" <<'EOF'
#!/usr/bin/env bash
echo x >> "$TEST_DIR/calls"
[[ $(wc -l < "$TEST_DIR/calls") -ge $OK_ON ]] || exit 1
echo ok
EOF
chmod +x "$TEST_DIR/sleep" "$TEST_DIR/flaky"

# Usage: expect_retries NAME OK_ON WANT_STATUS WANT_STDOUT WANT_CALLS WANT_DELAYS [retry.sh flags...]
expect_retries() {
  local name=$1 ok_on=$2 want_status=$3 want_stdout=$4 want_calls=$5 want_delays=$6
  shift 6
  : > "$TEST_DIR/calls"
  : > "$TEST_DIR/delays"

  local got_stdout got_status=0
  got_stdout=$(OK_ON=$ok_on PATH="$TEST_DIR:$PATH" "$PROJECT_DIR/hack/testing/retry.sh" "$@" -- flaky 2>/dev/null) || got_status=$?

  local got_calls got_delays
  got_calls=$(wc -l < "$TEST_DIR/calls" | tr -d ' ')
  got_delays=$(cat "$TEST_DIR/delays")
  if [[ "$got_status" != "$want_status" || "$got_stdout" != "$want_stdout" || "$got_calls" != "$want_calls" || "$got_delays" != "$want_delays" ]]; then
    printf '%s: want status=%s stdout=%q calls=%s delays=%q; got status=%s stdout=%q calls=%s delays=%q\n' \
      "$name" "$want_status" "$want_stdout" "$want_calls" "$want_delays" \
      "$got_status" "$got_stdout" "$got_calls" "$got_delays" >&2
    exit 1
  fi
}

never=100
expect_retries "defaults" "$never" 1 "" 7 "2 4 8 16 32 64 "
expect_retries "success stops retrying" 3 0 "ok" 3 "2 4 "
expect_retries "attempts and delay overrides keep the backoff" "$never" 1 "" 3 "5 10 " --attempts 3 --delay 5
expect_retries "no-exponential keeps the delay constant" "$never" 1 "" 4 "3 3 3 " --attempts 4 --delay 3 --no-exponential
expect_retries "fractional delay keeps the backoff" "$never" 1 "" 4 "0.5 1 2 " --attempts 4 --delay 0.5

echo "retry.sh tests passed"
