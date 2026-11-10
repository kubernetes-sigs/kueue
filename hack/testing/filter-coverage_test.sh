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

set -o errexit -o nounset -o pipefail
script=$(cd "$(dirname "$0")" && pwd)/filter-coverage.sh
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
exclude='/zz_generated\.|^sigs\.k8s\.io/kueue/(client-go|internal/mocks|cmd/kueuectl/app/testing|test)/'
cat > "$work/atomic.cov" <<'PROFILE'
mode: atomic
sigs.k8s.io/kueue/pkg/controller/a.go:1.1,1.5 1 2
sigs.k8s.io/kueue/pkg/controller/a.go:1.1,1.5 1 3
sigs.k8s.io/kueue/pkg/controller/b.go:2.1,2.5 2 0
sigs.k8s.io/kueue/client-go/x.go:3.1,3.5 1 8
sigs.k8s.io/kueue/internal/mocks/x.go:4.1,4.5 1 8
sigs.k8s.io/kueue/test/util/x.go:5.1,5.5 1 8
sigs.k8s.io/kueue/pkg/zz_generated.deepcopy.go:6.1,6.5 1 8
PROFILE
bash "$script" "$work/atomic.cov" "$work/filtered.cov" "$exclude"
cat > "$work/want" <<'PROFILE'
mode: atomic
sigs.k8s.io/kueue/pkg/controller/a.go:1.1,1.5 1 5
sigs.k8s.io/kueue/pkg/controller/b.go:2.1,2.5 2 0
PROFILE
{ head -n 1 "$work/filtered.cov"; tail -n +2 "$work/filtered.cov" | sort; } > "$work/actual"
diff -u "$work/want" "$work/actual"
cat > "$work/set.cov" <<'PROFILE'
mode: set
sigs.k8s.io/kueue/pkg/a.go:1.1,1.5 1 1
sigs.k8s.io/kueue/pkg/a.go:1.1,1.5 1 1
sigs.k8s.io/kueue/pkg/b.go:1.1,1.5 1 0
sigs.k8s.io/kueue/pkg/b.go:1.1,1.5 1 1
PROFILE
bash "$script" "$work/set.cov" "$work/set-filtered.cov" "$exclude"
grep -qx 'sigs.k8s.io/kueue/pkg/a.go:1.1,1.5 1 1' "$work/set-filtered.cov"
grep -qx 'sigs.k8s.io/kueue/pkg/b.go:1.1,1.5 1 1' "$work/set-filtered.cov"
printf 'unchanged\n' > "$work/preserved.cov"
if bash "$script" "$work/missing.cov" "$work/preserved.cov" "$exclude" 2>/dev/null; then
  echo 'missing input unexpectedly succeeded' >&2; exit 1
fi
printf 'not a profile\n' > "$work/bad.cov"
if bash "$script" "$work/bad.cov" "$work/preserved.cov" "$exclude" 2>/dev/null; then
  echo 'invalid header unexpectedly succeeded' >&2; exit 1
fi
printf 'mode: atomic\nmalformed\n' > "$work/bad.cov"
if bash "$script" "$work/bad.cov" "$work/preserved.cov" "$exclude" 2>/dev/null; then
  echo 'invalid block unexpectedly succeeded' >&2; exit 1
fi
printf 'mode: atomic\nsigs.k8s.io/kueue/test/a.go:1.1,2.1 1 1\n' > "$work/bad.cov"
if bash "$script" "$work/bad.cov" "$work/preserved.cov" "$exclude" 2>/dev/null; then
  echo 'all-excluded profile unexpectedly succeeded' >&2; exit 1
fi
grep -qx unchanged "$work/preserved.cov"
echo 'filter-coverage.sh tests passed'
