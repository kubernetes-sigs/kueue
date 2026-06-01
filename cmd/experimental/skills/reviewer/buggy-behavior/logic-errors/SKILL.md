---
name: logic-errors
description: Flag incorrect conditionals, inverted boolean checks, off-by-one errors, unhandled edge cases, race conditions, or mishandled error returns.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Logic Errors

**Logic errors** — incorrect conditionals, inverted boolean checks, off-by-one errors,
unhandled edge cases (empty slices, nil maps, zero values), race conditions, or
mishandled error returns. Trace each new branch and ask: what input lands here, and
is the outcome correct?
