#!/usr/bin/env bash

# Copyright 2023 The Kubernetes Authors.
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

GO_CMD=${1:-go}
KUEUE_ROOT=$(realpath $(dirname ${BASH_SOURCE[0]})/..)
CODEGEN_PKG=$($GO_CMD list -m -f "{{.Dir}}" k8s.io/code-generator)

cd $(dirname ${BASH_SOURCE[0]})/..

source "${CODEGEN_PKG}/generate-groups.sh" \
  "all" \
  sigs.k8s.io/kueue/client-go \
  sigs.k8s.io/kueue/apis \
  kueue:v1beta1 \
  --go-header-file ${KUEUE_ROOT}/hack/boilerplate.go.txt

# Future releases of of code-generator add better support for out of GOPATH generation,
# check https://github.com/kubernetes/code-generator/blob/master/examples/hack/update-codegen.sh for details,
# for now we should just move the generated code to `client-go`.
if  [ -d sigs.k8s.io/kueue/client-go ]
then
  echo "Out of GOPATH generation, move the generated files to ../client-go"
  if  [ -d ${KUEUE_ROOT}/client-go ]
  then
    rm -r ${KUEUE_ROOT}/client-go
  fi
  mv sigs.k8s.io/kueue/client-go ${KUEUE_ROOT}/client-go
fi
