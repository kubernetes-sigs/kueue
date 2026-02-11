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

if [[ "$#" -lt 2 ]]; then
	echo "Usage: ${0} rev1 rev2 [num-iterations [summary-dir]]
	To be run from kueue's root dir."
	exit 1
fi

git --no-pager diff --exit-code || {
	echo "

The git tree is not clean.
Commit your changes or reset the git tree before running $0"
	exit 1
}

REV_1="$1"
REV_2="$2"
NUM_IT=${3:-5}
SUMMARY_DIR=${4:-$(mktemp -d)}

STATS_FILE="${SUMMARY_DIR}/perf_stats.yaml"
SUMMARY_FILE="${SUMMARY_DIR}/perf_stats.yaml}"

if [ -f "${STATS_FILE}" ]; then
	rm "${STATS_FILE}" 
fi

if [ -f "${SUMMARY_FILE}" ]; then
	rm "${SUMMARY_FILE}" 
fi

echo "
Using:
Stats file: $STATS_FILE
Summary file: $SUMMARY_FILE
"

#$1 - iteration
#$2 - git-rev 
function run_and_store {
	git reset --hard 
	git checkout "${2}" 
	make run-performance-scheduler
	run="${1}" git_rev="$2" yq ' .flat.run = env(run) | .flat.rev = env(git_rev) |
		.flat.maxrss = .maxrss |
		.flat.sysMs = .sysMs |
		.flat.userMs = .userMs |
		.flat.wallMs = .wallMs |
		.flat.mCPU = ((.sysMs + .userMs) * 1000 / .wallMs) |
		.flat | [] + . ' bin/run-performance-scheduler/minimalkueue.stats.yaml >> "${STATS_FILE}"

	run="${1}" git_rev="$2" yq ' .flat.run = env(run) |.flat.rev = env(git_rev) | 
		.flat.largeMsToAdm = .workloadClasses.large.averageTimeToAdmissionMs | 
		.flat.mediumMsToAdm = .workloadClasses.medium.averageTimeToAdmissionMs |
		.flat.smallMsToAdm = .workloadClasses.small.averageTimeToAdmissionMs | 
		.flat.largeEvictions = .workloadClasses.large.totalEvictions | 
		.flat.mediumEvictions = .workloadClasses.medium.totalEvictions | 
		.flat.smallEvictions = .workloadClasses.small.totalEvictions | 
		.flat.resUsage = .clusterQueueClasses.cq.cpuAverageUsage * 100 / .clusterQueueClasses.cq.nominalQuota | 
		.flat |  [] + . ' bin/run-performance-scheduler/summary.yaml >> "$SUMMARY_FILE" 

}

for i in $(seq 1 "$NUM_IT"); do
	echo "
	**********************
	Start loop iteration ${i} "
	run_and_store "$i" "$REV_1"
	run_and_store "$i" "$REV_2"

	echo "
	**********************
	After iteration ${i}"
	echo "Stats (csv):"
 	yq "${STATS_FILE}" -oc
	echo "Summary (csv):"
 	yq "${SUMMARY_FILE}" -oc
done

