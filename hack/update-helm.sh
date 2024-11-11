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
SRC_CRD_DIR=config/components/crd/bases
SRC_RBAC_DIR=config/components/rbac
SRC_WEBHOOK_DIR=config/components/webhook
SRC_VISIBILITY_DIR=config/components/visibility
SRC_VISIBILITY_APF_DIR=config/components/visibility-apf

DEST_CRD_DIR=charts/kueue/templates/crd
DEST_RBAC_DIR=charts/kueue/templates/rbac
DEST_WEBHOOK_DIR=charts/kueue/templates/webhook
DEST_VISIBILITY_DIR=charts/kueue/templates/visibility
DEST_VISIBILITY_APF_DIR=charts/kueue/templates/visibility-apf

YQ=./bin/yq
SED=${SED:-/usr/bin/sed}

# Create the destination directory if it doesn't exist
mkdir -p ${DEST_CRD_DIR} "${DEST_RBAC_DIR}" ${DEST_WEBHOOK_DIR} ${DEST_VISIBILITY_DIR}

# Add more excluded files separated by spaces
EXCLUDE_FILES='kustomization.yaml kustomizeconfig.yaml'
# shellcheck disable=SC2086
EXCLUDE_FILES_ARGS=$(printf "! -name %s " $EXCLUDE_FILES)

