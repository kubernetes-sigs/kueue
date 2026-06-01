---
name: inaccurate-comments
description: Flag any comment or docstring that no longer matches the code — these mislead future maintainers who trust them.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Inaccurate Comments

**Inaccurate comments** — any comment or docstring that no longer matches the code.
Example: a comment that says "first batch of Job pods" on a function that now
operates on generic Workloads. Inaccurate comments are worse than no comments because
future maintainers will trust them.
