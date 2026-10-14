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

# Mock discovery only; exercise the actual filter and runtime-balanced shard wrapper.
mkdir -p "$TEST_DIR/bin"
cat > "$TEST_DIR/bin/go" <<'EOF'
#!/usr/bin/env bash
set -o errexit
set -o nounset
if [[ "$#" -ne 2 || "$1" != list || "$2" != "$UNIT_SELECTION_PATTERN" ]]; then
  echo "Unexpected package discovery arguments: $*" >&2
  exit 1
fi
cat "$UNIT_SELECTION_FIXTURE"
EOF
chmod +x "$TEST_DIR/bin/go"
export UNIT_SELECTION_FIXTURE="$TEST_DIR/packages"
export UNIT_SELECTION_PATTERN="./..."
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

# A focused target must reach Go discovery before packages are assigned to shards.
awk '/^sigs[.]k8s[.]io\/kueue\/test\/performance\/multikueue(\/|$)/' "$TEST_DIR/expected" > "$TEST_DIR/focused"
export UNIT_SELECTION_FIXTURE="$TEST_DIR/focused"
export UNIT_SELECTION_PATTERN="./test/performance/multikueue/..."
for shards in 1 2 3; do
  : > "$TEST_DIR/combined"
  for ((shard = 0; shard < shards; shard++)); do
    bash "$SCRIPT_DIR/shard-unit-tests.sh" "$shard" "$shards" ./test/performance/multikueue >> "$TEST_DIR/combined" 2> "$TEST_DIR/shard-log"
  done
  sort "$TEST_DIR/combined" > "$TEST_DIR/actual-sorted"
  sort "$TEST_DIR/focused" > "$TEST_DIR/expected-sorted"
  diff -u "$TEST_DIR/expected-sorted" "$TEST_DIR/actual-sorted"
done

# Longest-processing-time assignment is deterministic and keeps the slowest
# packages on different shards. Durations are milliseconds.
cat > "$TEST_DIR/balanced-packages" <<'EOF'
sigs.k8s.io/kueue/pkg/a
sigs.k8s.io/kueue/pkg/b
sigs.k8s.io/kueue/pkg/c
sigs.k8s.io/kueue/pkg/d
sigs.k8s.io/kueue/pkg/e
EOF
cat > "$TEST_DIR/balanced-durations" <<'EOF'
sigs.k8s.io/kueue/pkg/a 500
sigs.k8s.io/kueue/pkg/b 400
sigs.k8s.io/kueue/pkg/c 100
sigs.k8s.io/kueue/pkg/d 100
sigs.k8s.io/kueue/pkg/e 50
EOF
export UNIT_SELECTION_FIXTURE="$TEST_DIR/balanced-packages"
export UNIT_SELECTION_PATTERN="./..."
export UNIT_TEST_PACKAGE_DURATIONS="$TEST_DIR/balanced-durations"
bash "$SCRIPT_DIR/shard-unit-tests.sh" 0 2 > "$TEST_DIR/balanced-shard-0" 2> "$TEST_DIR/shard-log"
bash "$SCRIPT_DIR/shard-unit-tests.sh" 1 2 > "$TEST_DIR/balanced-shard-1" 2> "$TEST_DIR/shard-log"
diff -u "$TEST_DIR/balanced-shard-0" - <<'EOF'
sigs.k8s.io/kueue/pkg/a
sigs.k8s.io/kueue/pkg/d
EOF
diff -u "$TEST_DIR/balanced-shard-1" - <<'EOF'
sigs.k8s.io/kueue/pkg/b
sigs.k8s.io/kueue/pkg/c
sigs.k8s.io/kueue/pkg/e
EOF
bash "$SCRIPT_DIR/shard-unit-tests.sh" 0 2 > "$TEST_DIR/balanced-shard-0-again" 2> "$TEST_DIR/shard-log"
diff -u "$TEST_DIR/balanced-shard-0" "$TEST_DIR/balanced-shard-0-again"

# Measured durations stay within one heaviest package, and the two slowest
# packages do not share a shard. An unmeasured package is still assigned once.
awk 'NF == 2 && $2 ~ /^[0-9]+$/ { print $1 }' "$SCRIPT_DIR/unit_test_package_durations.txt" \
  | "$SCRIPT_DIR/filter-unit-test-packages.sh" > "$TEST_DIR/measured-packages"
