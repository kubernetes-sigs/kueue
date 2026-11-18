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

# Hugo v0.170.0 removed LibSass, so toCSS needs the Dart Sass `sass` binary on PATH.
# The npm sass-embedded package is not used because on Linux npm also installs the
# pure JS `sass` package, whose `sass` bin does not support `--embedded`.
# Usage: install-dart-sass.sh <destination-dir>
# The binary is installed as <destination-dir>/sass.

set -euo pipefail

DART_SASS_VERSION="1.108.2"

DEST_DIR="${1:?usage: $0 <destination-dir>}"

if [[ -x "${DEST_DIR}/sass" ]] && [[ "$("${DEST_DIR}/sass" --version)" == "${DART_SASS_VERSION}" ]]; then
  exit 0
fi

case "$(uname -s)-$(uname -m)" in
  Linux-x86_64)
    platform="linux-x64"
    sha256="32d2eb5b83cbc180fce9657ff3c57401a9006c59eccfe0bfdde0aa755a66d42f"
    ;;
  Linux-aarch64 | Linux-arm64)
    platform="linux-arm64"
    sha256="1c7a587c81467e8f7551d71e5d62845b53270033c5cc3d36aa8fa07330a5d2a9"
    ;;
  Darwin-x86_64)
    platform="macos-x64"
    sha256="8822d1f3715d84126a6adfe7053559a64e69687bb6e1b5da0d7f854b0c6ba2fa"
    ;;
  Darwin-arm64)
    platform="macos-arm64"
    sha256="95b1bbb13f0b38ba8aacefae45ef23f11271231d24fb57b1593c9e2adf65481c"
    ;;
  *)
    echo "Unsupported platform $(uname -s)-$(uname -m); install Dart Sass manually: https://sass-lang.com/install/" >&2
    exit 1
    ;;
esac

archive="dart-sass-${DART_SASS_VERSION}-${platform}.tar.gz"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

curl -fsSL --retry 3 -o "${tmp_dir}/${archive}" \
  "https://github.com/sass/dart-sass/releases/download/${DART_SASS_VERSION}/${archive}"

if command -v sha256sum >/dev/null; then
  echo "${sha256}  ${tmp_dir}/${archive}" | sha256sum -c - >/dev/null
else
  echo "${sha256}  ${tmp_dir}/${archive}" | shasum -a 256 -c - >/dev/null
fi

# The archive contains a dart-sass/ directory with the sass launcher and its runtime.
tar -xzf "${tmp_dir}/${archive}" -C "${tmp_dir}"
if [[ -e "${DEST_DIR}" ]] && [[ ! -x "${DEST_DIR}/sass" ]]; then
  echo "${DEST_DIR} exists and is not a Dart Sass installation; refusing to replace it" >&2
  exit 1
fi
rm -rf "${DEST_DIR}"
mkdir -p "$(dirname "${DEST_DIR}")"
mv "${tmp_dir}/dart-sass" "${DEST_DIR}"
