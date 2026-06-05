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
# Dynamic Round-Robin Integration Test Sharding Helper Script
# ==============================================================================
# This script discovers all Go test packages inside a specified path and
# dynamically assigns them into a specific shard using round-robin distribution.
# All debug information is streamed to stderr (>&2) to keep stdout clean for Ginkgo.
# ==============================================================================

MAX_SHARDS=16
INTEGRATION_SHARD_INDEX=$1
INTEGRATION_TOTAL_SHARDS=$2
TARGET_PATTERN=${3:-./test/integration/singlecluster/...}

if [ -z "$INTEGRATION_SHARD_INDEX" ] || [ -z "$INTEGRATION_TOTAL_SHARDS" ]; then
    echo "Error: INTEGRATION_SHARD_INDEX and INTEGRATION_TOTAL_SHARDS are required arguments." >&2
    echo "Usage: $0 <integration_shard_index> <integration_total_shards> [target_pattern]" >&2
    exit 1
fi

if [ "$INTEGRATION_TOTAL_SHARDS" -le 0 ] || [ "$INTEGRATION_TOTAL_SHARDS" -gt "$MAX_SHARDS" ]; then
    echo "Error: INTEGRATION_TOTAL_SHARDS ($INTEGRATION_TOTAL_SHARDS) must be between 1 and $MAX_SHARDS." >&2
    exit 1
fi

if [ "$INTEGRATION_SHARD_INDEX" -lt 0 ] || [ "$INTEGRATION_SHARD_INDEX" -ge "$INTEGRATION_TOTAL_SHARDS" ]; then
    echo "Error: INTEGRATION_SHARD_INDEX ($INTEGRATION_SHARD_INDEX) must be between 0 and $((INTEGRATION_TOTAL_SHARDS - 1))." >&2
    exit 1
fi

# Store discovered package paths in an array.
# Note: 'go list' outputs packages in deterministic lexicographical order.
# This ensures package distribution remains perfectly stable across shards.
packages=()
while read -r pkg; do
    if [ -n "$pkg" ]; then
        packages+=("$pkg")
    fi
done < <(go list "$TARGET_PATTERN" | sed 's|^sigs.k8s.io/kueue/|./|')
total_targets=${#packages[@]}

if [ "$total_targets" -eq 0 ]; then
    echo "Error: Discovered 0 integration test packages matching pattern '$TARGET_PATTERN'." >&2
    exit 1
fi

# Print detailed debugging overview to stderr (>&2) so Ginkgo command capture remains clean
echo "================================================================================" >&2
echo "Test Sharding Evaluation Summary (Total Targets = $total_targets)" >&2
echo "================================================================================" >&2

for (( i = 0; i < total_targets; i++ )); do
    pkg="${packages[i]}"
    current_modulus=$(( i % INTEGRATION_TOTAL_SHARDS ))

    if [ "$current_modulus" -eq "$INTEGRATION_SHARD_INDEX" ]; then
        echo "[*] [Shard $current_modulus] $pkg" >&2
        # Standard output streams shard's paths for Ginkgo capture
        echo "$pkg"
    else
        echo "[ ] [Shard $current_modulus] $pkg" >&2
    fi
done

echo "================================================================================" >&2
