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

# Set the source and destination directories
SRC_WEBHOOK_DIR=config/components/webhook
DEST_WEBHOOK_DIR=charts/kueue/templates/webhook

# Define tools
YQ=./bin/yq
YAML_PROCESSOR=./bin/yaml-processor

# Process the manifests.yaml file using yq
$YQ -N -s '.kind' ${SRC_WEBHOOK_DIR}/manifests.yaml

# Move webhook configuration files to the source directory
files=("MutatingWebhookConfiguration.yml" "ValidatingWebhookConfiguration.yml")
for f in "${files[@]}"; do
  mv "$f" ${SRC_WEBHOOK_DIR}/
done

# Process the YAML files using the YAML processor
$YAML_PROCESSOR hack/processing-plan.yaml

# Combine the webhook configuration files into a single file
{
  cat ${DEST_WEBHOOK_DIR}/MutatingWebhookConfiguration.yml
  echo "---"
  cat ${DEST_WEBHOOK_DIR}/ValidatingWebhookConfiguration.yml
} > ${DEST_WEBHOOK_DIR}/webhook.yaml

# Clean up temporary files
rm ${SRC_WEBHOOK_DIR}/MutatingWebhookConfiguration.yml ${SRC_WEBHOOK_DIR}/ValidatingWebhookConfiguration.yml
rm ${DEST_WEBHOOK_DIR}/MutatingWebhookConfiguration.yml ${DEST_WEBHOOK_DIR}/ValidatingWebhookConfiguration.yml
