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

STAGING_IMAGE_REGISTRY=${STAGING_IMAGE_REGISTRY:-us-central1-docker.pkg.dev/k8s-staging-images/kueue}
IMAGE_REGISTRY=${IMAGE_REGISTRY:-${STAGING_IMAGE_REGISTRY}}
HELM_CHART_REPO=${HELM_CHART_REPO:-${STAGING_IMAGE_REGISTRY}/charts}

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

# Back up the values files before editing them. The tree is restored from
# these copies after packaging: a yq-based revert would not restore them,
# because clearing a tag writes the node double-quoted (`tag: ""`), and
# writing the default value back keeps that quoted style (`tag: "main"`),
# leaving the working tree dirty.
backup_kueue_values=$(mktemp)
backup_kueue_populator_values=$(mktemp)
backup_kueue_priority_booster_values=$(mktemp)
cp charts/kueue/values.yaml "${backup_kueue_values}"
cp cmd/experimental/kueue-populator/charts/kueue-populator/values.yaml "${backup_kueue_populator_values}"
cp cmd/experimental/kueue-priority-booster/charts/kueue-priority-booster/values.yaml "${backup_kueue_priority_booster_values}"

# Update the image repo, tag and policy
${YQ}  e  ".controllerManager.manager.image.repository = \"${IMAGE_REGISTRY}/kueue\" | .controllerManager.manager.image.tag = \"\" | .controllerManager.manager.image.pullPolicy = \"IfNotPresent\"" -i charts/kueue/values.yaml

# Update the KueueViz images repo, tag and policy in values.yaml
${YQ}  e  ".kueueViz.backend.image.repository = \"${IMAGE_REGISTRY}/kueueviz-backend\" | .kueueViz.backend.image.tag = \"\" | .kueueViz.backend.image.pullPolicy = \"IfNotPresent\"" -i charts/kueue/values.yaml
${YQ}  e  ".kueueViz.frontend.image.repository = \"${IMAGE_REGISTRY}/kueueviz-frontend\" | .kueueViz.frontend.image.tag = \"\" | .kueueViz.frontend.image.pullPolicy = \"IfNotPresent\"" -i charts/kueue/values.yaml

# Update the KueuePopulator repo, tag and policy
${YQ}  e  ".kueuePopulator.image.repository = \"${IMAGE_REGISTRY}/kueue-populator\" | .kueuePopulator.image.tag = \"\" | .kueuePopulator.image.pullPolicy = \"IfNotPresent\"" -i cmd/experimental/kueue-populator/charts/kueue-populator/values.yaml

# Update the kueue-priority-booster repo, tag and policy
${YQ}  e  ".kueuePriorityBooster.image.repository = \"${IMAGE_REGISTRY}/kueue-priority-booster\" | .kueuePriorityBooster.image.tag = \"\" | .kueuePriorityBooster.image.pullPolicy = \"IfNotPresent\"" -i cmd/experimental/kueue-priority-booster/charts/kueue-priority-booster/values.yaml

# The experimental charts bundle the kueue chart via `helm dependency build`,
# which packs charts/kueue as it sits on disk. Its committed Chart.yaml
# appVersion then becomes the image tag fallback for every bundled image
# (controller, kueueViz backend and frontend), so whenever GIT_TAG differs
# from the committed appVersion the bundled charts render a stale tag while
# --app-version fixes only the top-level charts. Set the appVersion to
# GIT_TAG for the packaging window and restore the checkout metadata from a
# byte backup afterwards, next to the values files. The chart `version` stays
# untouched: the experimental charts constrain it (e.g. `~0.20.7`).
backup_kueue_chart_metadata=$(mktemp)
cp charts/kueue/Chart.yaml "${backup_kueue_chart_metadata}"
${YQ}  e  ".appVersion = \"${GIT_TAG}\"" -i charts/kueue/Chart.yaml

# TODO: consider signing it
rm -Rf cmd/experimental/kueue-populator/charts/kueue-populator/charts
rm -f cmd/experimental/kueue-populator/charts/kueue-populator/Chart.lock
rm -Rf cmd/experimental/kueue-priority-booster/charts/kueue-priority-booster/charts
rm -f cmd/experimental/kueue-priority-booster/charts/kueue-priority-booster/Chart.lock
${HELM} package --version "${chart_version}" --app-version "${GIT_TAG}" charts/kueue -d "${DEST_CHART_DIR}"
${HELM} dependency build cmd/experimental/kueue-populator/charts/kueue-populator
${HELM} package --version "${chart_version}" --app-version "${GIT_TAG}" cmd/experimental/kueue-populator/charts/kueue-populator -d "${DEST_CHART_DIR}"
${HELM} dependency build cmd/experimental/kueue-priority-booster/charts/kueue-priority-booster
${HELM} package --version "${chart_version}" --app-version "${GIT_TAG}" cmd/experimental/kueue-priority-booster/charts/kueue-priority-booster -d "${DEST_CHART_DIR}"

# Restore the values files and the kueue chart metadata from the backups
# taken before packaging
cp "${backup_kueue_values}" charts/kueue/values.yaml
cp "${backup_kueue_populator_values}" cmd/experimental/kueue-populator/charts/kueue-populator/values.yaml
cp "${backup_kueue_priority_booster_values}" cmd/experimental/kueue-priority-booster/charts/kueue-priority-booster/values.yaml
cp "${backup_kueue_chart_metadata}" charts/kueue/Chart.yaml
rm -f "${backup_kueue_values}" "${backup_kueue_populator_values}" "${backup_kueue_priority_booster_values}" "${backup_kueue_chart_metadata}"

if [ "$HELM_CHART_PUSH" = "true" ]; then
  ${HELM} push "${DEST_CHART_DIR}/kueue-${chart_version}.tgz" "oci://${HELM_CHART_REPO}"
  ${HELM} push "${DEST_CHART_DIR}/kueue-populator-${chart_version}.tgz" "oci://${HELM_CHART_REPO}"
  ${HELM} push "${DEST_CHART_DIR}/kueue-priority-booster-${chart_version}.tgz" "oci://${HELM_CHART_REPO}"
fi
