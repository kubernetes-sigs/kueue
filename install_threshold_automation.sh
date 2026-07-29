#!/bin/bash
# Automated installation script for Performance Threshold Automation
#
# This script sets up the entire performance threshold automation feature
# in one command.
#
# Usage: bash install_threshold_automation.sh

set -o errexit
set -o nounset
set -o pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Get repository root
REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
AUTOMATION_DIR="${REPO_ROOT}/hack/performance-threshold-automation"

print_header() {
    echo ""
    echo -e "${BLUE}╔════════════════════════════════════════════════════════════╗${NC}"
    echo -e "${BLUE}║${NC} $1"
    echo -e "${BLUE}╚════════════════════════════════════════════════════════════╝${NC}"
    echo ""
}

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

# Check requirements
check_requirements() {
    print_header "Checking Requirements"
    
    local missing=0
    
    # Check Python
    if ! command -v python3 &> /dev/null; then
        print_error "Python 3 not found. Please install Python 3."
        missing=$((missing + 1))
    else
        print_success "Python 3 found: $(python3 --version)"
    fi
    
    # Check Git
    if ! command -v git &> /dev/null; then
        print_error "Git not found. Please install Git."
        missing=$((missing + 1))
    else
        print_success "Git found: $(git --version | head -1)"
    fi
    
    # Check Bash
    if ! command -v bash &> /dev/null; then
        print_error "Bash not found."
        missing=$((missing + 1))
    else
        print_success "Bash found"
    fi
    
    if [ $missing -gt 0 ]; then
        print_error "Please install missing dependencies and try again."
        exit 1
    fi
}

# Check PyYAML
check_pyyaml() {
    print_header "Checking Python Dependencies"
    
    if python3 -c "import yaml" 2>/dev/null; then
        print_success "PyYAML is installed"
    else
        print_warning "PyYAML not found. Installing..."
        if pip install PyYAML; then
            print_success "PyYAML installed successfully"
        else
            print_error "Failed to install PyYAML. Try: pip install PyYAML"
            return 1
        fi
    fi
}

# Create directory structure
setup_directories() {
    print_header "Setting Up Directories"
    
    if [ -d "${AUTOMATION_DIR}" ]; then
        print_warning "Directory already exists: ${AUTOMATION_DIR}"
    else
        mkdir -p "${AUTOMATION_DIR}"
        print_success "Created directory: ${AUTOMATION_DIR}"
    fi
    
    mkdir -p "${REPO_ROOT}/.github/workflows"
    print_success "Ensured .github/workflows exists"
}

# Copy main script
copy_main_script() {
    print_header "Installing Main Script"
    
    local script_name="calculate_performance_thresholds.py"
    local source_file="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/${script_name}"
    local dest_file="${AUTOMATION_DIR}/${script_name}"
    
    if [ ! -f "${source_file}" ]; then
        print_error "Source file not found: ${source_file}"
        print_info "Make sure you're running this script from the directory containing the installation files."
        return 1
    fi
    
    cp "${source_file}" "${dest_file}"
    chmod +x "${dest_file}"
    print_success "Installed: ${dest_file}"
}

# Copy shell script
copy_shell_script() {
    print_header "Installing Shell Helper Script"
    
    local script_name="update_thresholds_ci.sh"
    local source_file="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/${script_name}"
    local dest_file="${AUTOMATION_DIR}/${script_name}"
    
    if [ ! -f "${source_file}" ]; then
        print_warning "Source file not found: ${source_file}"
        print_info "Skipping shell script installation"
        return 0
    fi
    
    cp "${source_file}" "${dest_file}"
    chmod +x "${dest_file}"
    print_success "Installed: ${dest_file}"
}

# Copy documentation
copy_documentation() {
    print_header "Installing Documentation"
    
    local source_file="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/THRESHOLD_AUTOMATION_README.md"
    local dest_file="${AUTOMATION_DIR}/README.md"
    
    if [ ! -f "${source_file}" ]; then
        print_warning "Documentation not found, skipping: ${source_file}"
        return 0
    fi
    
    cp "${source_file}" "${dest_file}"
    print_success "Installed: ${dest_file}"
}

