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

set -euo pipefail

version="${1:?missing Kubernetes version}"
envtest="${ENVTEST:-setup-envtest}"
os="${ENVTEST_K8S_OS:-$(go env GOOS)}"
arch="${ENVTEST_K8S_ARCH:-$(go env GOARCH)}"
etcd_version="${ENVTEST_ETCD_VERSION:-3.6.8}"
first_error=""
tmp_dir=""

cleanup() {
  if [[ -n "${first_error}" ]]; then
    rm -f "${first_error}"
  fi
  if [[ -n "${tmp_dir}" ]]; then
    rm -rf "${tmp_dir:?}"
  fi
}
trap cleanup EXIT

sha512() {
  local file="$1"
  if command -v sha512sum >/dev/null 2>&1; then
    sha512sum "${file}" | awk '{print $1}'
  else
    shasum -a 512 "${file}" | awk '{print $1}'
  fi
}

sha256() {
  local file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "${file}" | awk '{print $1}'
  else
    shasum -a 256 "${file}" | awk '{print $1}'
  fi
}

download_kubernetes_binary() {
  local name="$1"
  local target="$2"
  local url="${KUBERNETES_BINARY_BASE_URL:-https://dl.k8s.io/${version_with_v}/bin/${os}/${arch}}/${name}"
  local sha_url="${url}.sha512"
  local expected_hash
  local actual_hash

  echo "downloading ${name} from ${url}" >&2
  curl -fsSL --retry 3 --retry-delay 2 --connect-timeout 20 -o "${target}" "${url}"
  expected_hash="$(curl -fsSL "${sha_url}" | awk '{print $1}')"
  actual_hash="$(sha512 "${target}")"
  if [[ "${actual_hash}" != "${expected_hash}" ]]; then
    echo "sha512 mismatch for ${url}" >&2
    echo "expected: ${expected_hash}" >&2
    echo "actual:   ${actual_hash}" >&2
    exit 1
  fi
  chmod 0555 "${target}"
}

download_etcd() {
  local target="$1"
  local archive_name="etcd-v${etcd_version}-${os}-${arch}.tar.gz"
  local archive="${tmp_dir}/${archive_name}"
  local sums="${tmp_dir}/etcd-SHA256SUMS"
  local release_url="${ETCD_RELEASE_URL:-https://github.com/etcd-io/etcd/releases/download/v${etcd_version}}"
  local expected_hash
  local actual_hash
  local extract_dir="${tmp_dir}/etcd"

  case "${os}" in
    linux) ;;
    *)
      echo "etcd fallback currently supports linux envtest assets only; ${os}/${arch} needs controller-tools envtest assets or KUBEBUILDER_ASSETS" >&2
      exit 1
      ;;
  esac

  echo "downloading etcd ${etcd_version} from ${release_url}/${archive_name}" >&2
  curl -fsSL --retry 3 --retry-delay 2 --connect-timeout 20 -o "${archive}" "${release_url}/${archive_name}"
  curl -fsSL -o "${sums}" "${release_url}/SHA256SUMS"
  expected_hash="$(awk -v archive="${archive_name}" '$2 == archive {print $1}' "${sums}")"
  if [[ -z "${expected_hash}" ]]; then
    echo "unable to find checksum for ${archive_name} in ${release_url}/SHA256SUMS" >&2
    exit 1
  fi
  actual_hash="$(sha256 "${archive}")"
  if [[ "${actual_hash}" != "${expected_hash}" ]]; then
    echo "sha256 mismatch for ${release_url}/${archive_name}" >&2
    echo "expected: ${expected_hash}" >&2
    echo "actual:   ${actual_hash}" >&2
    exit 1
  fi

  mkdir -p "${extract_dir}"
  tar -xzf "${archive}" -C "${extract_dir}" "etcd-v${etcd_version}-${os}-${arch}/etcd"
  cp "${extract_dir}/etcd-v${etcd_version}-${os}-${arch}/etcd" "${target}"
  chmod 0555 "${target}"
}

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/kueue-envtest.XXXXXX")"
first_error="${tmp_dir}/setup-envtest.err"

if assets="$("${envtest}" --os "${os}" --arch "${arch}" use "${version}" -p path 2>"${first_error}")"; then
  printf '%s' "${assets}"
  exit 0
fi

if [[ ! "${version}" =~ ^v?[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  cat "${first_error}" >&2
  exit 1
fi

version_without_v="${version#v}"
version_with_v="v${version_without_v}"
assets_dir="${tmp_dir}/assets"
envtest_archive="${tmp_dir}/envtest-v${version_without_v}-${os}-${arch}.tar.gz"
mkdir -p "${assets_dir}"

echo "setup-envtest assets for Kubernetes ${version_without_v} (${os}/${arch}) were not found in the default index; assembling assets from upstream release binaries" >&2

download_kubernetes_binary kube-apiserver "${assets_dir}/kube-apiserver"
download_kubernetes_binary kubectl "${assets_dir}/kubectl"
download_etcd "${assets_dir}/etcd"

tar -czf "${envtest_archive}" -C "${assets_dir}" kube-apiserver etcd kubectl
"${envtest}" --os "${os}" --arch "${arch}" sideload "${version_without_v}" < "${envtest_archive}" >/dev/null
"${envtest}" --os "${os}" --arch "${arch}" use "${version_without_v}" -p path
