---
name: cmp-diff-assertions
description: Review that new unit tests compare a result with the expected value with cmp.Diff instead of ==, != or reflect.DeepEqual.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Compare Results with cmp.Diff

Follow [Unit Test Assertions](../../../../../site/content/en/community/contribution_guidelines/coding_guidelines.md#unit-test-assertions).

**Flag:** A unit-test assertion that the diff adds, and that compares a result
with the expected value with `==`, `!=` or `reflect.DeepEqual` and then calls
`t.Errorf` or `t.Fatalf`, also for strings, numbers and booleans:

```go
got := preemptionMessage(tc.preemptor, tc.reason)
if got != tc.want {
    t.Errorf("preemptionMessage() = %q, want %q", got, tc.want)
}
```

**Ask:** Use `cmp.Diff` and print the diff as `(-want,+got)`:

```go
got := preemptionMessage(tc.preemptor, tc.reason)
if diff := cmp.Diff(tc.want, got); diff != "" {
    t.Errorf("unexpected message (-want,+got):\n%s", diff)
}
```

Use the options from `test/util/behavioral/constants.go` and `cmpopts` when the
case needs them, and `cmpopts.EquateErrors()` for errors.

## Do not flag

- An existing assertion that the diff does not change. Do not ask to convert it
  in the same PR.
- A check that stops the test before the assertions, for example
  `if err != nil { t.Fatalf(...) }` in setup, or a length check before indexing.
- Ginkgo and Gomega assertions, which use `gomega.Equal` or
  `gomega.BeComparableTo`.
