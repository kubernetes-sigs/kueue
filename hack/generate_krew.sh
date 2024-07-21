#!/usr/bin/env bash

# Copyright 2024 The Kubernetes Authors.
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

# Resolve the absolute path of the directory containing the script
SCRIPT_DIR=$(realpath "$(dirname "${BASH_SOURCE[0]}")")
REPO_ROOT="$SCRIPT_DIR/.."

docker run --rm -v "$REPO_ROOT":/home/app ghcr.io/rajatjindal/krew-release-bot:v0.0.46 krew-release-bot template --tag "$1" --template-file .krew.yaml > "$REPO_ROOT"/kueue.yaml