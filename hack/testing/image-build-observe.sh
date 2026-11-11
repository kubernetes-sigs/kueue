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

# Observe the compilation cgroup without sampling processes or changing build flags.
# Counters belong to the visible cgroup and may include concurrent build steps.
set -o nounset
set -o pipefail

component=$1
shift
cgroup_root=${IMAGE_BUILD_CGROUP_ROOT:-/sys/fs/cgroup}
started=$(date +%s)

snapshot() {
    local phase=$1 metric value
    for metric in cpu.max cpu.stat cpuset.cpus.effective memory.max memory.peak \
        cpu/cpu.cfs_quota_us cpu/cpu.cfs_period_us cpu/cpu.stat \
        memory/memory.limit_in_bytes memory/memory.max_usage_in_bytes; do
        if [[ -r "$cgroup_root/$metric" ]]; then
            value=$(tr '\n' ' ' < "$cgroup_root/$metric") || value=unavailable
            printf 'image-build-observe component=%s arch=%s phase=%s metric=%s value=%s\n' \
                "$component" "${TARGETARCH:-host}" "$phase" "$metric" "$value"
        fi
    done
}

printf 'image-build-observe component=%s arch=%s phase=begin gomaxprocs=%s goflags=%s\n' \
    "$component" "${TARGETARCH:-host}" "${GOMAXPROCS:-auto}" "${GOFLAGS:-default}"
snapshot before
status=0
"$@" || status=$?
finished=$(date +%s)
snapshot after
printf 'image-build-observe component=%s arch=%s phase=end duration_seconds=%s status=%s\n' \
    "$component" "${TARGETARCH:-host}" "$((finished - started))" "$status"
exit "$status"
