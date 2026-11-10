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

# Filter a Go profile, collapsing duplicate blocks from -coverpkg.
# Usage: filter-coverage.sh INPUT OUTPUT EXCLUDE_REGEX
set -o errexit -o nounset -o pipefail
if (( $# != 3 )); then
  echo "usage: $0 INPUT OUTPUT EXCLUDE_REGEX" >&2
  exit 2
fi
input=$1 output=$2 exclude=$3
if [[ ! -s "$input" ]]; then
  echo "coverage profile is missing or empty: $input" >&2
  exit 1
fi
# Only replace the destination after a complete successful parse.
tmp=$(mktemp "$output.tmp.XXXXXX")
trap 'rm -f "$tmp"' EXIT
awk -v exclude="$exclude" '
  function invalid(message) {
    print "invalid coverage profile: " message > "/dev/stderr"
    failed = 1
    exit 2
  }
  NR == 1 {
    if ($0 != "mode: atomic" && $0 != "mode: count" && $0 != "mode: set")
      invalid("missing or unsupported mode header")
    mode = $2
    print
    next
  }
  {
    if (NF != 3 || $1 !~ /:[0-9]+\.[0-9]+,[0-9]+\.[0-9]+$/ ||
        $2 !~ /^[0-9]+$/ || $3 !~ /^[0-9]+$/)
      invalid("malformed block at line " NR)
    if (exclude != "" && $0 ~ exclude)
      next
    key = $1 " " $2
    if (mode == "set") {
      if (!(key in counts) || $3 > counts[key])
        counts[key] = $3
    } else {
      counts[key] += $3
    }
    found[key] = 1
  }
  END {
    if (failed) exit 2
    for (key in found)
      printf "%s %.0f\n", key, counts[key]
  }
' "$input" > "$tmp"
if [[ $(wc -l < "$tmp") -lt 2 ]]; then
  echo "no coverage blocks remain after filtering: $input" >&2
  exit 1
fi
mv -f "$tmp" "$output"
