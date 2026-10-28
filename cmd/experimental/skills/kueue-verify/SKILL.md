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

1. Inspect the complete PR diff against its target branch, including committed,
   staged, unstaged, and non-ignored untracked changes. Choose the generators and
   checks using [local verification guidance](../../../../CONTRIBUTING.md#verification-for-local-changes).
   Targets do not automatically inspect changes or select scopes.
2. Run the relevant generators first, inspect and include their output, and rerun
   until the output is stable. Then run the related checks as separate commands
   so parallel Make execution cannot race generation against readers.
3. Include `make verify-cli` for production CLI changes or changes to its shared
   or platform-specific dependencies, module dependencies, license, or build script.
   Do not infer CLI impact solely from whether `cmd/` changed. Test-only changes
   do not require CLI compilation.
4. Use `make verify` for build-system or generator-tooling changes, when the scope
   is uncertain, or when the developer requests full verification. CI continues
   to run the complete target.
5. Run relevant application tests separately and fix reported issues. Report the
   selected targets and results, including failures or checks that could not run.
   Do not describe scoped checks as the full CI suite; they omit its final
   Git-clean assertion.

Useful explicit scopes are `make verify-go`, `make verify-api`, `make verify-cli`,
`make verify-manifests`, `make verify-helm`, `make verify-docs`, and
`make verify-scripts`. Run `make verify-skills-lint` for changes to skills or agent
instructions. Scopes that generate files require reviewing their resulting diffs.
