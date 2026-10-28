---
name: kueue-verify
description: Select and run Kueue verification for the changed inputs before pushing or opening or updating a pull request, including relevant generators and CLI dependencies.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Run Kueue Verification

Verification does not replace running the relevant unit, integration, or E2E tests;
see [Writing tests](../../../../site/content/en/community/contribution_guidelines/writing_tests.md).

Before pushing changes or opening or updating a pull request:

1. Confirm the PR's target branch. Run `make verify-plan VERIFY_BASE_REF=<target-ref>`
   to inspect the selected generators and checks. For a main-branch PR, use
   `upstream/main`; use the appropriate release ref for a release-branch PR.
2. Run `make verify-changed VERIFY_BASE_REF=<target-ref>` against the final changes.
   It includes committed changes since the merge base, staged and unstaged changes,
   and non-ignored untracked files. It checks CLI dependencies on all configured
   platforms, not just changes under `cmd/`. Unknown inputs, dependency manifests,
   and build-system changes select the full verification checks.
3. Fix reported issues. If generators update files, inspect and include those
   outputs, then rerun the selected verification. Existing uncommitted edits are
   allowed, but generation must not change the output files during the final run.
4. Run any additional checks required by the change. The selector does not run the
   application unit, integration, or cluster E2E suites. Use `make verify` when the
   developer requests full verification; CI continues to run this complete target.
5. Report the base ref, selected targets, and results, including failures or checks
   that could not run. Do not describe a scoped run as the full CI suite.

Useful explicit scopes are `make verify-go`, `make verify-api`, `make verify-cli`,
`make verify-manifests`, `make verify-helm`, and `make verify-scripts`. API and docs
scopes can regenerate files, so inspect their diffs. Prefer `verify-changed` when
multiple scopes apply; it runs generators before checks to avoid codegen races.
