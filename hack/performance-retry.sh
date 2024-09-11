#!/usr/bin/env bash

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

if [[ "$#" -lt 1 ]]; then
	echo "Usage: ${0} <num-retries>"
	exit 1
fi


for i in $(seq 1 "$1"); do
	echo "Try ${i}"
	err=0
	make test-performance-scheduler-once || err=$?
	if [ $err -eq 0 ]; then
		break
	else
		if [ "$i" -lt "$1" ]; then
			mv "${ARTIFACTS}/junit.xml" "${ARTIFACTS}/junit-fail-${i}.xml" || echo "Unable to back-up ${ARTIFACTS}/junit.xml"
			mv "${ARTIFACTS}/run-performance-scheduler" "${ARTIFACTS}/run-performance-scheduler-fail-${i}" || echo "Unable to back-up ${ARTIFACTS}/run-performance-scheduler"
		else 
			exit $err
		fi
	fi
done
