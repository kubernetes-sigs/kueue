---
name: misplaced-logic
description: Flag code put somewhere a reader would not expect to find it — cleanup scattered across callers, validation deep in business logic.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Misplaced Logic

**Misplaced logic** — code put somewhere a reader would not expect to find it.
Cleanup belongs inside the `Delete` path, not scattered across callers; validation
belongs at the trust boundary, not deep in business logic.
