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

# Assign unit-test packages to one shard by measured runtime.
#
# Package-count round-robin put the slow packages on one shard: with the same
# 3400m CPU request, shard 0 ran for about 13 minutes and shard 1 for about 7
# (kueue issue 16802). This script keeps the unit-test filter, then places the
# longest remaining package onto the shard with the smallest total duration.
# Equal totals go to the lower shard index. Packages missing from the duration
# file use the median measured duration so new packages still spread out.
# Integration tests keep the shared round-robin helper.
#
# Usage: shard-unit-tests.sh <shard_index> <total_shards> [go_test_target]
# Stdout is the selected packages, longest first. Diagnostics go to stderr.

set -o errexit
set -o nounset
set -o pipefail

MAX_SHARDS=16
UNIT_SHARD_INDEX=${1:-}
UNIT_TOTAL_SHARDS=${2:-}
GO_TEST_TARGET=${3:-.}

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DURATIONS_FILE="${UNIT_TEST_PACKAGE_DURATIONS:-${SCRIPT_DIR}/unit_test_package_durations.txt}"

if [[ -z "${UNIT_SHARD_INDEX}" || -z "${UNIT_TOTAL_SHARDS}" ]]; then
    echo "Error: SHARD_INDEX and TOTAL_SHARDS are required arguments." >&2
    echo "Usage: $0 <shard_index> <total_shards> [go_test_target]" >&2
    exit 1
fi

if [[ ! "${UNIT_SHARD_INDEX}" =~ ^[0-9]+$ || ! "${UNIT_TOTAL_SHARDS}" =~ ^[0-9]+$ ]]; then
    echo "Error: SHARD_INDEX and TOTAL_SHARDS must be non-negative integers." >&2
    exit 1
fi

if (( UNIT_TOTAL_SHARDS <= 0 || UNIT_TOTAL_SHARDS > MAX_SHARDS )); then
    echo "Error: TOTAL_SHARDS (${UNIT_TOTAL_SHARDS}) must be between 1 and ${MAX_SHARDS}." >&2
    exit 1
fi

if (( UNIT_SHARD_INDEX >= UNIT_TOTAL_SHARDS )); then
    echo "Error: SHARD_INDEX (${UNIT_SHARD_INDEX}) must be between 0 and $((UNIT_TOTAL_SHARDS - 1))." >&2
    exit 1
fi

if [[ ! -s "${DURATIONS_FILE}" ]]; then
    echo "Error: package duration file ${DURATIONS_FILE} is missing or empty." >&2
    exit 1
fi

default_ms="$(
    awk 'NF == 2 && $2 ~ /^[0-9]+$/ { print $2 }' "${DURATIONS_FILE}" \
        | sort -n \
        | awk 'BEGIN { n = 0 } { n++; a[n] = $1 } END { if (n == 0) exit 1; print a[int((n + 1) / 2)] }'
)"

tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

discovered="${tmp_dir}/packages"
ranked="${tmp_dir}/ranked"
if ! go list "${GO_TEST_TARGET}/..." | "${SCRIPT_DIR}/filter-unit-test-packages.sh" | sed '/^[[:space:]]*$/d' > "${discovered}"; then
    echo "Error: failed to discover unit test packages for ${GO_TEST_TARGET}/..." >&2
    exit 1
fi

if [[ ! -s "${discovered}" ]]; then
    echo "Error: discovered 0 unit test packages for ${GO_TEST_TARGET}/..." >&2
    exit 1
fi

# Longest packages first. Equal durations break ties by import path so every
# shard index computes the same assignment.
awk -v default_ms="${default_ms}" '
    NR == FNR {
        if (NF == 2 && $2 ~ /^[0-9]+$/ && $2 > 0) {
            weight[$1] = $2
        }
        next
    }
    {
        if ($1 in weight) {
            print weight[$1], $1
        } else {
            print default_ms, $1
        }
    }
' "${DURATIONS_FILE}" "${discovered}" | LC_ALL=C sort -k1,1nr -k2,2 > "${ranked}"

load=()
count=()
for ((i = 0; i < UNIT_TOTAL_SHARDS; i++)); do
    load[i]=0
    count[i]=0
done

total_packages=0
while read -r weight pkg; do
    best=0
    for ((i = 1; i < UNIT_TOTAL_SHARDS; i++)); do
        if (( load[i] < load[best] )); then
            best=$i
        fi
    done
    load[best]=$((load[best] + weight))
    count[best]=$((count[best] + 1))
    total_packages=$((total_packages + 1))
    if (( best == UNIT_SHARD_INDEX )); then
        marker="*"
        printf '%s\n' "${pkg}"
    else
        marker=" "
    fi
    printf '[%s] [Shard %d] %s (%sms)\n' "${marker}" "${best}" "${pkg}" "${weight}" >&2
done < "${ranked}"

echo "================================================================================" >&2
echo "Unit Test Sharding Summary (Total Packages = ${total_packages})" >&2
for ((i = 0; i < UNIT_TOTAL_SHARDS; i++)); do
    printf 'Shard %d: packages=%d duration=%sms\n' "${i}" "${count[i]}" "${load[i]}" >&2
done
echo "================================================================================" >&2
