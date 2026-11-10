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

# Exercise real Make recipes using stub Ginkgo and Go (no envtest needed).
set -o errexit -o nounset -o pipefail
project=$(cd "$(dirname "$0")/../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin" "$work/artifacts"
cat > "$work/profile.cov" <<'PROFILE'
mode: atomic
sigs.k8s.io/kueue/pkg/controller/x.go:1.1,2.1 1 1
sigs.k8s.io/kueue/pkg/controller/x.go:1.1,2.1 1 2
sigs.k8s.io/kueue/client-go/ignore.go:1.1,2.1 1 5
PROFILE
cat > "$work/bin/ginkgo" <<'EOF_GINKGO'
#!/usr/bin/env bash
set -euo pipefail
printf 'ginkgo %s\n' "$*" >> "$MOCK_CALLS"
profile=0
for arg in "$@"; do
  case "$arg" in
    --coverprofile=integration-cover.out) profile=1 ;;
    --json-report=integration.json) printf '[]\n' > "$ARTIFACTS/integration.json" ;;
    --json-report=multikueue-integration.json) printf '[]\n' > "$ARTIFACTS/multikueue-integration.json" ;;
  esac
done
if (( profile )); then cp "$MOCK_PROFILE" "$ARTIFACTS/integration-cover.out"; fi
EOF_GINKGO
cat > "$work/bin/go" <<'EOF_GO'
#!/usr/bin/env bash
set -euo pipefail
printf 'go %s\n' "$*" >> "$MOCK_CALLS"
if [[ "$1 $2" == 'tool cover' ]]; then
  for arg in "$@"; do
    case "$arg" in -o=*) printf 'HTML\n' > "$(printf '%s' "$arg" | sed 's/^-o=//')" ;; esac
  done
else
  # Unsupported Go commands must fail rather than silently passing the test.
  exit 3
fi
EOF_GO
cat > "$work/bin/ginkgo-top" <<'EOF_TOP'
#!/usr/bin/env bash
printf 'top: []\n'
EOF_TOP
chmod +x "$work/bin/"*
cat > "$work/Makefile" <<EOF_MAKE
PROJECT_DIR := $project
BIN_DIR := $work/bin
ARTIFACTS := $work/artifacts
GINKGO := $work/bin/ginkgo
GO_CMD := $work/bin/go
INTEGRATION_TARGET := ./test/integration/singlecluster/...
INTEGRATION_TARGET_MULTIKUEUE := ./test/integration/multikueue/...
include $project/hack/make/test.mk
.PHONY: compile-crd-manifests envtest ginkgo dep-crds ginkgo-top kueuectl
compile-crd-manifests envtest ginkgo dep-crds ginkgo-top kueuectl:
	@:
EOF_MAKE
export MOCK_CALLS="$work/calls.log" MOCK_PROFILE="$work/profile.cov"
run_make() {
  # Do not inherit an outer make's command-line overrides. In particular,
  # ARTIFACTS= would redirect the nested recipes into the caller's directory.
  env -u MAKEFLAGS -u MFLAGS -u GNUMAKEFLAGS -u MAKEOVERRIDES -u MAKEFILES \
    make --no-print-directory -s -f "$work/Makefile" "$1" "INTEGRATION_COVERAGE=$2" "KUBEBUILDER_ASSETS=$work" >/dev/null
}
for target in test-integration-run test-multikueue-integration; do
  : > "$MOCK_CALLS"
  run_make "$target" 1
  grep -F -- '--cover --coverpkg=sigs.k8s.io/kueue/... --coverprofile=integration-cover.out' "$MOCK_CALLS" >/dev/null
  grep -F 'go tool cover' "$MOCK_CALLS" >/dev/null
  grep -qx 'sigs.k8s.io/kueue/pkg/controller/x.go:1.1,2.1 1 3' "$work/artifacts/filtered.cov"
  [[ -s "$work/artifacts/filtered.html" ]]
  [[ -s "$work/artifacts/integration-cover.out" ]]
done
: > "$MOCK_CALLS"
run_make test-integration-run 0
if grep -F -- '--coverprofile=' "$MOCK_CALLS" >/dev/null; then
  echo 'coverage disabled but Ginkgo received coverage flags' >&2; exit 1
fi
if grep -F 'go tool cover' "$MOCK_CALLS" >/dev/null; then
  echo 'coverage disabled but HTML generator ran' >&2; exit 1
fi
[[ ! -e "$work/artifacts/filtered.cov" ]]
[[ ! -e "$work/artifacts/filtered.html" ]]
[[ ! -e "$work/artifacts/integration-cover.out" ]]
# Simulate command-line overrides inherited from a calling make process.
# Both recipes must write into their sandbox without changing caller artifacts.
caller_artifacts="$work/caller-artifacts"
mkdir -p "$caller_artifacts"
caller_files=(integration-cover.out filtered.cov filtered.html integration.json multikueue-integration.json integration-top.yaml multikueue-integration-top.yaml)
for file in "${caller_files[@]}"; do
  printf 'caller sentinel: %s\n' "$file" > "$caller_artifacts/$file"
done
for target in test-integration-run test-multikueue-integration; do
  : > "$MOCK_CALLS"
  MAKEFLAGS="ARTIFACTS=$caller_artifacts" GNUMAKEFLAGS='-e' ARTIFACTS="$caller_artifacts" run_make "$target" 1
  grep -F 'go tool cover' "$MOCK_CALLS" >/dev/null
  grep -qx 'sigs.k8s.io/kueue/pkg/controller/x.go:1.1,2.1 1 3' "$work/artifacts/filtered.cov"
  for file in "${caller_files[@]}"; do
    if ! grep -Fxq "caller sentinel: $file" "$caller_artifacts/$file"; then
      echo "nested Make modified caller artifact: $file" >&2
      exit 1
    fi
  done
done
echo 'integration-coverage Make recipe tests passed'
