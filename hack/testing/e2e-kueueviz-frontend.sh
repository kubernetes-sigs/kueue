#!/bin/bash

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

set -e

# if CI is true and PROW_JOB_ID is set, set NO_COLOR to 1
if [ -n "${CI}" ] && [ -n "${PROW_JOB_ID}" ]; then
  export NO_COLOR=1
  echo "Running in prow: Disabling color to have readable logs: NO_COLOR=${NO_COLOR}"
fi

# Retry network-dependent install steps to tolerate transient registry/network
# failures (e.g. ECONNRESET) that would otherwise flake the e2e job.
RETRY="${PROJECT_DIR}/hack/testing/retry.sh"

# Install missing dependencies
"${RETRY}" --attempts 5 --delay 5 --stream --exponential -- apt-get update
"${RETRY}" --attempts 5 --delay 5 --stream --exponential -- apt-get install -y curl make xdg-utils

# Function to clean up background processes
cleanup() {
  echo "Cleaning up kueueviz processes"
  kill "${FRONTEND_PID}"
}

# Run frontend unit tests before starting the app
cd "${PROJECT_DIR}/cmd/kueueviz/frontend"
"${RETRY}" --attempts 5 --delay 5 --stream --exponential -- npm install
npm test

# Start kueueviz frontend
npm start & FRONTEND_PID=$!

# Run Cypress tests for kueueviz frontend
cd "${PROJECT_DIR}/test/e2e/kueueviz"
"${RETRY}" --attempts 5 --delay 5 --stream --exponential -- npm install
"${RETRY}" --attempts 5 --delay 5 --stream --exponential -- npx cypress install
npm run cypress:run --headless --config-file cypress.config.js

# The trap will handle cleanup 
