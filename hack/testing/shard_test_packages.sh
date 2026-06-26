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
# Dynamic Round-Robin Test Package Sharding Script
# ==============================================================================
# Discovers Go test packages matching a given pattern and assigns them to a
# specific shard using round-robin distribution.
# All debug information is written to stderr so stdout stays clean for callers.
#
# Usage: shard_test_packages.sh <shard_index> <total_shards> <go_list_pattern>
# ==============================================================================

MAX_SHARDS=16
SHARD_INDEX=$1
TOTAL_SHARDS=$2
TARGET_PATTERN=$3

if [ -z "$SHARD_INDEX" ] || [ -z "$TOTAL_SHARDS" ] || [ -z "$TARGET_PATTERN" ]; then
    echo "Error: SHARD_INDEX, TOTAL_SHARDS and TARGET_PATTERN are required arguments." >&2
    echo "Usage: $0 <shard_index> <total_shards> <go_list_pattern>" >&2
    exit 1
fi

if [ "$TOTAL_SHARDS" -le 0 ] || [ "$TOTAL_SHARDS" -gt "$MAX_SHARDS" ]; then
    echo "Error: TOTAL_SHARDS ($TOTAL_SHARDS) must be between 1 and $MAX_SHARDS." >&2
    exit 1
fi

if [ "$SHARD_INDEX" -lt 0 ] || [ "$SHARD_INDEX" -ge "$TOTAL_SHARDS" ]; then
    echo "Error: SHARD_INDEX ($SHARD_INDEX) must be between 0 and $((TOTAL_SHARDS - 1))." >&2
    exit 1
fi

packages=()
while read -r pkg; do
    if [ -n "$pkg" ]; then
        packages+=("$pkg")
    fi
done < <(go list "$TARGET_PATTERN")

total_packages=${#packages[@]}

if [ "$total_packages" -eq 0 ]; then
    echo "Error: Discovered 0 packages matching pattern '$TARGET_PATTERN'." >&2
    exit 1
fi

echo "================================================================================" >&2
echo "Test Sharding Summary (Total Packages = $total_packages)" >&2
echo "================================================================================" >&2

for (( i = 0; i < total_packages; i++ )); do
    pkg="${packages[i]}"
    current_modulus=$(( i % TOTAL_SHARDS ))

    if [ "$current_modulus" -eq "$SHARD_INDEX" ]; then
        echo "[*] [Shard $current_modulus] $pkg" >&2
        echo "$pkg"
    else
        echo "[ ] [Shard $current_modulus] $pkg" >&2
    fi
done

echo "================================================================================" >&2
