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

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
TEST_DIR=$(mktemp -d)
trap 'rm -r "$TEST_DIR"' EXIT

cat > "$TEST_DIR/packages" <<'EOF'
sigs.k8s.io/kueue/pkg/controller/core
sigs.k8s.io/kueue/test/integration/singlecluster
sigs.k8s.io/kueue/test/performance/multikueue
sigs.k8s.io/kueue/test/performance/multikueue/checker
sigs.k8s.io/kueue/test/performance/multikueue/config
sigs.k8s.io/kueue/test/performance/multikueue/report
sigs.k8s.io/kueue/test/performance/multikueue-extra
sigs.k8s.io/kueue/test/performance/scheduler
sigs.k8s.io/kueue/test/e2e/multikueue
sigs.k8s.io/kueue/pkg/workload
EOF

cat > "$TEST_DIR/expected" <<'EOF'
sigs.k8s.io/kueue/pkg/controller/core
sigs.k8s.io/kueue/test/performance/multikueue
sigs.k8s.io/kueue/test/performance/multikueue/checker
sigs.k8s.io/kueue/test/performance/multikueue/config
sigs.k8s.io/kueue/test/performance/multikueue/report
sigs.k8s.io/kueue/pkg/workload
EOF

bash "$SCRIPT_DIR/filter-unit-test-packages.sh" < "$TEST_DIR/packages" > "$TEST_DIR/actual"
diff -u "$TEST_DIR/expected" "$TEST_DIR/actual"

# Mock discovery only; exercise the actual filter and round-robin shard wrapper.
mkdir -p "$TEST_DIR/bin"
cat > "$TEST_DIR/bin/go" <<'EOF'
#!/usr/bin/env bash
cat "$UNIT_SELECTION_FIXTURE"
EOF
chmod +x "$TEST_DIR/bin/go"
export UNIT_SELECTION_FIXTURE="$TEST_DIR/packages"
export PATH="$TEST_DIR/bin:$PATH"
for shards in 1 2 3; do
  : > "$TEST_DIR/combined"
  for ((shard = 0; shard < shards; shard++)); do
    bash "$SCRIPT_DIR/shard-unit-tests.sh" "$shard" "$shards" >> "$TEST_DIR/combined" 2> "$TEST_DIR/shard-log"
  done
  sort "$TEST_DIR/combined" > "$TEST_DIR/actual-sorted"
  sort "$TEST_DIR/expected" > "$TEST_DIR/expected-sorted"
  diff -u "$TEST_DIR/expected-sorted" "$TEST_DIR/actual-sorted"
done

echo "Unit-test selection and shard coverage passed."
