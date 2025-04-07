#!/bin/bash
set -xeou pipefail

OCP_MINOR=$(oc get clusterversion version -o jsonpath='{.status.desired.version}' | cut -d. -f2)
echo "Detected OCP minor version: ${OCP_MINOR}"

TARGET=$((OCP_MINOR - 3))
echo "Target cert-manager minor version: ${TARGET}"

# Get package manifest JSON
PACKAGE_MANIFEST=$(oc get packagemanifest openshift-cert-manager-operator -n openshift-marketplace -o json)

# Find version-specific channels matching pattern stable-vX.Y
COMPATIBLE_CHANNELS=$(echo "$PACKAGE_MANIFEST" | jq -r --arg target "$TARGET" '
  .status.channels[] | 
  select(.name | test("stable-v[0-9]+\\.[0-9]+$")) |
  .name as $name |
  ($name | split("-v")[1] | split(".") | map(tonumber)) as $version |
  {
    name: $name,
    major: $version[0],
    minor: $version[1],
    currentCSV: .currentCSV
  } |
  select(.minor <= ($target | tonumber))
')

SELECTED_CHANNEL=$(echo "$COMPATIBLE_CHANNELS" | jq -s 'sort_by(.minor) | last')

# Fallback to default channel if no version-specific match
if [[ -z "$SELECTED_CHANNEL" || "$SELECTED_CHANNEL" == "null" ]]; then
  echo "No version-specific channel found, using default channel"
  DEFAULT_CHANNEL=$(echo "$PACKAGE_MANIFEST" | jq -r '.status.defaultChannel')
  SELECTED_CHANNEL=$(echo "$PACKAGE_MANIFEST" | jq -r --arg ch "$DEFAULT_CHANNEL" '
    .status.channels[] | select(.name == $ch) | {
      name: .name,
      major: (.name | split("-v")[1] | split(".")[0] | tonumber),
      minor: (.name | split("-v")[1] | split(".")[1] | tonumber),
      currentCSV: .currentCSV
    }
  ')
fi

CHANNEL_NAME=$(echo "$SELECTED_CHANNEL" | jq -r '.name')
STARTING_CSV=$(echo "$SELECTED_CHANNEL" | jq -r '.currentCSV')
MAJOR_VER=$(echo "$SELECTED_CHANNEL" | jq -r '.major')
MINOR_VER=$(echo "$SELECTED_CHANNEL" | jq -r '.minor')

echo "Selected channel: ${CHANNEL_NAME}"
echo "Starting CSV: ${STARTING_CSV}"

oc apply -f - <<EOF
apiVersion: v1
kind: Namespace
metadata:
  name: cert-manager-operator
  labels:
    name: cert-manager-operator
EOF

# Create appropriate OperatorGroup based on version
if [[ $MAJOR_VER -ge 1 ]] && [[ $MINOR_VER -ge 15 ]]; then
  echo "Creating AllNamespaces OperatorGroup for v1.15+"
  oc apply -f - <<EOF
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: openshift-cert-manager-operator
  namespace: cert-manager-operator
spec: {}
EOF
else
  echo "Creating SingleNamespace OperatorGroup for pre-v1.15"
  oc apply -f - <<EOF
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: openshift-cert-manager-operator
  namespace: cert-manager-operator
spec:
  targetNamespaces:
  - "cert-manager-operator"
EOF
fi

oc apply -f - <<EOF
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: openshift-cert-manager-operator
  namespace: cert-manager-operator
spec:
  channel: ${CHANNEL_NAME}
  name: openshift-cert-manager-operator
  source: redhat-operators
  sourceNamespace: openshift-marketplace
  installPlanApproval: Automatic
  startingCSV: ${STARTING_CSV}
EOF
