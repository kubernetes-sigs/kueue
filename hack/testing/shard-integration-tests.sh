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

# Wrapper around shard_test_packages.sh for integration tests.
# Converts module-path output (sigs.k8s.io/kueue/...) to relative paths (./...)
# as required by Ginkgo.
#
# Usage: shard-integration-tests.sh <shard_index> <total_shards> [target_pattern]

INTEGRATION_SHARD_INDEX=$1
INTEGRATION_TOTAL_SHARDS=$2
TARGET_PATTERN=${3:-./test/integration/singlecluster/...}

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

bash "$SCRIPT_DIR/shard_test_packages.sh" \
    "$INTEGRATION_SHARD_INDEX" \
    "$INTEGRATION_TOTAL_SHARDS" \
    "$TARGET_PATTERN" \
    | sed 's|^sigs.k8s.io/kueue/|./|'
