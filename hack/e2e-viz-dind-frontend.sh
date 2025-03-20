#!/bin/bash

set -e

# if CI is true and PROW_JOB_ID is set, set NO_COLOR to 1
if [ -n "${CI}" ] && [ -n "${PROW_JOB_ID}" ]; then
  export NO_COLOR=1
  echo "Running in prow: Disabling color to have readable logs: NO_COLOR=${NO_COLOR}"
fi

# Install missing dependencies
apt-get update && apt-get install -y curl make xdg-utils

# Function to clean up background processes
cleanup() {
  echo "Cleaning up kueue-viz processes"
  kill "${FRONTEND_PID}"
}

# Start kueue-viz frontend
cd cmd/experimental/kueue-viz/frontend
npm install
npm start & FRONTEND_PID=$!

# Run Cypress tests for kueue-viz frontend
npx cypress install
npm run cypress:run --headless

# The trap will handle cleanup 