# Copy all YAML files from the source directory to the destination directory
cp ${SRC_CRD_DIR}/*.yaml ${DEST_CRD_DIR}
# shellcheck disable=SC2086
find $SRC_RBAC_DIR -name "*.yaml" $EXCLUDE_FILES_ARGS -exec cp "{}" $DEST_RBAC_DIR \;
# shellcheck disable=SC2086
find $SRC_WEBHOOK_DIR -name "*.yaml" $EXCLUDE_FILES_ARGS -exec cp "{}" $DEST_WEBHOOK_DIR \;
# shellcheck disable=SC2086
find $SRC_VISIBILITY_DIR -name "*.yaml" $EXCLUDE_FILES_ARGS -exec cp "{}" $DEST_VISIBILITY_DIR \;
# shellcheck disable=SC2086
find $SRC_VISIBILITY_APF_DIR -name "*.yaml" $EXCLUDE_FILES_ARGS -exec cp "{}" $DEST_VISIBILITY_APF_DIR \;
$YQ -N -s '.kind' ${DEST_WEBHOOK_DIR}/manifests.yaml
rm ${DEST_WEBHOOK_DIR}/manifests.yaml
files=("MutatingWebhookConfiguration.yml" "ValidatingWebhookConfiguration.yml")
for f in "${files[@]}"; do
  mv "$f" ${DEST_WEBHOOK_DIR}/
done

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

search_service_line="spec:"
replace_service_line=$(
  cat <<'EOF'
  {{- if .Values.webhookService.ipDualStack.enabled }}
  ipFamilies: {{ toYaml .Values.webhookService.ipDualStack.ipFamilies | nindent 4 }}
  ipFamilyPolicy: {{ .Values.webhookService.ipDualStack.ipFamilyPolicy }}
  {{- end }}
  type: {{ .Values.webhookService.type }}
  selector:
  {{- include "kueue.selectorLabels" . | nindent 4 }}
  ports:
  {{- .Values.webhookService.ports | toYaml | nindent 2 -}}
EOF
)

search_webhook_pod_mutate="        path: /mutate--v1-pod"
search_webhook_pod_validate="        path: /validate--v1-pod"
search_webhook_deployment_mutate="        path: /mutate-apps-v1-deployment"
search_webhook_deployment_validate="        path: /validate-apps-v1-deployment"
search_mutate_webhook_annotations='  name: '\''{{ include "kueue.fullname" . }}-mutating-webhook-configuration'\'''
search_validate_webhook_annotations='  name: '\''{{ include "kueue.fullname" . }}-validating-webhook-configuration'\'''
add_webhook_line=$(
  cat <<'EOF'
{{- $integrationsConfig := (fromYaml .Values.managerConfig.controllerManagerConfigYaml).integrations }}
EOF
)
add_annotations_line=$(
  cat <<'EOF'
  {{- if .Values.enableCertManager }}
  annotations:
    cert-manager.io/inject-ca-from: {{ .Release.Namespace }}/{{ include "kueue.fullname" . }}-serving-cert
  {{- end }}
  namespace: '{{ .Release.Namespace }}'
EOF
)
add_webhook_pod_mutate=$(
  cat <<'EOF'
    {{- if has "pod" $integrationsConfig.frameworks }}
    failurePolicy: Fail
    {{- else }}
    failurePolicy: Ignore
    {{- end }}
    name: mpod.kb.io
    namespaceSelector:
      {{- if and (hasKey $integrationsConfig "podOptions") (hasKey ($integrationsConfig.podOptions) "namespaceSelector") }}
        {{- toYaml $integrationsConfig.podOptions.namespaceSelector | nindent 6 -}}
      {{- else }}
      matchExpressions:
        - key: kubernetes.io/metadata.name
          operator: NotIn
          values:
            - kube-system
            - '{{ .Release.Namespace }}'
      {{- end }}
EOF
)
add_webhook_pod_validate=$(
  cat <<'EOF'
    {{- if has "pod" $integrationsConfig.frameworks }}
    failurePolicy: Fail
    {{- else }}
    failurePolicy: Ignore
    {{- end }}
    name: vpod.kb.io
    namespaceSelector:
      {{- if and (hasKey $integrationsConfig "podOptions") (hasKey ($integrationsConfig.podOptions) "namespaceSelector") }}
        {{- toYaml $integrationsConfig.podOptions.namespaceSelector | nindent 6 -}}
      {{- else }}
      matchExpressions:
        - key: kubernetes.io/metadata.name
          operator: NotIn
          values:
            - kube-system
            - '{{ .Release.Namespace }}'
      {{- end }}
EOF
)

add_webhook_deployment_mutate=$(
  cat <<'EOF'
    {{- if has "deployment" $integrationsConfig.frameworks }}
    failurePolicy: Fail
    {{- else }}
    failurePolicy: Ignore
    {{- end }}
    name: mdeployment.kb.io
    namespaceSelector:
      matchExpressions:
        - key: kubernetes.io/metadata.name
          operator: NotIn
          values:
            - kube-system
            - '{{ .Release.Namespace }}'
EOF
)
add_webhook_deployment_validate=$(
  cat <<'EOF'
    {{- if has "deployment" $integrationsConfig.frameworks }}
    failurePolicy: Fail
    {{- else }}
    failurePolicy: Ignore
    {{- end }}
    name: vdeployment.kb.io
    namespaceSelector:
      matchExpressions:
        - key: kubernetes.io/metadata.name
          operator: NotIn
          values:
            - kube-system
            - '{{ .Release.Namespace }}'
EOF
)

# Add certmanager and webhook values in the YAML files
for output_file in "${DEST_CRD_DIR}"/*.yaml; do
  input_file="${output_file%.yaml}.yaml.test"
  mv "$output_file" "$input_file"
  : > "$output_file"
  while IFS= read -r line; do
    echo "$line" >>"$output_file"
    if [[ $line == "$search_cert_line" ]]; then
      echo "$replace_cert_line" >>"$output_file"
    elif [[ $line == "$search_webhook_line" ]]; then
      echo "$replace_webhook_line" >>"$output_file"
    fi
  done < "$input_file"
  rm "$input_file"
  $SED -i '/^metadata:.*/a\  labels:\n  {{- include "kueue.labels" . | nindent 4 }}' "$output_file"
done

