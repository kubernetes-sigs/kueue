---
name: kueue-generated-files
description: Regenerate and verify Kueue's checked-in generated files to avoid pull-kueue-verify-main failures. Use before pushing, opening, or updating a pull request after changing API types or markers, conversion or defaulting code, mocks, CRD or Helm inputs, feature gates, metrics, CLI documentation, or other generator inputs; or when pull-kueue-verify-main reports a dirty working tree.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Verify Kueue Generated Files

After changing an input to code or documentation generation:

1. Run from a clean candidate commit whenever possible because `make verify` expects a clean tree. Otherwise, record the initial `git status --short` and diff so the changes produced by verification can be distinguished from existing work.
2. Run `make verify` from the repository root. This command regenerates checked-in artifacts before checking that the tree is clean.
3. Inspect `git status --short` and `git diff` even when `make verify` fails. A diff introduced by verification means generated output is missing from the candidate change. Keep every generated change caused by the source change and include it in the pull request.
4. Do not discard all generated changes merely because the diff is large or contains unrelated noise. Separate relevant output from unrelated output first. For API changes, pay particular attention to CRDs, apply configurations, clients, conversions, deep-copy code, and API reference documentation for every affected API version.
5. If generation produces unexpected broad changes, reproduce it from a clean worktree at the same commit and investigate tool or environment differences. Never use the noisy output as a reason to omit a generated file related to the change.
6. Rerun `make verify` from a clean tree at the final candidate commit. Report whether it passed and any exact blocker if it could not run.
