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

# This script generates a Krew-compatible plugin manifest.

VERSION=${VERSION:-$(git describe --tags | sed 's/^v//g')}

# Generate the manifest for a single platform.
function generate_platform {
    cat <<EOF
  - selector:
      matchLabels:
        os: "${1}"
        arch: "${2}"
    uri: https://github.com/kubernetes-sigs/kueue/releases/download/v${VERSION}/kubectl-kueue-${1}-${2}.tar.gz
    sha256: $(curl -L https://github.com/kubernetes-sigs/kueue/releases/download/v${VERSION}/kubectl-kueue-${1}-${2}.tar.gz | shasum -a 256 - | awk '{print $1}')
    bin: "./kubectl-kueue-${1}-${2}/${3}"
EOF
}

# shellcheck disable=SC2129
cat <<EOF > kueue.yaml
apiVersion: krew.googlecontainertools.github.com/v1alpha2
kind: Plugin
metadata:
  name: kueue
spec:
  version: "v${VERSION}"
  shortDescription: Controls Kueue queueing manager.
  homepage: https://kueue.sigs.k8s.io/docs/reference/kubectl-kueue/
  description: |
    The kubectl-kueue plugin, kueuectl, allows you to list, create, resume
    and stop kueue resources such as clusterqueues, localqueues and workloads.

    See the documentation for more information: https://kueue.sigs.k8s.io/docs/reference/kubectl-kueue/
  caveats: |
    Requires the Kueue operator to be installed:
      https://kueue.sigs.k8s.io/docs/installation/
  platforms:
EOF

generate_platform linux amd64 kubectl-kueue >> kueue.yaml
generate_platform linux arm64 kubectl-kueue >> kueue.yaml
generate_platform darwin amd64 kubectl-kueue >> kueue.yaml
generate_platform darwin arm64 kubectl-kueue >> kueue.yaml

echo "To publish to the krew index, create a pull request on https://github.com/kubernetes-sigs/krew-index/tree/master/plugins to update kueue.yaml with the newly generated kueue.yaml."