# Add RBAC files, replace names, namespaces in helm format, remove document separators (---)
for output_file in "${DEST_RBAC_DIR}"/*.yaml; do
  if [ "$(< "$output_file" $YQ '.metadata | has("name")')" = "true" ]; then
    $YQ -N -i '.metadata.name |= "{{ include \"kueue.fullname\" . }}-" + .' "$output_file"
  fi
  if [ "$(< "$output_file" $YQ '.metadata | has("namespace")')" = "true" ]; then
    $YQ -N -i '.metadata.namespace = "{{ .Release.Namespace }}"' "$output_file"
  fi
  if [ "$(< "$output_file" $YQ '.roleRef | has("name")')" = "true" ]; then
    $YQ -N -i '.roleRef.name |= "{{ include \"kueue.fullname\" . }}-" + .' "$output_file"
  fi
  if [ "$(< "$output_file" $YQ '.subjects.[] | has("name")')" = "true" ]; then
    $YQ -N -i '.subjects.[].name |= "{{ include \"kueue.fullname\" . }}-" + .' "$output_file"
  fi
  if [ "$(< "$output_file" $YQ '.subjects.[] | has("namespace")')" = "true" ]; then
    $YQ -N -i '.subjects.[].namespace = "{{ .Release.Namespace }}"' "$output_file"
  fi
  if [ "$(< "$output_file" $YQ '.metadata | has("labels")')" = "true" ]; then
    $SED -i '/labels:.*/a\  {{- include "kueue.labels" . | nindent 4 }}' "$output_file"
  else
    $SED -i '/^metadata:.*/a\  labels:\n  {{- include "kueue.labels" . | nindent 4 }}' "$output_file"
  fi
done

# Add webhook files, replace names, namespaces in helm format
webhook_files=(
"${DEST_WEBHOOK_DIR}/MutatingWebhookConfiguration.yml"
"${DEST_WEBHOOK_DIR}/ValidatingWebhookConfiguration.yml"
"${DEST_WEBHOOK_DIR}/service.yaml"
)
for output_file in "${webhook_files[@]}"; do
  if [ "$(< "$output_file" $YQ '.metadata | has("name")')" = "true" ]; then
    $YQ -N -i '.metadata.name |= "{{ include \"kueue.fullname\" . }}-" + .' "$output_file"
  fi
  if [ "$(< "$output_file" $YQ '.metadata | has("namespace")')" = "true" ]; then
    $YQ -N -i '.metadata.namespace = "{{ .Release.Namespace }}"' "$output_file"
  fi
  $YQ -N -i '.webhooks.[].clientConfig.service.name |= "{{ include \"kueue.fullname\" . }}-" + .' "$output_file"
  $YQ -N -i '.webhooks.[].clientConfig.service.namespace = "{{ .Release.Namespace }}"' "$output_file"
  $SED -i '/^metadata:.*/a\  labels:\n  {{- include "kueue.labels" . | nindent 4 }}' "$output_file"
done

# Add service values in the YAML files
for output_file in ${DEST_WEBHOOK_DIR}/service.yaml; do
  input_file="${output_file%.yaml}.yaml.test"
  mv "$output_file" "$input_file"
  : > "$output_file"
  while IFS= read -r line; do
    echo "$line" >>"$output_file"
    if [[ $line == "$search_service_line" ]]; then
      echo "$replace_service_line" >>"$output_file"
      break
    fi
  done <"$input_file"
  rm "$input_file"
done

# Add webhook values in the YAML files
new_files=("${DEST_WEBHOOK_DIR}/MutatingWebhookConfiguration.yml" "${DEST_WEBHOOK_DIR}/ValidatingWebhookConfiguration.yml")
for output_file in "${new_files[@]}"; do
  input_file="${output_file%.yaml}.yml.test"
  mv "$output_file" "$input_file"
  : > "$output_file"
  count=0
  while IFS= read -r line; do
    if [[ $count -gt 0 ]]; then
      ((count--))
      continue
    fi
    echo "$line" >>"$output_file"
    if [[ $line == "$search_mutate_webhook_annotations" ]]; then
      echo "$add_annotations_line" >> "$output_file"
    fi
    if [[ $line == "$search_validate_webhook_annotations" ]]; then
      echo "$add_annotations_line" >> "$output_file"
    fi
    if [[ $line == "$search_webhook_pod_mutate" ]]; then
      count=$((count+2))
      echo "$add_webhook_pod_mutate" >>"$output_file"
    fi
    if [[ $line == "$search_webhook_pod_validate" ]]; then
      count=$((count+2))
      echo "$add_webhook_pod_validate" >>"$output_file"
    fi
    if [[ $line == "$search_webhook_deployment_mutate" ]]; then
      count=$((count+2))
      echo "$add_webhook_deployment_mutate" >>"$output_file"
    fi
    if [[ $line == "$search_webhook_deployment_validate" ]]; then
      count=$((count+2))
      echo "$add_webhook_deployment_validate" >>"$output_file"
    fi
  done < "$input_file"
  rm "$input_file"
