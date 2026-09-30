---
name: kueue-verify
description: Run Kueue's complete verification suite before pushing changes or opening or updating a pull request. Use as final validation to catch CI failures, including stale generated files.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Run Kueue Verification

Before pushing changes or opening or updating a pull request:

1. Run `make verify` from the repository root.
2. Fix every reported issue and rerun `make verify` against the final changes.
3. Report whether the command passed. If it could not run, report the exact reason.
