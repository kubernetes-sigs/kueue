#!/bin/bash

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

# ==============================================================================
# Dynamic Round-Robin Unit Test Sharding Helper Script
# ==============================================================================
# Discovers all Go unit-test packages (excluding ./test/) and assigns them to
# a specific shard using round-robin distribution.
# All debug information is written to stderr so stdout stays clean for Make.
# ==============================================================================

MAX_SHARDS=16
UNIT_SHARD_INDEX=$1
UNIT_TOTAL_SHARDS=$2
GO_TEST_TARGET=${3:-.}

if [ -z "$UNIT_SHARD_INDEX" ] || [ -z "$UNIT_TOTAL_SHARDS" ]; then
    echo "Error: UNIT_SHARD_INDEX and UNIT_TOTAL_SHARDS are required arguments." >&2
    echo "Usage: $0 <shard_index> <total_shards> [go_test_target]" >&2
    exit 1
fi

if [ "$UNIT_TOTAL_SHARDS" -le 0 ] || [ "$UNIT_TOTAL_SHARDS" -gt "$MAX_SHARDS" ]; then
    echo "Error: UNIT_TOTAL_SHARDS ($UNIT_TOTAL_SHARDS) must be between 1 and $MAX_SHARDS." >&2
    exit 1
fi

if [ "$UNIT_SHARD_INDEX" -lt 0 ] || [ "$UNIT_SHARD_INDEX" -ge "$UNIT_TOTAL_SHARDS" ]; then
    echo "Error: UNIT_SHARD_INDEX ($UNIT_SHARD_INDEX) must be between 0 and $((UNIT_TOTAL_SHARDS - 1))." >&2
    exit 1
fi

# Collect all packages, excluding anything under ./test/.
# 'go list' output is deterministic (lexicographic), guaranteeing stable shard assignment.
packages=()
while read -r pkg; do
    if [ -n "$pkg" ]; then
        packages+=("$pkg")
    fi
done < <(go list "${GO_TEST_TARGET}/..." | grep -v '/test/')

total_packages=${#packages[@]}

if [ "$total_packages" -eq 0 ]; then
    echo "Error: Discovered 0 unit-test packages under '$GO_TEST_TARGET' (excluding /test/)." >&2
    exit 1
fi

echo "================================================================================" >&2
echo "Unit Test Sharding Summary (Total Packages = $total_packages)" >&2
echo "================================================================================" >&2

for (( i = 0; i < total_packages; i++ )); do
    pkg="${packages[i]}"
    current_modulus=$(( i % UNIT_TOTAL_SHARDS ))

    if [ "$current_modulus" -eq "$UNIT_SHARD_INDEX" ]; then
        echo "[*] [Shard $current_modulus] $pkg" >&2
        echo "$pkg"
    else
        echo "[ ] [Shard $current_modulus] $pkg" >&2
    fi
done

echo "================================================================================" >&2