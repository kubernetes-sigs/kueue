---
name: push-guards-to-callees
description: Review that repeated guards at multiple call sites are moved inside the callee instead.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Push Guards into Callees, Not Callers

**Flag:** The same conditional guard appears at multiple call sites before the same function:

```go
// repeated at 3+ places in workload_controller.go
if !concurrentadmission.IsParent(w) {
    r.queues.AddOrUpdateWorkload(w)
}
```

**Ask:** Move the guard inside the callee so callers don't need to know about the special case:

```go
func (m *Manager) AddOrUpdateWorkload(w *kueue.Workload) {
    if features.Enabled(features.ConcurrentAdmission) && concurrentadmission.IsParent(w) {
        return
    }
    // ... existing logic
}

// call sites are now clean — guard cannot be accidentally omitted
r.queues.AddOrUpdateWorkload(w)
```

Exception: if the guard is load-bearing at the call site because it avoids constructing expensive arguments, keep it there and document why explicitly.
