---
name: inline-test-steps
description: Review that new tests write their steps and assertions inline instead of sharing them through local closures or callbacks.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Inline Test Steps

Follow [Writing tests](../../../../../site/content/en/community/contribution_guidelines/writing_tests.md).
Tests follow the DAMP principle (Descriptive And Meaningful Phrases): each spec
or case shows its own steps, even when that repeats code.

**Flag:** Test code that the diff adds, and that moves test steps or assertions
out of the specs into a local closure, a callback, or a test-only helper in the
same file. The steps include creating objects, running the action under test,
and `Eventually` or `Consistently` checks. For example:

```go
ginkgo.When("Stopping a LocalQueue", func() {
    expectStopPolicy := func(lq *kueue.LocalQueue, want kueue.StopPolicy) {
        ginkgo.GinkgoHelper()
        gomega.Eventually(func(g gomega.Gomega) {
            g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), lq)).To(gomega.Succeed())
            g.Expect(ptr.Deref(lq.Spec.StopPolicy, kueue.None)).Should(gomega.Equal(want))
        }, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
    }

    ginkgo.It("Should stop a LocalQueue", func() {
        lq := utiltestingapi.MakeLocalQueue("lq", ns.Name).Obj()
        behavioral.MustCreate(ctx, k8sClient, lq)
        expectStopPolicy(lq, kueue.None)
        executeKueuectl(cfg, []string{"stop", "localqueue", lq.Name, "--namespace", ns.Name})
        expectStopPolicy(lq, kueue.HoldAndDrain)
    })
})
```

This includes closures that create the queues and the job for a spec, `func`
fields in test cases, and callbacks passed to a shared runner.

**Ask:** Drop the closure and write its steps directly in each spec or case:

```go
ginkgo.It("Should stop a LocalQueue", func() {
    lq := utiltestingapi.MakeLocalQueue("lq", ns.Name).Obj()
    behavioral.MustCreate(ctx, k8sClient, lq)
    gomega.Eventually(func(g gomega.Gomega) {
        g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), lq)).To(gomega.Succeed())
        g.Expect(ptr.Deref(lq.Spec.StopPolicy, kueue.None)).Should(gomega.Equal(kueue.None))
    }, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

    executeKueuectl(cfg, []string{"stop", "localqueue", lq.Name, "--namespace", ns.Name})
    gomega.Eventually(func(g gomega.Gomega) {
        g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), lq)).To(gomega.Succeed())
        g.Expect(ptr.Deref(lq.Spec.StopPolicy, kueue.None)).Should(gomega.Equal(kueue.HoldAndDrain))
    }, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
})
```

A reader sees what each spec does without jumping to the closure, and a change
to one spec does not change the others. Build objects with the wrappers in
`pkg/util/testing` and `pkg/util/testingjobs`. When a step is generic and is
needed in many test files, ask to add it to `test/util/behavioral` instead.

This skill takes precedence over [extract-helpers](../extract-helpers/SKILL.md)
for test code.

## Do not flag

- Reuse of existing shared helpers, such as `behavioral.MustCreate`, the
  `test/util/behavioral` expectations, and the suite cleanup helpers.
- Function literals that the framework or an API requires, such as
  `ginkgo.It`, `ginkgo.By`, `BeforeEach`, `DeferCleanup`,
  `Eventually(func(g gomega.Gomega) {...})`, client interceptor funcs, and
  sort or `cmp` functions.
- The runner of a unit-test table, either the subtest body or a function in
  the same file that runs each case. Each case still declares its own inputs
  and expectations.
- A closure or helper that exists before the diff and that the diff only
  calls. Do not ask to inline it in the same PR.
