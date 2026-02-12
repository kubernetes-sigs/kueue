
# AGENTS.md

Guidelines for AI assistants (Claude, Cursor, GitHub Copilot, etc.) when working on this project.

## Project Overview

Kueue is a Kubernetes job queueing system that manages job admission based on resource quotas and priorities. It provides:
- Job queueing with priority-based scheduling (StrictFIFO, BestEffortFIFO)
- Resource management with flavor fungibility, fair sharing, cohorts, and preemption
- Support for multiple job types (Batch Jobs, Kubeflow, Ray, JobSet, Pods, Deployments, etc.)
- Multi-cluster job dispatching (MultiKueue)
- Topology-aware scheduling

**Required Kubernetes version:** 1.29 or newer

## Build and Development Commands

### Building
```bash
# Build the controller binary
make build

# Build kubectl-kueue CLI
make kueuectl
```

### Testing

#### Unit Tests
```bash
# Run all unit tests
make test

# Run unit tests for specific package
GO_TEST_TARGET=./pkg/scheduler make test
```

#### Integration Tests
```bash
# Run all single-cluster integration tests
make test-integration

# Run baseline integration tests (fast, excludes slow/redundant tests)
make test-integration-baseline

# Run extended integration tests (slow/redundant tests only)
make test-integration-extended

# Run MultiKueue integration tests
make test-multikueue-integration

# Run integration tests with custom settings
INTEGRATION_NPROCS=8 INTEGRATION_TARGET=./test/integration/singlecluster/scheduler make test-integration
```

#### E2E Tests
```bash
# Run e2e tests (creates Kind cluster automatically)
make test-e2e

# Run e2e with Helm installation
make test-e2e-helm

# Run MultiKueue e2e tests
make test-multikueue-e2e

# Run Topology-Aware Scheduling (TAS) e2e tests
make test-tas-e2e

# Run e2e with custom Kubernetes version
E2E_K8S_VERSION=1.33 make test-e2e

# Keep Kind cluster running for debugging
E2E_RUN_ONLY_ENV=true make test-e2e

# Use existing Kind cluster
CREATE_KIND_CLUSTER=false KIND_CLUSTER_NAME=my-cluster make test-e2e
```

### Code Generation and Verification
```bash
# Generate all code (deepcopy, clientsets, CRDs, docs)
make generate

# Generate CRD manifests
make manifests

# Update Helm charts
make update-helm

# Verify all code generation is up-to-date
make verify

# Format code
make fmt

# Run linters
make ci-lint

# Fix linting issues
make lint-fix

# Lint API changes
make lint-api
```

### Local Deployment
```bash
# Install CRDs to cluster
make install

# Deploy controller to cluster
make deploy

# Undeploy from cluster
make undeploy

# Run controller locally (not in cluster)
make run
```

### Working with Kind
```bash
# Build and load image into Kind cluster
make kind-image-build

# Build specific component images for Kind
make kind-ray-project-mini-image-build
```

## Architecture

### Core Components

