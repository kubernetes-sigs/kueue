---
name: map-table-tests
description: Review that new table-driven unit tests use a map keyed by the case name instead of a slice of cases.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Map-Based Table-Driven Tests

Follow [Table-Driven Unit Tests](../../../../../site/content/en/community/contribution_guidelines/coding_guidelines.md#table-driven-unit-tests).

**Flag:** A table-driven test or benchmark that the diff adds, and that ranges
over a slice of cases and names each subtest with a field of the case, for
example:

```go
testCases := []struct {
    name    string
    wantErr error
}{...}

for _, tc := range testCases {
    t.Run(tc.name, func(t *testing.T) {...})
}
```

This includes benchmarks (`b.Run`), any field of the case (`tc.qty`), and
names built with `fmt.Sprintf(..., tc.nodes, tc.pods)`.

**Ask:** Use a map keyed by the case name, and drop the `name` field if the
table has one:

```go
testCases := map[string]struct {
    wantErr error
}{
    "descriptive case name": {...},
}

for name, tc := range testCases {
    t.Run(name, func(t *testing.T) {...})
}
```

A map rejects duplicate case names at compile time, and Go randomizes its
iteration order, which exposes cases that depend on each other.

If a case depends on state left by an earlier case, also ask to make each case
independent: give each case its own setup and fields that describe its starting
state, instead of keeping a slice.

## Do not flag

- An existing slice-based table, even when the diff adds, removes, or changes
  its cases. Do not ask to convert it in the same PR.
- A slice of inputs that are not named subtests, for example values passed to
  one assertion.
- Ginkgo `DescribeTable` entries.