done
echo "$add_webhook_line" > ${DEST_WEBHOOK_DIR}/webhook.yaml
{
  cat ${DEST_WEBHOOK_DIR}/MutatingWebhookConfiguration.yml
  echo "---"
  cat ${DEST_WEBHOOK_DIR}/ValidatingWebhookConfiguration.yml
} >> ${DEST_WEBHOOK_DIR}/webhook.yaml
rm ${DEST_WEBHOOK_DIR}/MutatingWebhookConfiguration.yml ${DEST_WEBHOOK_DIR}/ValidatingWebhookConfiguration.yml

# Add visibility files, replace names, namespaces in helm format
for output_file in "${DEST_VISIBILITY_DIR}"/*.yaml; do
  # The name of the v1beta1.visibility.kueue.x-k8s.io APIService needs to remain unchanged.
  if [ "$(< "$output_file" $YQ '.metadata | has("name")')" = "true" ] &&
    [ "$(< "$output_file" $YQ '.metadata.name | . == "v1beta1*"')" = "false" ]; then
    $YQ -N -i '.metadata.name |= "{{ include \"kueue.fullname\" . }}-" + .' "$output_file"
  fi
  # The namespace of the visibility-server-auth-reader rolebinding needs to remain unchanged.
  if [ "$(< "$output_file" $YQ '.metadata | has("namespace")')" = "true" ] &&
    [ "$(< "$output_file" $YQ '.metadata.namespace | (. == "kube-system")')" = "false" ]; then
    $YQ -N -i '.metadata.namespace = "{{ .Release.Namespace }}"' "$output_file"
  fi
  if [ "$(< "$output_file" $YQ '.spec.service | has("name")')" = "true" ]; then
    $YQ -N -i '.spec.service.name |= "{{ include \"kueue.fullname\" . }}-" + .' "$output_file"
  fi
  if [ "$(< "$output_file" $YQ '.spec.service | has("namespace")')" = "true" ]; then
    $YQ -N -i '.spec.service.namespace = "{{ .Release.Namespace }}"' "$output_file"
  fi
  if [ "$(< "$output_file" $YQ '.subjects.[] | has("namespace")')" = "true" ]; then
    $YQ -N -i '.subjects.[].namespace = "{{ .Release.Namespace }}"' "$output_file"
  fi
  if [ "$(< "$output_file" $YQ '.kind | select(. == "Service")')" ]; then
    # shellcheck disable=SC2086
    cat <<EOT >> $output_file
  selector:
  {{- include "kueue.selectorLabels" . | nindent 4 }}
EOT
  fi
  $SED -i '/^metadata:.*/a\  labels:\n  {{- include "kueue.labels" . | nindent 4 }}' "$output_file"
done

# Replace flowcontrol version on visibility-apf directory
for output_file in "${DEST_VISIBILITY_APF_DIR}"/*.yaml; do
  $YQ -N -i '.apiVersion = "flowcontrol.apiserver.k8s.io/{{ and (eq .Capabilities.KubeVersion.Major \"1\") (eq .Capabilities.KubeVersion.Minor \"28\") | ternary \"v1beta3\" \"v1\" }}"' "$output_file"
  $YQ -N -i '.metadata.name |= "{{ include \"kueue.fullname\" . }}-" + .' "$output_file"
  $YQ -N -i '.metadata.namespace = "{{ .Release.Namespace }}"' "$output_file"
  $SED -i '/^metadata:.*/a\  labels:\n  {{- include "kueue.labels" . | nindent 4 }}' "$output_file"
  {
    echo '{{- if .Values.enableVisibilityAPF }}'
    cat "$output_file"
    echo "{{- end }}"
  } > "${output_file}.tmp"
  mv "${output_file}.tmp" "${output_file}"
done
