#!/usr/bin/env bash

# Copyright 2025 The Kubernetes Authors.
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

# This shell script is designed to facilitate the build logs from a Kueue CI/CD (GCS) bucket using the gcloud CLI.
# It transforms a given URL copied from the CI into a format compatible with the gcloud storage cp command.
# It ensures that the gcloud CLI is installed and that given URL is in fact pointing to Kueue related GCS bucket.
# Second command line argument is optional and allows to define output directory, which by default is located in build_logs at project direcotry level.

set -o errexit
set -o nounset
set -o pipefail


transform_url() {
    local url="$1"
    local prefix="https://prow.k8s.io/view/gs/"  # Prefix to remove
    local replacement="gs://"  # Replacement prefix

    # Check if the URL starts with the prefix
    if [[ "$url" == "$prefix"* ]]; then
        # Replace the prefix with 'gs://'
        local transformed_url="${url/$prefix/$replacement}"
        echo "$transformed_url"
    else
        echo "Error: URL does not start with the expected prefix: $prefix"
        return 1
    fi
}

if ! command -v gcloud &> /dev/null; then
    echo "Can't find Google Cloud SDK (gcloud CLI) in PATH."
    echo "Please install it by following the instructions at: https://cloud.google.com/sdk/docs/install"
    exit 1
fi

if [[ "$1" != *"kubernetes-sigs_kueue"* ]]; then
    echo "The URL does not point to Kueue build logs bucket."
fi

transformed_url=$(transform_url "$1")
root_dir="$(cd "$(dirname "$0")/.." && pwd)"
output_dir=${2:-"$root_dir/build_logs"}
mkdir -p "$output_dir"
gsutil -m cp -r "$transformed_url" "$output_dir"

