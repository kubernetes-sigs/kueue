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

set -o errexit
set -o nounset
set -o pipefail

DEST_CHART_DIR=${DEST_CHART_DIR:-bin/}

GIT_TAG=${GIT_TAG:-$(git describe --tags --dirty --always)}

STAGING_IMAGE_REGISTRY=${STAGING_IMAGE_REGISTRY:-us-central1-docker.pkg.dev/k8s-staging-images}
IMAGE_REGISTRY=${IMAGE_REGISTRY:-${STAGING_IMAGE_REGISTRY}/kueue}
HELM_CHART_REPO=${HELM_CHART_REPO:-${STAGING_IMAGE_REGISTRY}/kueue/charts}

HELM=${HELM:-./bin/helm}
YQ=${YQ:-./bin/yq}

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

default_image_repo=$(${YQ} ".controllerManager.manager.image.repository" charts/kueue/values.yaml)
readonly default_image_repo

default_kueueviz_backend_image_repo=$(${YQ} ".kueueViz.backend.image.repository" charts/kueue/values.yaml)
readonly default_kueueviz_backend_image_repo

default_kueueviz_frontend_image_repo=$(${YQ} ".kueueViz.frontend.image.repository" charts/kueue/values.yaml)
readonly default_kueueviz_frontend_image_repo

# Update the image repo, tag and policy
${YQ}  e  ".controllerManager.manager.image.repository = \"${IMAGE_REGISTRY}/kueue\" | .controllerManager.manager.image.tag = \"${GIT_TAG}\" | .controllerManager.manager.image.pullPolicy = \"IfNotPresent\"" -i charts/kueue/values.yaml

# Update the KueueViz images repo, tag and policy in values.yaml
${YQ}  e  ".kueueViz.backend.image.repository = \"${IMAGE_REGISTRY}/kueueviz-backend\" | .kueueViz.backend.image.tag = \"${GIT_TAG}\" | .kueueViz.backend.image.pullPolicy = \"IfNotPresent\"" -i charts/kueue/values.yaml
${YQ}  e  ".kueueViz.frontend.image.repository = \"${IMAGE_REGISTRY}/kueueviz-frontend\" | .kueueViz.frontend.image.tag = \"${GIT_TAG}\" | .kueueViz.frontend.image.pullPolicy = \"IfNotPresent\"" -i charts/kueue/values.yaml

# TODO: consider signing it
${HELM} package --version "${chart_version}" --app-version "${GIT_TAG}" charts/kueue -d "${DEST_CHART_DIR}"

# Revert the image changes
${YQ}  e  ".controllerManager.manager.image.repository = \"${default_image_repo}\" | del(.controllerManager.manager.image.tag) | .controllerManager.manager.image.pullPolicy = \"Always\"" -i charts/kueue/values.yaml

# Revert the KueueViz image changes
${YQ}  e  ".kueueViz.backend.image.repository = \"${default_kueueviz_backend_image_repo}\" | del(.kueueViz.backend.image.tag) | .kueueViz.backend.image.pullPolicy = \"Always\"" -i charts/kueue/values.yaml
${YQ}  e  ".kueueViz.frontend.image.repository = \"${default_kueueviz_frontend_image_repo}\" | del(.kueueViz.frontend.image.tag) | .kueueViz.frontend.image.pullPolicy = \"Always\"" -i charts/kueue/values.yaml

if [ "$HELM_CHART_PUSH" = "true" ]; then
  ${HELM} push "${DEST_CHART_DIR}/kueue-${chart_version}.tgz" "oci://${HELM_CHART_REPO}"
fi