printf '%s\n' "sigs.k8s.io/kueue/pkg/not-yet-measured" >> "$TEST_DIR/measured-packages"
export UNIT_SELECTION_FIXTURE="$TEST_DIR/measured-packages"
unset UNIT_TEST_PACKAGE_DURATIONS
bash "$SCRIPT_DIR/shard-unit-tests.sh" 0 2 > "$TEST_DIR/measured-shard-0" 2> "$TEST_DIR/shard-log"
bash "$SCRIPT_DIR/shard-unit-tests.sh" 1 2 > "$TEST_DIR/measured-shard-1" 2> "$TEST_DIR/shard-log"
sort "$TEST_DIR/measured-shard-0" "$TEST_DIR/measured-shard-1" > "$TEST_DIR/measured-actual"
sort "$TEST_DIR/measured-packages" > "$TEST_DIR/measured-expected"
diff -u "$TEST_DIR/measured-expected" "$TEST_DIR/measured-actual"
if [[ -n "$(uniq -d "$TEST_DIR/measured-actual")" ]]; then
  echo "A measured package was assigned to both shards." >&2
  exit 1
fi
default_ms="$(awk 'NF == 2 && $2 ~ /^[0-9]+$/ { print $2 }' "$SCRIPT_DIR/unit_test_package_durations.txt" | sort -n | awk '{ a[NR] = $1 } END { print a[int((NR + 1) / 2)] }')"
load_of() {
  awk -v default_ms="${default_ms}" 'NR == FNR { if (NF == 2 && $2 ~ /^[0-9]+$/) weight[$1] = $2; next } { sum += ($1 in weight ? weight[$1] : default_ms) } END { print sum + 0 }' \
    "$SCRIPT_DIR/unit_test_package_durations.txt" "$1"
}
load0="$(load_of "$TEST_DIR/measured-shard-0")"
load1="$(load_of "$TEST_DIR/measured-shard-1")"
max_ms="$(awk 'NF == 2 && $2 ~ /^[0-9]+$/ { print $2 }' "$SCRIPT_DIR/unit_test_package_durations.txt" | sort -n | tail -1)"
if (( load0 > load1 )); then
  gap=$((load0 - load1))
else
  gap=$((load1 - load0))
fi
if (( gap > max_ms )); then
  echo "Measured shard loads ${load0}ms and ${load1}ms differ by ${gap}ms, more than the heaviest package ${max_ms}ms." >&2
  exit 1
fi
awk 'NR == FNR { if (NF == 2 && $2 ~ /^[0-9]+$/) weight[$1] = $2; next } $1 in weight { print weight[$1], $1 }' \
  "$SCRIPT_DIR/unit_test_package_durations.txt" "$TEST_DIR/measured-packages" \
  | sort -k1,1nr | awk 'NR <= 2 { print $2 }' > "$TEST_DIR/slowest"
slowest0="$(sed -n '1p' "$TEST_DIR/slowest")"
slowest1="$(sed -n '2p' "$TEST_DIR/slowest")"
if grep -F -x -q "${slowest0}" "$TEST_DIR/measured-shard-0" && grep -F -x -q "${slowest1}" "$TEST_DIR/measured-shard-0"; then
  echo "The two slowest packages ${slowest0} and ${slowest1} were both assigned to shard 0." >&2
  exit 1
fi
if grep -F -x -q "${slowest0}" "$TEST_DIR/measured-shard-1" && grep -F -x -q "${slowest1}" "$TEST_DIR/measured-shard-1"; then
  echo "The two slowest packages ${slowest0} and ${slowest1} were both assigned to shard 1." >&2
  exit 1
fi

# Integration tests still use package-count round-robin from the shared helper.
cat > "$TEST_DIR/integration-packages" <<'EOF'
sigs.k8s.io/kueue/test/integration/singlecluster/a
sigs.k8s.io/kueue/test/integration/singlecluster/b
sigs.k8s.io/kueue/test/integration/singlecluster/c
sigs.k8s.io/kueue/test/integration/singlecluster/d
EOF
export UNIT_SELECTION_FIXTURE="$TEST_DIR/integration-packages"
export UNIT_SELECTION_PATTERN="./test/integration/singlecluster/..."
bash "$SCRIPT_DIR/shard_test_packages.sh" 0 2 "${UNIT_SELECTION_PATTERN}" > "$TEST_DIR/integration-shard-0" 2> "$TEST_DIR/shard-log"
bash "$SCRIPT_DIR/shard_test_packages.sh" 1 2 "${UNIT_SELECTION_PATTERN}" > "$TEST_DIR/integration-shard-1" 2> "$TEST_DIR/shard-log"
diff -u - "$TEST_DIR/integration-shard-0" <<'EOF'
sigs.k8s.io/kueue/test/integration/singlecluster/a
sigs.k8s.io/kueue/test/integration/singlecluster/c
EOF
diff -u - "$TEST_DIR/integration-shard-1" <<'EOF'
sigs.k8s.io/kueue/test/integration/singlecluster/b
sigs.k8s.io/kueue/test/integration/singlecluster/d
EOF

echo "Unit-test selection and shard coverage passed."
