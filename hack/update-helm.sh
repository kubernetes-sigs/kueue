#!/usr/bin/env bash

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