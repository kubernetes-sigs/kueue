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

PROJECT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)
TEST_DIR=$(mktemp -d)
trap 'rm -r "$TEST_DIR"' EXIT
export TEST_DIR

# The fake kube_codegen.sh installs its generators the way the real one does.
mkdir "$TEST_DIR/code-generator"
cat > "$TEST_DIR/code-generator/kube_codegen.sh" <<'EOF'
kube::codegen::gen_helpers() { go install k8s.io/code-generator/cmd/deepcopy-gen; }
kube::codegen::gen_openapi() { :; }
kube::codegen::gen_client() { :; }
EOF
# The fake go points "go list -m" at the fake code-generator. Its first install
# fails with the proxy stream error from CI, then installs succeed.
cat > "$TEST_DIR/go" <<'EOF'
#!/usr/bin/env bash
case "$*" in
  "mod download "*) ;;
  "list -m "*) echo "$TEST_DIR/code-generator" ;;
  "install "*)
    echo "$*" >> "$TEST_DIR/install.calls"
    if [[ $(wc -l < "$TEST_DIR/install.calls") -eq 1 ]]; then
      echo 'golang.org/x/tools@v0.52.0: read "https://proxy.golang.org/golang.org/x/tools/@v/v0.52.0.zip": stream error: stream ID 5; INTERNAL_ERROR; received from peer' >&2
      exit 1
    fi
    ;;
  *) echo "unexpected go $*" >&2; exit 1 ;;
esac
EOF
# The fake sleep skips the retry delay.
cat > "$TEST_DIR/sleep" <<'EOF'
#!/usr/bin/env bash
EOF
chmod +x "$TEST_DIR/go" "$TEST_DIR/sleep"

if ! PATH="$TEST_DIR:$PATH" "$PROJECT_DIR/hack/tools/code-generator/generate.sh" > "$TEST_DIR/generate.log" 2>&1; then
  cat "$TEST_DIR/generate.log" >&2
  echo "expected generate.sh to succeed after one failed go install" >&2
  exit 1
fi

[[ $(wc -l < "$TEST_DIR/install.calls") -eq 2 ]] || { echo "expected go install to be called twice" >&2; exit 1; }
want="install k8s.io/code-generator/cmd/deepcopy-gen"
got=$(tail -n 1 "$TEST_DIR/install.calls")
[[ "$got" == "$want" ]] || { printf 'expected go install args:\n  %s\ngot:\n  %s\n' "$want" "$got" >&2; exit 1; }

echo "generate-code tests passed"
