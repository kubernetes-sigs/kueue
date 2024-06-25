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

# This script sends a USR2 signal to a running kueue binary in the kueue-system namespace

set -o errexit
set -o nounset
set -o pipefail

NAMESPACE=${NAMESPACE:-kueue-system}
LEASE_NAME=${LEASE_NAME:-c1f6bfd2.kueue.x-k8s.io}
DEBUG_IMAGE=${DEBUG_IMAGE:-us-central1-docker.pkg.dev/k8s-staging-images/kueue/debug:main}

leader=$(kubectl get lease -n ${NAMESPACE} ${LEASE_NAME} -o jsonpath='{.spec.holderIdentity}' | cut -d '_' -f 1)

# When the FOLLOW environment variable is set, output the logs.
if [ -v FOLLOW ]; then
    kubectl logs -n ${NAMESPACE} ${leader} -f --tail=1 &
fi

kubectl debug -n ${NAMESPACE} ${leader} --image=${DEBUG_IMAGE} --target=manager --profile=restricted --image-pull-policy=IfNotPresent -- sh -c 'kill -USR2 1'
