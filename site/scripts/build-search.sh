#!/usr/bin/env bash
# Copyright The Kubernetes Authors.
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

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SITE_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${SITE_DIR}"

PUBLIC_DIR="public"

if [ ! -d "${PUBLIC_DIR}" ]; then
  echo "Error: ${PUBLIC_DIR} directory does not exist. Run hugo first." >&2
  exit 1
fi

echo "Building search index for main docs and site..."
rm -rf "${PUBLIC_DIR}/pagefind"
npx -y pagefind --site "${PUBLIC_DIR}" \
  --glob "{{docs,zh-cn/docs,community,zh-cn/community,adopters,zh-cn/adopters,examples,zh-cn/examples}/**/*.{html,htm},*.html,zh-cn/*.html}" \
  --output-subdir pagefind

echo "Building search indexes for versioned docs..."
for dir in "${PUBLIC_DIR}"/v0.*; do
  if [ -d "${dir}" ]; then
    ver=$(basename "${dir}")
    echo "Indexing version ${ver}..."
    rm -rf "${PUBLIC_DIR}/${ver}/pagefind"
    npx -y pagefind --site "${PUBLIC_DIR}" \
      --glob "{$ver,zh-cn/$ver}/**/*.{html}" \
      --output-subdir "${ver}/pagefind"
  fi
done

echo "Search indexes built successfully."
