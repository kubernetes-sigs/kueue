---
name: test-fixture-wrappers
description: Review that new tests build fixture objects with the testing wrappers and Clone() instead of callbacks, closures, or helper constructors.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Test Fixtures with Wrappers

Follow [Writing tests](../../../../../site/content/en/community/contribution_guidelines/writing_tests.md).

**Flag:** Test code that the diff adds, and that builds fixture objects or
their variants with something other than the testing wrappers:

- a callback or closure that builds or changes an object, for example a
  `func(*kueue.Workload)` field in a test case, or a `makeWorkload := func(...)`
  closure;
- a helper function in the test file that builds an object;
- a shared base object that the cases change without `Clone()`.

For example:

```go
makeWorkload := func(mutate func(*kueue.Workload)) *kueue.Workload {
    wl := utiltestingapi.MakeWorkload("wl", ns).Queue("lq").Obj()
    mutate(wl)
    return wl
}

testCases := map[string]struct {
    workload *kueue.Workload
}{
    "inactive workload": {
        workload: makeWorkload(func(wl *kueue.Workload) { wl.Spec.Active = new(false) }),
    },
}
```

**Ask:** Build each object with the wrapper chain in its test case:

```go
testCases := map[string]struct {
    workload *kueue.Workload
}{
    "inactive workload": {
        workload: utiltestingapi.MakeWorkload("wl", ns).Queue("lq").Active(false).Obj(),
    },
}
```

When the cases share a long chain, declare a base wrapper and call `Clone()`
with the chain methods in each case:

```go
baseWl := utiltestingapi.MakeWorkload("wl", ns).
    Queue("lq").
    Priority(100).
    Request(corev1.ResourceCPU, "1").
    Request(corev1.ResourceMemory, "1Gi")

testCases := map[string]struct {
    workload *kueue.Workload
}{
    "inactive workload": {
        workload: baseWl.Clone().Active(false).Obj(),
    },
}
```

Do not ask for a base wrapper when the chain is short; a short chain repeated
in each case is easier to read.

When a wrapper lacks a chain method that the test needs, ask to add it to the
wrapper in `pkg/util/testing` or `pkg/util/testingjobs`.

## Do not flag

- Objects declared directly in each test case.
- A callback or helper that exists before the diff and that the diff only
  calls. Do not ask to replace it in the same PR.
- Function literals that are not fixtures, such as client interceptor funcs
  and sort or `cmp` functions.
