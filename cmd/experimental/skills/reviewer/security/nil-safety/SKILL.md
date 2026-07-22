---
name: nil-safety
description: Flag unguarded dereferences of optional pointer fields reachable from a malformed CR, and goroutines without recover (CWE-476).
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Nil-Safety and Crash Resistance (CWE-476)

- Unguarded dereferences of optional pointer fields reachable from a malformed CR
  (`Workload.Status.Admission`, `ClusterQueue.Spec.Cohort`, optional sub-structs, slice
  elements). A panic in scheduler cache construction or webhook handling wedges the
  scheduling loop — treat as **high severity**.
- Goroutines launched outside the controller-runtime workqueue without a deferred
  `recover()` or a documented panic policy.
