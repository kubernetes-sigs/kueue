#!/bin/bash

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

# Performance Threshold Automation Script
# Automates calculation and update of performance test thresholds

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || echo ".")"
CALC_SCRIPT="${PROJECT_ROOT}/hack/testing/calculate_performance_thresholds.py"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

print_info() { echo -e "${BLUE}ℹ${NC} $*"; }
print_success() { echo -e "${GREEN}✓${NC} $*"; }
print_error() { echo -e "${RED}✗${NC} $*" >&2; }

# Parse arguments
COMMAND="${1:-help}"
DRY_RUN="${2:-false}"

case "$COMMAND" in
  collect)
    print_info "Collecting performance test data..."
    read -p "How many times to run tests? (recommended: 5-10) " count
    
    for i in $(seq 1 "$count"); do
      echo ""
      echo "========================================="
      echo "Run $i/$count"
      echo "========================================="
      make run-performance-scheduler
      
      if [ $i -lt $count ]; then
        mkdir -p artifacts/run-performance-scheduler-backup-$i
        cp -r artifacts/run-performance-scheduler/* artifacts/run-performance-scheduler-backup-$i/
        print_success "Backed up run $i"
      fi
    done
    
    print_success "Collected $count performance runs"
    print_info "To calculate thresholds, run: make update-performance-thresholds"
    ;;
    
  calculate)
    print_info "Calculating performance thresholds..."
    
    if [ ! -f "$CALC_SCRIPT" ]; then
      print_error "Python script not found: $CALC_SCRIPT"
      exit 1
    fi
    
    # Install Python dependency if needed
    if ! python3 -c "import yaml" 2>/dev/null; then
      print_info "Installing PyYAML..."
      pip install PyYAML || {
        print_error "Failed to install PyYAML"
        exit 1
      }
    fi
    
    # Run calculation
    if [ "$DRY_RUN" = "true" ]; then
      print_info "Running in DRY RUN mode (preview only)..."
      python3 "$CALC_SCRIPT" \
        --artifacts-dir ./artifacts \
        --dry-run
    else
      print_info "Calculating and updating thresholds..."
      python3 "$CALC_SCRIPT" \
        --artifacts-dir ./artifacts
      
      print_success "Thresholds updated successfully"
      print_info "Review changes: git diff test/performance/scheduler/configs/baseline/rangespec.yaml"
    fi
    ;;
    
  validate)
    print_info "Validating performance test metrics..."
    
    if [ ! -d "./artifacts" ]; then
      print_error "No artifacts directory found"
      exit 1
    fi
    
    if find ./artifacts -name "summary.yaml" | grep -q .; then
      print_success "Found summary.yaml files:"
      find ./artifacts -name "summary.yaml" -exec echo "  - {}" \;
    else
      print_error "No summary.yaml files found in artifacts"
      exit 1
    fi
    
    print_info "To calculate thresholds, run: make update-performance-thresholds"
    ;;
    
  help|*)
    cat << 'EOF'

Performance Threshold Automation

Usage:
  make update-performance-thresholds collect       # Collect test runs
  make update-performance-thresholds calculate     # Calculate & apply thresholds
  make update-performance-thresholds calculate dry # Preview changes (dry-run)
  make update-performance-thresholds validate      # Validate metrics exist
  make update-performance-thresholds help          # Show this help

Examples:
  # Collect 5 test runs
  ./hack/testing/performance-thresholds.sh collect

  # Preview threshold changes
  ./hack/testing/performance-thresholds.sh calculate dry

  # Apply threshold changes
  ./hack/testing/performance-thresholds.sh calculate

  # Validate metrics
  ./hack/testing/performance-thresholds.sh validate

For detailed documentation, see:
  ./hack/testing/README-PERFORMANCE-THRESHOLDS.md

EOF
    ;;
esac
