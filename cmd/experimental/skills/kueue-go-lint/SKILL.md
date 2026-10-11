---
name: kueue-go-lint
description: Run Kueue's Go linter after modifying Go code. Use before committing, pushing, opening or updating a pull request, or handing Go changes back to a user.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Run Kueue Go Lint

After changing any Go file:

1. Run `make ci-lint` from the repository root.
2. Fix every reported issue and rerun `make ci-lint` against the final diff.
3. Report whether the command passed. If it could not run, report the exact reason.
