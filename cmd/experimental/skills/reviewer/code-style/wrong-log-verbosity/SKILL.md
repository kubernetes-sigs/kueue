---
name: wrong-log-verbosity
description: Flag per-reconcile-cycle log lines emitted at V(2) — recurring reconcile-loop logs must be V(4) or higher.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Wrong Log Verbosity

**Wrong log verbosity** — per-reconcile-cycle log lines emitted at `V(2)`. `V(2)` is
for coarse-grained lifecycle events; recurring reconcile-loop logs must be `V(4)` or
higher.
