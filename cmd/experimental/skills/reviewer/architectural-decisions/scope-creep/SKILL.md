---
name: scope-creep
description: Flag diffs that bundle a bugfix with an unrelated refactor, or generalize a change to all types when only one type needs it.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Scope Creep

**Scope creep** — diffs that bundle a bugfix with an unrelated refactor, or that
generalize a change to all types when only one type needs it. Prefer the smallest
change that resolves the stated problem and nothing else.
