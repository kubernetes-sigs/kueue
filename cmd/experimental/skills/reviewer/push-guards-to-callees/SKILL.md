---
name: push-guards-to-callees
description: Review that repeated guards at multiple call sites are moved inside the callee instead.
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

<!--
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->
