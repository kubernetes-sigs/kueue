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
SRC_CRD_DIR=config/components/crd/bases
SRC_RBAC_DIR=config/components/rbac
SRC_VISIBILITY_DIR=config/components/visibility

DEST_CRD_DIR=charts/kueue/templates/crd
DEST_RBAC_DIR=charts/kueue/templates/rbac
DEST_VISIBILITY_DIR=charts/kueue/templates/visibility

YQ=./bin/yq

# Create the destination directory if it doesn't exist
mkdir -p ${DEST_CRD_DIR} ${DEST_RBAC_DIR} ${DEST_VISIBILITY_DIR}

# Add more excluded files separated by spaces
EXCLUDE_FILES='kustomization.yaml'

# Copy all YAML files from the source directory to the destination directory
cp ${SRC_CRD_DIR}/*.yaml ${DEST_CRD_DIR}
cp ${SRC_VISIBILITY_DIR}/*.yaml ${DEST_VISIBILITY_DIR}
find $SRC_RBAC_DIR -name "*.yaml" $(printf "! -name %s " $EXCLUDE_FILES) -exec cp "{}" $DEST_RBAC_DIR \;

search_cert_line="  annotations:"
replace_cert_line=$(
  cat <<'EOF'
    {{- if .Values.enableCertManager }}
    cert-manager.io/inject-ca-from: {{ .Release.Namespace }}/{{ include "kueue.fullname" . }}-serving-cert
    {{- end }}
EOF
)

search_webhook_line="spec:"
replace_webhook_line=$(
  cat <<'EOF'
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
for output_file in ${DEST_CRD_DIR}/*.yaml; do
  input_file="${output_file%.yaml}.yaml.test"
  mv "$output_file" "$input_file"
  : >$output_file
  while IFS= read -r line; do
    echo "$line" >>"$output_file"
    if [[ $line == "$search_cert_line" ]]; then
      echo "$replace_cert_line" >>"$output_file"
    elif [[ $line == "$search_webhook_line" ]]; then
      echo "$replace_webhook_line" >>"$output_file"
    fi
  done <"$input_file"
  rm $input_file
done

# Add RBAC files, replace names, namespaces in helm format, remove document separators (---)
for output_file in ${DEST_RBAC_DIR}/*.yaml; do
  if [ "$(cat $output_file | $YQ '.metadata | has("name")')" = "true" ]; then
    $YQ -N -i '.metadata.name |= "{{ include \"kueue.fullname\" . }}-" + .' $output_file
  fi
  if [ "$(cat $output_file | $YQ '.metadata | has("namespace")')" = "true" ]; then
    $YQ -N -i '.metadata.namespace = "{{ .Release.Namespace }}"' $output_file
  fi
  if [ "$(cat $output_file | $YQ '.roleRef | has("name")')" = "true" ]; then
    $YQ -N -i '.roleRef.name |= "{{ include \"kueue.fullname\" . }}-" + .' $output_file
  fi
  if [ "$(cat $output_file | $YQ '.subjects.[] | has("name")')" = "true" ]; then
    $YQ -N -i '.subjects.[].name |= "{{ include \"kueue.fullname\" . }}-" + .' $output_file
  fi
  if [ "$(cat $output_file | $YQ '.subjects.[] | has("namespace")')" = "true" ]; then
    $YQ -N -i '.subjects.[].namespace = "{{ .Release.Namespace }}"' $output_file
  fi
done

# Add visibility files, replace names, namespaces in helm format
for output_file in ${DEST_VISIBILITY_DIR}/*.yaml; do
  # The name of the v1alpha1.visibility.kueue.x-k8s.io APIService needs to remain unchanged.
  if [ "$(cat $output_file | $YQ '.metadata | has("name")')" = "true" ] &&
    [ "$(cat $output_file | $YQ '.metadata.name | (. == "v1alpha1*")')" = "false" ]; then
    $YQ -N -i '.metadata.name |= "{{ include \"kueue.fullname\" . }}-" + .' $output_file
  fi
  # The namespace of the visibility-server-auth-reader rolebinding needs to remain unchanged.
  if [ "$(cat $output_file | $YQ '.metadata | has("namespace")')" = "true" ] &&
    [ "$(cat $output_file | $YQ '.metadata.namespace | (. == "kube-system")')" = "false" ]; then
    $YQ -N -i '.metadata.namespace = "{{ .Release.Namespace }}"' $output_file
  fi
  if [ "$(cat $output_file | $YQ '.spec.service | has("name")')" = "true" ]; then
    $YQ -N -i '.spec.service.name |= "{{ include \"kueue.fullname\" . }}-" + .' $output_file
  fi
  if [ "$(cat $output_file | $YQ '.spec.service | has("namespace")')" = "true" ]; then
    $YQ -N -i '.spec.service.namespace = "{{ .Release.Namespace }}"' $output_file
  fi
  if [ "$(cat $output_file | $YQ '.subjects.[] | has("namespace")')" = "true" ]; then
    $YQ -N -i '.subjects.[].namespace = "{{ .Release.Namespace }}"' $output_file
  fi
done
