#!/bin/bash

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

DEST_CHART_DIR=${DEST_CHART_DIR:-bin/}

EXTRA_TAG=${EXTRA_TAG:-$(git branch --show-current)} 
GIT_TAG=${GIT_TAG:-$(git describe --tags --dirty --always)}

STAGING_IMAGE_REGISTRY=${STAGING_IMAGE_REGISTRY:-us-central1-docker.pkg.dev/k8s-staging-images}
IMAGE_REGISTRY=${IMAGE_REGISTRY:-${STAGING_IMAGE_REGISTRY}/kueue}
HELM_CHART_REPO=${HELM_CHART_REPO:-${STAGING_IMAGE_REGISTRY}/charts}
IMAGE_REPO=${IMAGE_REPO:-${IMAGE_REGISTRY}/kueue}

HELM=${HELM:-./bin/helm}
YQ=${YQ:-./bin/yq}

readonly k8s_registry="registry.k8s.io/kueue"
readonly semver_regex='^v([0-9]+)(\.[0-9]+){1,2}$'

image_repository=${IMAGE_REPO}
chart_version=${GIT_TAG}
if [[ ${EXTRA_TAG} =~ ${semver_regex} ]]
then
	image_repository=${k8s_registry}/kueue
	chart_version=${EXTRA_TAG}
fi

readonly default_image_repo=$(${YQ} ".controllerManager.manager.image.repository" charts/kueue/values.yaml)
# Update the image repo, tag and policy
${YQ}  e  ".controllerManager.manager.image.repository = \"${image_repository}\" | .controllerManager.manager.image.tag = \"${chart_version}\" | .controllerManager.manager.image.pullPolicy = \"IfNotPresent\"" -i charts/kueue/values.yaml

${HELM} package --version ${chart_version} --app-version ${chart_version} charts/kueue -d ${DEST_CHART_DIR}

# Revert the image changes
${YQ}  e  ".controllerManager.manager.image.repository = \"${default_image_repo}\" | .controllerManager.manager.image.tag = \"main\" | .controllerManager.manager.image.pullPolicy = \"Always\"" -i charts/kueue/values.yaml

${HELM} push bin/kueue-${chart_version}.tgz oci://${HELM_CHART_REPO}
