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

# Reuse source-built tools until their version or build configuration changes.
set -o errexit
set -o nounset
set -o pipefail

binary=$1
module=$2
version=$3
package=$4
go_cmd=${GO_CMD:-go}
host_os=$("$go_cmd" env GOHOSTOS)
host_arch=$("$go_cmd" env GOHOSTARCH)
identity="$module $package $version $("$go_cmd" env GOVERSION GOHOSTOS GOHOSTARCH GOFLAGS) CGO_ENABLED=${CGO_ENABLED:-default}"
marker="$binary.source-version"
if [[ -x "$binary" && -f "$marker" && "$(cat "$marker")" == "$identity" ]]; then
    exit 0
fi
retry_script="$(dirname "${BASH_SOURCE[0]}")/../testing/retry.sh"
bash "$retry_script" --stream -- \
    env GOBIN="$(dirname "$binary")" GOOS="$host_os" GOARCH="$host_arch" \
    "$go_cmd" install "$package@$version"
printf '%s\n' "$identity" > "$marker"
