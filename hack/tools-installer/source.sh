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

# Reuse source-built tools until their identity or binary contents change.
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
go_identity=$("$go_cmd" env GOVERSION GOHOSTOS GOHOSTARCH GOFLAGS | tr '\n' ' ')
identity="$module $package $version $go_identity CGO_ENABLED=${CGO_ENABLED:-default}"
marker="$binary.source-version"

sha256() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    else
        shasum -a 256 "$1" | awk '{print $1}'
    fi
}

if [[ -x "$binary" && -f "$marker" && "$(sed -n '1p' "$marker")" == "$identity" && "$(sed -n '2p' "$marker")" == "$(sha256 "$binary")" ]]; then
    exit 0
fi
retry_script="$(dirname "${BASH_SOURCE[0]}")/../testing/retry.sh"
bash "$retry_script" --stream -- \
    env GOBIN="$(dirname "$binary")" GOOS="$host_os" GOARCH="$host_arch" \
    "$go_cmd" install "$package@$version"
printf '%s\n%s\n' "$identity" "$(sha256 "$binary")" > "$marker"
