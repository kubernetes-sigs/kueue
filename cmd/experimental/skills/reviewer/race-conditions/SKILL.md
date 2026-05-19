---
name: race-conditions
description: Review concurrent Go code for races, preferring fixes at the mutation site over adding snapshot fields.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
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
