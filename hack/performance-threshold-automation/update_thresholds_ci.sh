#!/bin/bash
# CI/CD Integration script for performance threshold automation
# 
# Usage: ./update_thresholds_ci.sh [--baseline|--tas] [--dry-run]
#
# This script is designed to be run in CI/CD pipelines to automatically
# update performance thresholds after test runs.

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || echo ".")"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Default values
TEST_TYPE="baseline"
DRY_RUN=false
ARTIFACTS_DIR="${PROJECT_ROOT}/artifacts"
CMD_MARGIN=0.20
UTIL_MARGIN=-0.07
ADM_MARGIN=0.20

# Function to print colored output
print_info() {
    echo -e "${BLUE}ℹ${NC} $*"
}

print_success() {
    echo -e "${GREEN}✓${NC} $*"
}

print_warning() {
    echo -e "${YELLOW}⚠${NC} $*"
}

print_error() {
    echo -e "${RED}✗${NC} $*" >&2
}

# Function to print usage
usage() {
    cat <<EOF
Usage: $0 [OPTIONS]

Options:
    --baseline          Run for baseline performance tests (default)
    --tas              Run for TAS (Topology Aware Scheduling) tests
    --dry-run          Show changes without writing files
    --artifacts-dir    Custom artifacts directory
    --cmd-margin       Command time margin (default: 0.20)
    --util-margin      Utilization margin (default: -0.07)
    --adm-margin       Admission time margin (default: 0.20)
    --help             Show this help message
EOF
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --baseline)
            TEST_TYPE="baseline"
            shift
            ;;
        --tas)
            TEST_TYPE="tas"
            shift
            ;;
        --dry-run)
            DRY_RUN=true
            shift
            ;;
        --artifacts-dir)
            ARTIFACTS_DIR="$2"
            shift 2
            ;;
        --cmd-margin)
            CMD_MARGIN="$2"
            shift 2
            ;;
        --util-margin)
            UTIL_MARGIN="$2"
            shift 2
            ;;
        --adm-margin)
            ADM_MARGIN="$2"
            shift 2
            ;;
        --help)
            usage
            exit 0
            ;;
        *)
            print_error "Unknown option: $1"
            usage
            exit 1
            ;;
    esac
done

# Determine rangespec file based on test type
if [ "$TEST_TYPE" = "tas" ]; then
    RANGESPEC_FILE="${PROJECT_ROOT}/test/performance/scheduler/configs/tas/rangespec.yaml"
    print_info "Running for TAS (Topology Aware Scheduling) tests"
else
    RANGESPEC_FILE="${PROJECT_ROOT}/test/performance/scheduler/configs/baseline/rangespec.yaml"
    print_info "Running for baseline performance tests"
fi

print_info "Project root: ${PROJECT_ROOT}"
print_info "Artifacts directory: ${ARTIFACTS_DIR}"
print_info "Target rangespec: ${RANGESPEC_FILE}"

# Check if Python is available
if ! command -v python3 &> /dev/null; then
    print_error "Python3 not found. Please install Python3 to use this script."
    exit 1
fi

# Check if PyYAML is available
if ! python3 -c "import yaml" 2>/dev/null; then
    print_warning "PyYAML not found. Installing..."
    pip install PyYAML || {
        print_error "Failed to install PyYAML"
        exit 1
    }
fi

# Find the threshold calculation script
CALC_SCRIPT="${SCRIPT_DIR}/calculate_performance_thresholds.py"
if [ ! -f "$CALC_SCRIPT" ]; then
    print_error "Threshold calculation script not found: ${CALC_SCRIPT}"
    exit 1
fi

print_success "Found threshold calculation script"

# Prepare command
CMD="python3 ${CALC_SCRIPT}"
CMD="${CMD} --artifacts-dir ${ARTIFACTS_DIR}"
CMD="${CMD} --output ${RANGESPEC_FILE}"
CMD="${CMD} --cmd-margin ${CMD_MARGIN}"
CMD="${CMD} --util-margin ${UTIL_MARGIN}"
CMD="${CMD} --admission-margin ${ADM_MARGIN}"

if [ "$DRY_RUN" = true ]; then
    CMD="${CMD} --dry-run"
    print_info "DRY RUN MODE - No files will be written"
fi

# Run the calculation
print_info "Running threshold calculation..."
print_info "Command: ${CMD}"
echo ""

if ! eval "$CMD"; then
    print_error "Threshold calculation failed"
    exit 1
fi

echo ""

# Post-run actions (only if not dry-run)
if [ "$DRY_RUN" = false ]; then
    # Check if thresholds changed
    if git diff --quiet "${RANGESPEC_FILE}" 2>/dev/null; then
        print_warning "No changes to rangespec file"
    else
        print_success "Rangespec file updated successfully"
        print_info "Changes:"
        git diff "${RANGESPEC_FILE}" | head -50 || true
        
        # Provide next steps
        print_info ""
        print_info "Next steps to commit changes:"
        echo ""
        echo "  # Review changes"
        echo "  git diff ${RANGESPEC_FILE}"
        echo ""
        echo "  # Stage changes"
        echo "  git add ${RANGESPEC_FILE}"
        echo ""
        echo "  # Commit"
        echo "  git commit -m 'chore: update performance thresholds based on latest runs'"
        echo ""
        echo "  # Push to your branch"
        echo "  git push"
        echo ""
    fi
fi

print_success "Threshold update process completed"