# Copy Makefile targets
copy_makefile_targets() {
    print_header "Integrating Makefile Targets"
    
    local source_file="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/Makefile.perf-targets"
    local makefile="${REPO_ROOT}/Makefile"
    
    if [ ! -f "${source_file}" ]; then
        print_warning "Makefile targets not found, skipping: ${source_file}"
        return 0
    fi
    
    # Check if targets already added
    if grep -q "update-performance-thresholds" "${makefile}"; then
        print_warning "Makefile targets already exist, skipping"
        return 0
    fi
    
    # Append targets to Makefile
    echo "" >> "${makefile}"
    echo "# Performance Threshold Automation Targets" >> "${makefile}"
    cat "${source_file}" >> "${makefile}"
    print_success "Added targets to: ${makefile}"
}

# Setup GitHub Actions workflow
copy_workflow() {
    print_header "Setting Up GitHub Actions Workflow"
    
    local source_file="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/performance-threshold-update.yml"
    local dest_file="${REPO_ROOT}/.github/workflows/performance-threshold-update.yml"
    
    if [ ! -f "${source_file}" ]; then
        print_warning "Workflow file not found, skipping: ${source_file}"
        return 0
    fi
    
    if [ -f "${dest_file}" ]; then
        print_warning "Workflow already exists: ${dest_file}"
        return 0
    fi
    
    cp "${source_file}" "${dest_file}"
    print_success "Installed GitHub Actions workflow: ${dest_file}"
}

# Verify installation
verify_installation() {
    print_header "Verifying Installation"
    
    local errors=0
    
    # Check Python script
    if [ -f "${AUTOMATION_DIR}/calculate_performance_thresholds.py" ]; then
        print_success "Main script found"
    else
        print_error "Main script not found"
        errors=$((errors + 1))
    fi
    
    # Check Makefile
    if grep -q "update-performance-thresholds" "${REPO_ROOT}/Makefile"; then
        print_success "Makefile targets added"
    else
        print_error "Makefile targets not found"
        errors=$((errors + 1))
    fi
    
    # Check PyYAML
    if python3 -c "import yaml" 2>/dev/null; then
        print_success "PyYAML available"
    else
        print_warning "PyYAML not available (will try to install on first use)"
    fi
    
    if [ $errors -gt 0 ]; then
        return 1
    fi
}

# Test installation
test_installation() {
    print_header "Testing Installation"
    
    # Test help
    if make -C "${REPO_ROOT}" perf-threshold-help &> /dev/null; then
        print_success "Make targets working"
    else
        print_warning "Could not verify make targets (may need to source shell)"
    fi
    
    # Test Python script
    if python3 "${AUTOMATION_DIR}/calculate_performance_thresholds.py" --help &> /dev/null; then
        print_success "Python script working"
    else
        print_error "Python script not working"
        return 1
    fi
}

# Print summary
print_summary() {
    print_header "Installation Complete!"
    
    echo -e "${GREEN}✓ Performance Threshold Automation has been installed!${NC}"
    echo ""
    echo "Next steps:"
    echo ""
    echo "1. Verify installation:"
    echo "   ${BLUE}make -C ${REPO_ROOT} perf-threshold-help${NC}"
    echo ""
    echo "2. Run a performance test:"
    echo "   ${BLUE}make -C ${REPO_ROOT} run-performance-scheduler${NC}"
    echo ""
    echo "3. Preview threshold updates (dry-run):"
    echo "   ${BLUE}make -C ${REPO_ROOT} update-performance-thresholds-dry-run${NC}"
    echo ""
    echo "4. Read documentation:"
    echo "   ${BLUE}cat ${AUTOMATION_DIR}/README.md${NC}"
    echo ""
    echo "For detailed setup instructions, see:"
    echo "   ${BLUE}SETUP_GUIDE.md${NC}"
    echo ""
}

# Main execution
main() {
    print_header "Performance Threshold Automation Setup"
    
    echo "This script will:"
    echo "  1. Check system requirements"
    echo "  2. Install Python dependencies"
    echo "  3. Set up automation scripts"
    echo "  4. Integrate with Makefile"
    echo "  5. Setup GitHub Actions (optional)"
    echo ""
    
    read -p "Continue with installation? (y/n) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        print_info "Installation cancelled"
        exit 0
    fi
    
    # Run installation steps
    check_requirements || exit 1
    check_pyyaml || print_warning "Continuing despite PyYAML issue"
    setup_directories || exit 1
    copy_main_script || exit 1
    copy_shell_script || true
    copy_documentation || true
    copy_makefile_targets || exit 1
    copy_workflow || true
    
    # Verify and test
    verify_installation || exit 1
    test_installation || true
    
    # Print summary
    print_summary
    
    print_success "Setup script completed!"
}

# Run main
main "$@"