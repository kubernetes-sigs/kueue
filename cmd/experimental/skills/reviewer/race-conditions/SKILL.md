---
name: race-conditions
description: Review concurrent Go code for races, preferring fixes at the mutation site over adding snapshot fields.
---

# Skill: Race Condition Review — Fix the Mutation Site, Not the Read Site

Use this when reviewing concurrent Go code that adds a snapshot or mirror field to protect
a reader from a concurrent writer.

## Pattern to recognize

A secondary field is added so readers get a safe copy of data that a writer might mutate:

```go
type inflightWorkload struct {
    info      *workload.Info
    resources map[corev1.ResourceName]int64  // snapshot of info.TotalRequests
}
```

## Why to push back

It puts complexity at the read site and creates a new invariant: both fields must be set
and cleared in sync. Ask first: can we just stop the mutation from happening?

If the writer was mutating shared data through an alias, cloning at the mutation site is
usually simpler and leaves no invariant to maintain:

```go
// Before: alias → race
requests = a.wl.TotalRequests

// After: clone → no race, no snapshot field needed
for i, ps := range a.wl.TotalRequests {
    requests[i] = ps
    requests[i].Requests = maps.Clone(ps.Requests)
}
```

## When the snapshot IS correct

- The clone would be expensive and the read path is hot.
- The writer must mutate the original by contract.
- Multiple callers can mutate the data concurrently.

In those cases, wrap the paired fields in a named struct so the invariant is enforced by
the type system, not convention.

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
