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

# Set the source and destination directories
SRC_DIR=config/components/crd/bases
DEST_DIR=charts/kueue/templates/crd

# Create the destination directory if it doesn't exist
mkdir -p ${DEST_DIR}

# Copy all YAML files from the source directory to the destination directory
cp ${SRC_DIR}/*.yaml ${DEST_DIR}

search_cert_line="  annotations:"
replace_cert_line=$(cat <<'EOF'
    {{- if .Values.enableCertManager }}
    cert-manager.io/inject-ca-from: {{ .Release.Namespace }}/{{ include "kueue.fullname" . }}-serving-cert
    {{- end }}
EOF
)

search_webhook_line="spec:"
replace_webhook_line=$(cat <<'EOF'
  conversion:
    strategy: Webhook
    webhook:
      clientConfig:
        service:
          name: {{ include "kueue.fullname" . }}-webhook-service
          namespace: '{{ .Release.Namespace }}'
          path: /convert
      conversionReviewVersions:
      - v1
EOF
)

# Add certmanager and webhook values in the YAML files
for output_file in ${DEST_DIR}/*.yaml; do
    input_file="${output_file%.yaml}.yaml.test"
    mv "$output_file" "$input_file"
    :> $output_file
    while IFS= read -r line; do
    echo "$line" >> "$output_file"
    if [[ $line == "$search_cert_line" ]]; then
        echo "$replace_cert_line" >> "$output_file"
    elif [[ $line == "$search_webhook_line" ]]; then
        echo "$replace_webhook_line" >> "$output_file"
    fi
    done < "$input_file"
    rm $input_file
done
