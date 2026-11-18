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

# Fake curl and kind fail their first call with a network error, then succeed.
cat > "$TEST_DIR/curl" <<'EOF'
#!/usr/bin/env bash
echo x >> "$TEST_DIR/curl.calls"
if [[ $(wc -l < "$TEST_DIR/curl.calls") -eq 1 ]]; then
  echo "<html>503 Service Unavailable</html>"
  exit 22
fi
echo "v1.99.0-alpha.0"
EOF
cat > "$TEST_DIR/kind" <<'EOF'
#!/usr/bin/env bash
echo "$*" >> "$TEST_DIR/kind.calls"
if [[ $(wc -l < "$TEST_DIR/kind.calls") -eq 1 ]]; then
  echo "ERROR: error downloading HTTP content: unexpected EOF" >&2
  exit 1
fi
EOF
chmod +x "$TEST_DIR/curl" "$TEST_DIR/kind"
export TEST_DIR

if ! PATH="$TEST_DIR:$PATH" make --no-print-directory -s -C "$PROJECT_DIR" -o kind \
    kind-k8s-main-image-build KIND="$TEST_DIR/kind" > "$TEST_DIR/make.log" 2>&1; then
  cat "$TEST_DIR/make.log" >&2
  echo "expected kind-k8s-main-image-build to succeed after one failed curl and one failed kind build" >&2
  exit 1
fi

[[ $(wc -l < "$TEST_DIR/curl.calls") -eq 2 ]] || { echo "expected curl to be called twice" >&2; exit 1; }
[[ $(wc -l < "$TEST_DIR/kind.calls") -eq 2 ]] || { echo "expected kind to be called twice" >&2; exit 1; }
want="build node-image --image=k8s-main:latest https://dl.k8s.io/ci/v1.99.0-alpha.0/kubernetes-server-linux-$(go env GOARCH).tar.gz"
got=$(tail -n 1 "$TEST_DIR/kind.calls")
[[ "$got" == "$want" ]] || { printf 'expected kind args:\n  %s\ngot:\n  %s\n' "$want" "$got" >&2; exit 1; }

echo "kind-k8s-main-image-build tests passed"
