#!/bin/bash

# Copyright 2025 The Kubernetes Authors.
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

DEST_CHART_DIR=${DEST_CHART_DIR:-bin/}

GIT_TAG=${GIT_TAG:-$(git describe --tags --dirty --always)}

STAGING_IMAGE_REGISTRY=${STAGING_IMAGE_REGISTRY:-us-central1-docker.pkg.dev/k8s-staging-images/kueue}
IMAGE_REGISTRY=${IMAGE_REGISTRY:-${STAGING_IMAGE_REGISTRY}}
HELM_CHART_REPO=${HELM_CHART_REPO:-${STAGING_IMAGE_REGISTRY}/charts}

HELM=${HELM:-helm}
YQ=${YQ:-yq}

readonly k8s_registry="registry.k8s.io/kueue"
# This regex matches only Kueue versions for which the images are
# going to be promoted to registry.k8s.io.
readonly promoted_version_regex='^v([0-9]+)(\.[0-9]+){1,2}$'

if [[ ${GIT_TAG} =~ ${promoted_version_regex} ]]
then
	IMAGE_REGISTRY=${k8s_registry}
fi
# Strip leading v from version
chart_version="${GIT_TAG/#v/}"

default_image_repo=$(${YQ} ".kueuePopulator.image.repository" charts/kueue-populator/values.yaml)
readonly default_image_repo

default_image_tag=$(${YQ} ".kueuePopulator.image.tag" charts/kueue-populator/values.yaml)
readonly default_image_tag

${HELM} dependency build charts/kueue-populator

# Update the image repo, tag and policy
${YQ}  e  ".kueuePopulator.image.repository = \"${IMAGE_REGISTRY}/kueue-populator\" | .kueuePopulator.image.tag = \"${GIT_TAG}\" | .kueuePopulator.image.pullPolicy = \"IfNotPresent\"" -i charts/kueue-populator/values.yaml

# TODO: consider signing it
${HELM} package --version "${chart_version}" --app-version "${GIT_TAG}" charts/kueue-populator -d "${DEST_CHART_DIR}"

# Revert the image changes
${YQ}  e  ".kueuePopulator.image.repository = \"${default_image_repo}\" | .kueuePopulator.image.tag = \"${default_image_tag}\" | .kueuePopulator.image.pullPolicy = \"IfNotPresent\"" -i charts/kueue-populator/values.yaml

if [ "${HELM_CHART_PUSH:-false}" = "true" ]; then
  ${HELM} push "${DEST_CHART_DIR}/kueue-populator-${chart_version}.tgz" "oci://${HELM_CHART_REPO}"
fi
