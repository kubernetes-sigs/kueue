#!/usr/bin/env bash

# Copyright 2026 The Kubernetes Authors.
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

# Tests hack/kustomization-set-image.sh. The expected files are the output of
# `kustomize edit set image` (kustomize v5.8.3) for the same input.

set -o errexit
set -o nounset
set -o pipefail

PROJECT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
export YQ="${YQ:-$PROJECT_DIR/bin/yq}"
SET_IMAGE="$PROJECT_DIR/hack/kustomization-set-image.sh"
TEST_DIR=$(mktemp -d)
trap 'chmod -R u+w "$TEST_DIR" && rm -r "$TEST_DIR"' EXIT

header='resources:
- deployment.yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
images:'
backend='- name: backend
  newName: example.com/kueue/backend
  newTag: main'
frontend='- name: frontend
  newName: example.com/kueue/frontend
  newTag: main'
controller='- name: controller
  newName: example.com/kueue/controller
  newTag: v1'
base="$header
$backend
$frontend"

case_number=0

# Usage: expect_set_image NAME INPUT IMAGE WANT
expect_set_image() {
  local name=$1 input=$2 image=$3 want=$4
  case_number=$((case_number + 1))
  local dir="$TEST_DIR/$case_number"
  mkdir "$dir"
  printf '%s\n' "$input" > "$dir/kustomization.yaml"

  "$SET_IMAGE" "$dir" "$image"

  local got
  got=$(cat "$dir/kustomization.yaml")
  if [[ "$got" != "$want" ]]; then
    printf '%s: want:\n%s\ngot:\n%s\n' "$name" "$want" "$got" >&2
    exit 1
  fi
}

# Usage: expect_set_image_fails NAME IMAGE
expect_set_image_fails() {
  local name=$1 image=$2
  case_number=$((case_number + 1))
  local dir="$TEST_DIR/$case_number"
  mkdir "$dir"
  printf '%s\n' "$base" > "$dir/kustomization.yaml"

  if "$SET_IMAGE" "$dir" "$image" 2>/dev/null; then
    echo "$name: want an error, got none" >&2
    exit 1
  fi
  if [[ "$(cat "$dir/kustomization.yaml")" != "$base" ]]; then
    echo "$name: the file changed" >&2
    exit 1
  fi
}

expect_set_image "updates an existing image" "$base" backend=registry.example.com/backend:v1.2.3 "$header
- name: backend
  newName: registry.example.com/backend
  newTag: v1.2.3
$frontend"

expect_set_image "adds an image in sorted order" "$base" controller=example.com/kueue/controller:v1 "$header
$backend
$controller
$frontend"

expect_set_image "adds an image first" "$base" aaa=example.com/aaa:v1 "$header
- name: aaa
  newName: example.com/aaa
  newTag: v1
$backend
$frontend"

expect_set_image "adds an image last" "$base" zzz=example.com/zzz:v1 "$header
$backend
$frontend
- name: zzz
  newName: example.com/zzz
  newTag: v1"

expect_set_image "accepts a registry with a port" "$base" frontend=localhost:5000/frontend:dev "$header
$backend
- name: frontend
  newName: localhost:5000/frontend
  newTag: dev"

expect_set_image "keeps the file when the image is already set" "$base" backend=example.com/kueue/backend:main "$base"

expect_set_image "adds the images list" 'apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- deployment.yaml' controller=example.com/kueue/controller:v1 'apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- deployment.yaml
images:
- name: controller
  newName: example.com/kueue/controller
  newTag: v1'

expect_set_image "replaces a digest with the tag" 'apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
images:
- digest: sha256:0000000000000000000000000000000000000000000000000000000000000000
  name: controller
  newName: example.com/kueue/controller' controller=example.com/kueue/controller:v1 "apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
images:
$controller"

digest=sha256:1111111111111111111111111111111111111111111111111111111111111111

expect_set_image "sets a digest and removes the tag" "$base" "backend=example.com/kueue/backend@$digest" "$header
- digest: $digest
  name: backend
  newName: example.com/kueue/backend
$frontend"

expect_set_image "sets a tag and a digest" "$base" "backend=localhost:5000/backend:v2@$digest" "$header
- digest: $digest
  name: backend
  newName: localhost:5000/backend
  newTag: v2
$frontend"

expect_set_image "adds an image with a digest in sorted order" "$base" "controller=example.com/kueue/controller@$digest" "$header
$backend
- digest: $digest
  name: controller
  newName: example.com/kueue/controller
$frontend"

# Like kustomize, only the file needs to be writable, not its directory. The
# appwrapper manifests are copied from the read-only Go module cache.
case_number=$((case_number + 1))
dir="$TEST_DIR/$case_number"
mkdir "$dir"
printf '%s\n' "$base" > "$dir/kustomization.yaml"
chmod a-w "$dir"
"$SET_IMAGE" "$dir" backend=registry.example.com/backend:v1.2.3 || {
  echo "fails when the directory is read-only" >&2
  exit 1
}
if ! grep -q 'newTag: v1.2.3' "$dir/kustomization.yaml"; then
  echo "did not set the image when the directory is read-only" >&2
  exit 1
fi

expect_set_image_fails "rejects an image without a tag or digest" backend=example.com/kueue/backend
expect_set_image_fails "rejects an empty digest" backend=example.com/kueue/backend@
expect_set_image_fails "rejects an argument without NAME=" example.com/kueue/backend:v1

# Setting the images that the repository's kustomization files already have
# must leave them unchanged, or make verify fails after prepare-release-branch.
for dir in config/components/manager config/components/kueueviz \
  cmd/experimental/kueue-populator/config cmd/experimental/kueue-priority-booster/config; do
  case_number=$((case_number + 1))
  copy="$TEST_DIR/$case_number"
  mkdir "$copy"
  # make verify runs prepare-manifests at the same time, which can rewrite the
  # file, so compare against a snapshot instead of the file in the repository.
  cp "$PROJECT_DIR/$dir/kustomization.yaml" "$copy/snapshot.yaml"
  cp "$copy/snapshot.yaml" "$copy/kustomization.yaml"
  while IFS=$'\t' read -r name new_name new_tag; do
    "$SET_IMAGE" "$copy" "$name=$new_name:$new_tag"
  done < <("$YQ" '.images[] | [.name, .newName, .newTag] | @tsv' "$copy/kustomization.yaml")
  if ! cmp -s "$copy/snapshot.yaml" "$copy/kustomization.yaml"; then
    echo "$dir/kustomization.yaml: setting its current images changed the file" >&2
    diff "$copy/snapshot.yaml" "$copy/kustomization.yaml" >&2 || true
    exit 1
  fi
done

echo "kustomization-set-image.sh tests passed"
