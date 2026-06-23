---
title: "Development"
linkTitle: "Development"
weight: 20
description: >
  Setting up a local development environment for Kueue
---

## Prerequisites

- Go (version declared in `go.mod`)
- Docker (required for shell-lint and npm-depcheck)
- `kubectl`
- GNU sed (macOS: `brew install gnu-sed`)

## Running lint

Run Go linting across all modules:
```shell
make ci-lint
```

Run API-specific linting:
```shell
make lint-api
```

Verify Go code formatting (read-only, no auto-fix):
```shell
make fmt-verify
```

Run shell script linting (requires Docker):
```shell
make shell-lint
```

Validate Helm chart rendering:
```shell
make helm-verify
```

Run Helm unit tests:
```shell
make helm-unit-test
```

Verify frontend npm dependencies (requires Docker):
```shell
make npm-depcheck
```

## Fixing lint

Auto-fix Go lint issues where possible:
```shell
make lint-fix
```

Auto-fix API lint issues:
```shell
make lint-api-fix
```

Auto-format Go code:
```shell
make fmt
```

## Running verify

`make verify` is the single command that CI enforces. It regenerates all checked-in artifacts, runs all linters and checks, then asserts the repo is clean.

```shell
make verify
```

Run individual phases:
```shell
# Phase 1 only: regenerate all checked-in artifacts
make verify-tree-prereqs

# Phase 2 only: run all checks (linters, formatting, Helm, etc.)
make verify-checks
```

Common individual checks:
```shell
make ci-lint           # golangci-lint across all modules
make fmt-verify        # Go formatting check
make helm-verify       # Helm chart rendering
make helm-unit-test    # Helm unit tests
make shell-lint        # shellcheck (requires Docker)
make npm-depcheck      # frontend dependency check (requires Docker)
make gomod-verify      # go.mod / go.sum tidy check
```

Limit parallelism if needed:
```shell
VERIFY_NPROCS=4 make verify
```

{{% alert title="Note" color="primary" %}}
`make verify` requires Docker for the `shell-lint` and `npm-depcheck` steps.
If Docker is unavailable, run `make verify-checks` and skip those two targets, or run `make ci-lint fmt-verify helm-verify gomod-verify` as a lighter substitute.
{{% /alert %}}

## Running tests

For running unit, integration, and E2E tests, see the [Running and debugging tests](../testing) guide.

## Running test coverage

`make test` automatically collects coverage and writes the report to `artifacts/cover.out`. To view it:

```shell
# Open an HTML report in a browser
go tool cover -html=artifacts/cover.out

# Print a per-function summary
go tool cover -func=artifacts/cover.out
```