#### 1. **Cache (`pkg/cache/`)**
The cache maintains an in-memory representation of cluster state:
- **scheduler/**: ClusterQueue snapshots used by the scheduler
- **queue/**: LocalQueue and workload queueing logic
- **hierarchy/**: Cohort hierarchy and quota borrowing/lending

The cache is the single source of truth for scheduling decisions and is updated by controllers watching API objects.

#### 2. **Scheduler (`pkg/scheduler/`)**
The scheduler is responsible for:
- Workload admission based on resource availability
- Preemption decisions when resources are insufficient
- Fair sharing across tenants in a cohort
- Flavor assignment (mapping workloads to resource flavors)
- Topology-aware scheduling for optimized pod placement

Key files:
- `scheduler.go`: Main scheduling loop
- `flavorassigner/`: Determines which resource flavors to use
- `preemption/`: Implements preemption logic

#### 3. **Controllers (`pkg/controller/`)**
- **core/**: Core controllers for ClusterQueue, LocalQueue, ResourceFlavor, Workload, AdmissionCheck
- **jobs/**: Job-specific controllers for each supported workload type (job, pod, kubeflow, ray, etc.)
- **jobframework/**: Common framework for integrating new job types
- **admissionchecks/**: Admission check controllers (provisioning, multikueue)
- **tas/**: Topology-aware scheduling controllers
- **workloaddispatcher/**: MultiKueue workload dispatching

#### 4. **Workload (`pkg/workload/`)**
Workload utilities for creating, updating, and managing the Workload API object that represents a job's resource requirements and scheduling state.

#### 5. **Webhooks (`pkg/webhooks/`)**
Admission webhooks for validating and defaulting Kueue API objects.

### API Versions
- **v1beta2**: Current stable API (in `apis/kueue/v1beta2/`)
- **v1beta1**: Previous stable API (in `apis/kueue/v1beta1/`)
- **v1alpha1**: Alpha features (in `apis/kueue/v1alpha1/`)
- Configuration API in `apis/config/v1beta2/`

### Job Integration Framework

Adding support for a new job type involves:
1. Implementing the `GenericJob` interface in `pkg/controller/jobframework/`
2. Creating a new controller in `pkg/controller/jobs/<jobtype>/`
3. Registering the job via blank import in `pkg/controller/jobs/jobs.go`

See existing implementations in `pkg/controller/jobs/` for examples (job, pod, kubeflow, ray, etc.).

## Testing Patterns

### Integration Tests
- Located in `test/integration/singlecluster/` and `test/integration/multikueue/`
- Use envtest for fake Kubernetes API server
- Use Ginkgo/Gomega for test framework
- Common test utilities in `test/util/`

### E2E Tests
- Located in `test/e2e/singlecluster/`, `test/e2e/multikueue/`, `test/e2e/tas/`
- Run against real Kind clusters
- Test realistic workflows and integrations

### Test Utilities
- `test/util/util.go`: General test helpers
- `test/integration/framework/`: Integration test framework for creating test objects

## KEPs (Kueue Enhancement Proposals)

Enhancement proposals are in `keps/` directory. Each KEP is numbered and contains design documents for major features. Reference these when working on related features.

## Important Development Notes

### API Changes
- All API changes require updating generated code: run `make generate manifests update-helm`
- API fields must have proper JSON/YAML tags and kubebuilder validation markers
- Breaking changes require API version bumps and conversion webhooks
- Check `.golangci-kal.yml` for API linting rules

### Testing Requirements
- New features require both unit and integration tests
- E2E tests for user-facing features
- Use table-driven tests where applicable
- Integration tests should be labeled (use `--label-filter` in Ginkgo)

### Feature Gates
Feature gates are defined in `pkg/features/`. Use feature gates for experimental features and check them before accessing alpha/beta functionality.

### Code Organization
- Put shared utilities in `pkg/util/`
- Controller reconciliation logic goes in `pkg/controller/`
- Scheduler logic stays in `pkg/scheduler/`
- Keep cache operations in `pkg/cache/`
- Workload helpers in `pkg/workload/`

### Metrics
Prometheus metrics are defined in `pkg/metrics/`. Add new metrics there when instrumenting new features.

## Common Workflows

### Adding a New API Field
1. Add field to type definition in `apis/kueue/<version>/<resource>_types.go`
2. Add kubebuilder markers for validation and OpenAPI schema
3. Run `make generate manifests update-helm`
4. Add defaulting logic to webhook if needed in `pkg/webhooks/`
5. Add validation logic to webhook if needed
6. Update conversion webhooks if adding to v1beta1
7. Run `make verify` to ensure everything is generated correctly

### Debugging Integration Tests
```bash
# Run specific test
INTEGRATION_TARGET=./test/integration/singlecluster/scheduler GINKGO_ARGS='-focus="should admit workload"' make test-integration

# Increase API server logging
INTEGRATION_API_LOG_LEVEL=5 make test-integration

# Reduce parallelism for debugging
INTEGRATION_NPROCS=1 make test-integration
```

### Working with MultiKueue
MultiKueue tests require multiple clusters. Use the MultiKueue integration and e2e tests to verify multi-cluster functionality.

### Topology-Aware Scheduling
TAS (Topology-Aware Scheduling) tests are separate and require special cluster configuration. Use `make test-tas-e2e` with the TAS-specific Kind cluster config.

## Release Process

Releases are managed via tags and GitHub releases:
- Update `RELEASE_VERSION` and `RELEASE_BRANCH` in Makefile
- Run `make prepare-release-branch` to update version references
- Run `make artifacts` to generate release artifacts

## Documentation

- Website source in `site/` (Hugo-based)
- API reference auto-generated in `site/content/en/docs/reference/`
- Run `make site-server` to preview documentation locally
- Generate kueuectl docs: `make generate-kueuectl-docs`
- Generate API reference: `make generate-apiref`
- Generate Helm docs: `make generate-helm-docs`
