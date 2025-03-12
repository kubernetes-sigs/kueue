#!/bin/bash

set -e

# Install missing dependencies
apt-get update && apt-get install -y curl make xdg-utils

SOURCE_DIR="$(cd "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
# ROOT_DIR="$SOURCE_DIR/.."
# source "${SOURCE_DIR}/e2e-common.sh"


# Function to clean up background processes
cleanup() {
  echo "Cleaning up kueue-viz processes"
  kill $FRONTEND_PID
}

# Start kueue-viz frontend
cd cmd/experimental/kueue-viz/frontend
npm start & FRONTEND_PID=$!

# Run Cypress tests for kueue-viz frontend
npx cypress install
npm run cypress:run --headless

# The trap will handle cleanup 
