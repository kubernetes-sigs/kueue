---
name: cmp-diff-assertions
description: Review that new or changed unit tests compare a result with the expected value with cmp.Diff instead of ==, != or reflect.DeepEqual.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Compare Results with cmp.Diff

Follow [Unit Test Assertions](../../../../../site/content/en/community/contribution_guidelines/coding_guidelines.md#unit-test-assertions).

**Flag:** A unit-test assertion that compares a result with the expected value
with `==`, `!=` or `reflect.DeepEqual` and then calls `t.Error`, `t.Errorf`,
`t.Fatal` or `t.Fatalf`, also for strings and numbers, in a test function that
the diff adds or changes. A change includes new or changed cases in the
function's table, even when its runner stays the same:

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

- An assertion in a test function that the diff does not change. Do not ask to
  convert it in the same PR.
- A check of something that `cmp.Diff` ignores, for example `if got != want`
  on pointers, where the test verifies that it got the same object and not an
  equal one, or the location of a `time.Time`, which `cmp.Diff` compares with
  `Equal`.
- A boolean result compared directly with the expected value, for example
  `if got != tc.want` where both are `bool`. A diff adds nothing there.
- A check that stops the test before the assertions, for example
  `if err != nil { t.Fatalf(...) }` in setup, or a length check before indexing.
- Ginkgo and Gomega assertions, which use `gomega.Equal` or
  `gomega.BeComparableTo`.
