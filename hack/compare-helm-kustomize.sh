#!/bin/bash -eu

## Comparison between helm and kustomize output.

# This script is meant to check that a given kustomize configuration and helm values
# file output agreeable manifests.
# This is accomplished by generating both single-file manifests, sorting them by apiVersion, kind,
# namespace, and then name, sorting their keys lexigraphically, and then pretty-printing.
# From there, the files should be identical, save for the following caveats:
# * Kustomize output includes the Namesoace object, helm does not, as it is created separately
#   via the --create-namespace flag
# * Helm adds a set of release-wide labels that kustomize does not. These must be removed from both metadata
#   and selectors.
# * Helm adds an annotation to some pods with a configmap checksum to trigger rollout on configuration changes,
#   kustomize does not.
# These quirks are pre-processed out of the manifests before comparing.

kustomize_config=$1
helm_values=$2
shift 2

tmpout=$(mktemp -d)
trap "rm -rf '${tmpout}'" EXIT

set -o pipefail

function manifest_sort {
  bin/yq eval-all -P '
  [.] |
  sort_by(
    .apiVersion,
    .kind,
    .metadata.namespace,
    .metadata.name
  ) |
  .[] |
  sort_keys(..) |
  split_doc
' "$@"
}

function template_kustomize {
  bin/kustomize build "${kustomize_config}" |
    # Helm does not output namespace resource
    bin/yq eval-all '
      ... comments="" |
      select(.kind != "Namespace") |
      del(.metadata.labels["app.kubernetes.io/managed-by"]) |
      split_doc
    ' |
    manifest_sort
}

function template_helm {
  bin/helm template kueue ./charts/kueue/ \
    -f "${helm_values}" \
    -n kueue-system |
    bin/yq eval-all '
      ... comments="" |
      del(.metadata.labels["helm.sh/chart"]) |
      del(.metadata.labels["app.kubernetes.io/managed-by"]) |
      del(.metadata.labels["app.kubernetes.io/version"]) |
      del(.metadata.labels["app.kubernetes.io/instance"]) |
      del(.spec.template.metadata.labels["app.kubernetes.io/instance"]) |
      del(.spec.template.metadata.annotations["charts.kueue.x-k8s.io/config-checksum"]) |
      del(.spec.selector["app.kubernetes.io/instance"]) |
      del(.spec.selector["app.kubernetes.io/name"]) |
      del(.spec.selector.matchLabels["app.kubernetes.io/instance"]) |
      del(.spec.selector.matchLabels["app.kubernetes.io/name"]) |
      split_doc
    ' |
    manifest_sort
}

function resource_id {
  bin/yq eval-all '
    . |
    {
      "apiVersion": .apiVersion,
      "kind": .kind,
      "metadata": {
        "namespace": .metadata.namespace,
        "name": .metadata.name
      }
    } |
    split_doc
  ' "$@"
}

function resource_id_lines {
  bin/yq eval -ojson -r \
    '.apiVersion +"/"+ .kind +":"+ .metadata.namespace +"/"+ .metadata.name' \
    "$@"
}

# Step 1: Output both manifests and pre-process to strip expected differences

template_kustomize >"${tmpout}/kustomize.yaml"
template_helm >"${tmpout}/helm.yaml"

echo '# Comparing apiVersion+kind+namespace+name...'

# Step 2: Extract the apiVersion, kind, namespace, and name from each, confirm that the
# same objects are present, regardless of their contents yet

set +e
diff -U5 \
  <(resource_id "${tmpout}/kustomize.yaml") \
  <(resource_id "${tmpout}/helm.yaml")

rc=$?
set -e
case "$rc" in
0)
  echo '# Same resource set'
  ;;
1)
  echo '# Resource set different between helm and kuztomize, please check above output'
  exit 1
  ;;
2)
  echo '# Comparison failed, please check above for error message'
  exit 1
  ;;
esac

# Step 3: Compare each object to its counterpart

ix=0
diffs=()
while read -r id; do
  set +e
  diff="$(diff -U10 \
    <(bin/yq eval-all "select(document_index == $ix)" "${tmpout}/kustomize.yaml") \
    <(bin/yq eval-all "select(document_index == $ix)" "${tmpout}/helm.yaml"))"
  rc=$?
  set -e

  case "$rc" in
  0)
    # do nothing
    ;;
  1)
    diffs+=("${id}")
    echo "# ${id}"
    echo "${diff}"
    ;;
  2)
    echo "# Comparison failed for ${id}, please check above for error message"
    exit 1
    ;;
  esac
  ((ix++)) || true
done < <(resource_id_lines "${tmpout}/kustomize.yaml")

if [ "${#diffs[@]}" -gt 0 ]; then
  echo '# Differences detected, see above'
  exit 1
fi

echo '# Outputs match within tolerance.'
exit 0
