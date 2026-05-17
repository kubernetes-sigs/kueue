---
name: kueue-triage-ci
description: Triage failing CI on a Kueue PR. Use when CI is failing, integration tests fail, verify fails, lint fails, or a user asks "why is CI red" for a PR.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

You are an expert in the Kueue project CI system which uses Prow for test automation. Kueue CI jobs store artifacts in GCS buckets accessible via `gsutil` and `https://storage.googleapis.com/`.

## Input

The argument is a PR number, a PR URL, or empty (defaults to the current branch's PR).

If no argument is provided, find the PR for the current branch:

```bash
gh pr list --head "$(git branch --show-current)" --json number --jq '.[0].number'
```

If a full URL is provided, extract the PR number from it.

## Step 1: Get failing checks

```bash
gh pr checks <PR_NUMBER> 2>&1
```

Filter to only jobs with `fail` status. Ignore `pending`, `pass`, and `skipping` jobs.

For each failing job, extract the Prow URL. Convert Prow URLs to GCS and HTTPS base paths:

- Prow URL: `https://prow.k8s.io/view/gs/<bucket>/<path>/<job-name>/<build-id>`
- GCS base: `gs://<bucket>/<path>/<job-name>/<build-id>`
- HTTPS base: `https://storage.googleapis.com/<bucket>/<path>/<job-name>/<build-id>`

## Step 2: Categorize failures

Group failing jobs into categories:

| Category | Job name pattern | Triage approach |
|---|---|---|
| Verify | `pull-kueue-verify-*` | Fetch build-log.txt, look for lint/fmt/typecheck errors |
| Integration | `pull-kueue-test-integration-*` | Fetch JUnit XML artifacts, parse test failures |
| E2E | `pull-kueue-test-e2e-*` | Fetch JUnit XML artifacts, check for infra issues |
| Unit | `pull-kueue-test-unit-*` | Fetch build-log.txt, look for test failures |
| Other | everything else | Fetch build-log.txt for general analysis |

Triage each category using the appropriate steps below.

## Step 3: Triage verify failures

For `verify-*` jobs, fetch the build log:

```bash
# Use WebFetch to get the build log
# URL: <HTTPS_BASE>/build-log.txt
```

Look for these common Kueue verify targets and their errors:

- **`verify-ci-lint`** (golangci-lint): typecheck errors, gci import ordering, modernize suggestions, unused variables
- **`verify-lint-api`** (API linter): typecheck errors in API types or test files that use API types
- **`verify-fmt-verify`** (formatting): `go fmt` differences, unformatted files

For each error, extract the file path, line number, and error message.

For each error, determine the fix:
- Formatting or import ordering issues: run `make fmt`
- Typecheck errors: read the source file at the reported line and compare against the current function/type signatures in the production code
- Missing generated files: run `make generate && make manifests` and check for diffs
- Lint suggestions (e.g., `modernize`, `revive`): apply the suggested refactor or check `.golangci.yaml` for project-specific suppressions

## Step 4: Triage integration test failures

For `test-integration-*` jobs, use two artifact sources: JUnit XML for test-level failures and `integration.json` for compilation errors.

### 4a: Check JUnit XML for test failures

```bash
gsutil ls "<GCS_BASE>/artifacts/"
```

Fetch the top-level `integration-junit.xml`:

```bash
gsutil cat "<GCS_BASE>/artifacts/integration-junit.xml" | grep -oP 'failures="[^"]*"' | sort | uniq -c | sort -rn
```

If any suites have `failures="N"` where N > 0, extract the failing test names and messages:

```bash
gsutil cat "<GCS_BASE>/artifacts/integration-junit.xml" | python3 -c "
import sys, xml.etree.ElementTree as ET
tree = ET.parse(sys.stdin)
for tc in tree.iter('testcase'):
    for f in tc.iter('failure'):
        print(f'FAIL: {tc.get(\"name\", \"?\")}')
        msg = f.text or f.get('message', '')
        print(msg[:500])
        print('---')
"
```

### 4b: Check integration.json for compilation errors

If JUnit shows no test failures but the job still failed, the failure is likely a compilation error in one of the test suites. Fetch `integration.json`:

```bash
gsutil cat "<GCS_BASE>/artifacts/integration.json" | python3 -c "
import json, sys
data = json.load(sys.stdin)
for suite in data:
    if not suite.get('SuiteSucceeded', True):
        path = suite.get('SuitePath', '?')
        reasons = suite.get('SpecialSuiteFailureReasons', [])
        if reasons:
            print(f'COMPILATION ERROR in {path}:')
            for r in reasons:
                print(r[:500])
            print()
"
```

`SpecialSuiteFailureReasons` contains compilation error messages when a test suite fails to build.

### 4c: Analyze each failure

For each failing test, read the test source code in the local repo:
- Map `/home/prow/go/src/sigs.k8s.io/kueue/` paths to the local repo root
- Read the test file around the failing line to understand the assertion
- Read at least 50 lines before to understand the test setup (BeforeAll, BeforeEach, feature gates)

Look for patterns in the failure to determine the root cause:

- **Compilation errors**: If the error contains `undefined:`, `cannot use`, `does not implement`, or `not enough arguments`, the test code is out of sync with the production code. Compare the failing call site against the current function signature in the source. Look at how other test suites in `test/integration/` call the same function for the correct pattern.
- **Feature gate issues**: If the error mentions `Detected parallel setting of a feature gate`, two Ginkgo `Describe` blocks are racing on the same gate. If a field is unexpectedly nil or zero, check whether the `BeforeAll` enables the required feature gate via `features.SetFeatureGateDuringTest`.
- **Timeout / Eventually failures**: Read the assertion that timed out and check whether the preconditions (admission, scheduling, resource creation) are set up in the test.
- **Stale generated files**: If verify passes locally but CI fails, run `make generate && make manifests` and check for uncommitted diffs in CRD YAMLs, deepcopy files, or config.

## Step 5: Triage e2e test failures

For `test-e2e-*` jobs, follow the same JUnit XML approach as integration tests.

Additionally check for infrastructure issues:
- Kind cluster setup failures (look for `kind create cluster` errors in build-log.txt)
- Timeout waiting for pods to become ready
- Image pull failures

If the failure looks like a flake (intermittent timeout, transient network error), cross-reference with the [kueue-flake-debugger](../kueue-flake-debugger/SKILL.md) skill for deeper analysis including kube-scheduler logs, kubelet logs, and pod placement.

## Step 6: Distinguish PR-specific vs pre-existing failures

For each failure, determine if it was introduced by this PR:

```bash
# Get files changed in this PR
git diff --name-only origin/main...HEAD
```

- If the failing test file or the code it tests is in the PR diff, the failure is likely PR-specific
- If not, search for the same failure on main:
  ```bash
  gh search issues --repo kubernetes-sigs/kueue "<test-name>" --state open
  ```
- Check if the error exists in the main branch by looking at recent CI runs

## Step 7: Report findings

Present a structured summary:

### Failing Jobs

List all failing jobs with their category and Prow link.

### Per-Failure Analysis

For each failure:
1. **Test/Target**: the failing test name or verify target
2. **Error**: the specific error message
3. **Root Cause**: what is wrong and why
4. **PR-specific**: yes/no (was this introduced by this PR?)
5. **Suggested Fix**: concrete code change or command to run

### Pre-existing Issues

Link to any open GitHub issues that match the failures.

### Suggested Next Steps

For each failure, recommend the most direct path to resolution: a specific command to run, a file to edit, or a link to an existing issue if the failure is already tracked.
