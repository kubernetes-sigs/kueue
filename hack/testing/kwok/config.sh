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

# Shared paths.
KWOK_TEST_DIR="$(cd "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT_DIR="$(cd "${KWOK_TEST_DIR}/../../.." && pwd -P)"
ARTIFACTS="${ARTIFACTS:-${ROOT_DIR}/artifacts}"
KWOK_KUBECONFIG="${KWOK_KUBECONFIG:-${ARTIFACTS}/kwok-kubeconfig}"

# Shared test configuration.
E2E_MODE="${E2E_MODE:-ci}"
KWOK_CLUSTER_NAME="${KWOK_CLUSTER_NAME:-kueue-kwok}"
KWOK_RUNTIME="${KWOK_RUNTIME:-binary}"
KWOK_NODE_COUNT="${KWOK_NODE_COUNT:-1}"
KWOK_CLUSTER_TIMEOUT="${KWOK_CLUSTER_TIMEOUT:-5m}"
KWOK_DELETE_TIMEOUT_SECONDS="${KWOK_DELETE_TIMEOUT_SECONDS:-60}"

# Executables prepared by the Make target.
KWOKCTL="${KWOKCTL:-${ROOT_DIR}/bin/kwokctl}"
KUEUE_MANAGER="${KUEUE_MANAGER:-${ROOT_DIR}/bin/manager}"

# Shared resource files.
KUEUE_MANAGER_CONFIG_TEMPLATE="${KWOK_TEST_DIR}/manager-config.yaml"
KUEUE_BOOTSTRAP_RESOURCES="${KWOK_TEST_DIR}/bootstrap.yaml"

# Runtime state.
KUEUE_MANAGER_PID=""
