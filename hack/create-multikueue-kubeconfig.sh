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

KUBECONFIG_OUT=${1:-kubeconfig}
MULTIKUEUE_SA=multikueue-sa
NAMESPACE=kueue-system

echo "Creating a custom MultiKueue Role and Service Account"
kubectl apply -f - <<EOF
apiVersion: v1
kind: ServiceAccount
metadata:
  name: ${MULTIKUEUE_SA}
  namespace: ${NAMESPACE}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: multikueue-role
rules:
- apiGroups:
  - batch
  resources:
  - jobs
  verbs:
  - create
  - delete
  - get
  - list
  - watch
- apiGroups:
  - batch
  resources:
  - jobs/status
  verbs:
  - get
- apiGroups:
  - jobset.x-k8s.io
  resources:
  - jobsets
  verbs:
  - create
  - delete
  - get
  - list
  - watch
- apiGroups:
  - jobset.x-k8s.io
  resources:
  - jobsets/status
  verbs:
  - get
- apiGroups:
  - kueue.x-k8s.io
  resources:
  - workloads
  verbs:
  - create
  - delete
  - get
  - list
  - watch
- apiGroups:
  - kueue.x-k8s.io
  resources:
  - workloads/status
  verbs:
  - get
  - patch
  - update
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: multikueue-crb
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: multikueue-role
subjects:
- kind: ServiceAccount
  name: ${MULTIKUEUE_SA}
  namespace: ${NAMESPACE}
EOF

SA_SECRET_NAME=$(kubectl get -n ${NAMESPACE} sa/${MULTIKUEUE_SA} -o "jsonpath={.secrets[0]..name}")
if [ -z $SA_SECRET_NAME ]
then
# Create the secret and bind it to the desired SA
kubectl apply -f - <<EOF
apiVersion: v1
kind: Secret
type: kubernetes.io/service-account-token
metadata:
  name: ${MULTIKUEUE_SA}
  namespace: ${NAMESPACE}
  annotations:
    kubernetes.io/service-account.name: "${MULTIKUEUE_SA}"
EOF

SA_SECRET_NAME=${MULTIKUEUE_SA}
fi

# Note: service account token is stored base64-encoded in the secret but must
# be plaintext in kubeconfig.
SA_TOKEN=$(kubectl get -n ${NAMESPACE} secrets/${SA_SECRET_NAME} -o "jsonpath={.data['token']}" | base64 -d)
CA_CERT=$(kubectl get -n ${NAMESPACE} secrets/${SA_SECRET_NAME} -o "jsonpath={.data['ca\.crt']}")

# Extract cluster IP from the current context
CURRENT_CONTEXT=$(kubectl config current-context)
CURRENT_CLUSTER=$(kubectl config view -o jsonpath="{.contexts[?(@.name == \"${CURRENT_CONTEXT}\"})].context.cluster}")
CURRENT_CLUSTER_ADDR=$(kubectl config view -o jsonpath="{.clusters[?(@.name == \"${CURRENT_CLUSTER}\"})].cluster.server}")

echo "Writing kubeconfig in ${KUBECONFIG_OUT}"
cat > ${KUBECONFIG_OUT} <<EOF
apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: ${CA_CERT}
    server: ${CURRENT_CLUSTER_ADDR}
  name: ${CURRENT_CLUSTER}
contexts:
- context:
    cluster: ${CURRENT_CLUSTER}
    user: ${CURRENT_CLUSTER}-${MULTIKUEUE_SA}
  name: ${CURRENT_CONTEXT}
current-context: ${CURRENT_CONTEXT}
kind: Config
preferences: {}
users:
- name: ${CURRENT_CLUSTER}-${MULTIKUEUE_SA}
  user:
    token: ${SA_TOKEN}
EOF

echo "Done!"
