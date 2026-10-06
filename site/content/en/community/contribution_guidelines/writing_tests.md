---
title: "Writing tests"
linkTitle: "Writing tests"
weight: 24
description: >
  Choose test levels and write reliable, maintainable tests for Kueue.
type: docs
---

This guide builds on [Go Test Comments](https://go.dev/wiki/TestComments),
the [Kubernetes testing overview](https://www.kubernetes.dev/docs/guide/contributing/#testing),
and [Kubernetes E2E best practices](https://www.kubernetes.dev/blog/2023/04/12/e2e-testing-best-practices-reloaded/).
The common guidelines apply to all tests; each level's section adds its own
guidelines and examples.

## Common guidelines

- Choose the lowest test level that can prove the behavior.

- Cover relevant branches, important edge cases, and errors. Test both
  feature-gate states when the gate changes the result. For a bug fix, add a
  case that fails without the fix.

- Build fixture objects and their variants with the wrappers in
  `pkg/util/testing` and `pkg/util/testingjobs`, not with helper functions or
  closures. Declare objects in each test case, or clone a shared base object
  per case. Extend the wrappers if needed.

- Write integration and E2E tests with Ginkgo/Gomega and the shared suite
  setup. Keep independent scenarios in separate `ginkgo.It` blocks; the
  table-driven style applies only to unit tests.

## Unit tests

Use unit tests for decisions or helpers with controlled inputs and dependencies.

### Guidelines

- Prefer testing complete operations, such as `Reconcile` or a scheduling cycle,
  or established logic layers. Test all exported functions, but avoid testing
  unexported helpers directly.

- Use Go's `testing` package, with plain Go checks and `cmp` for structured
  comparisons, instead of assertion libraries.

- Extend a suitable map-based table and reuse its runner, assertions, and
  cleanup. If the runner lacks a setting that a case needs, add it rather than
  writing a separate setup. Start a separate table only when the setup differs
  or the runner would need many case-specific branches.

- Check the error details that matter, for example, the error type
  or its structure. Avoid asserting on error/no error by a boolean assert.

- Use the existing fake clocks for deadlines, and hooks or channels to
  coordinate concurrent operations. Avoid fixed sleeps and repeated attempts to
  win a timing race.

### Examples

- [TAS placement tests][unit-tas] give the assignment code a fixed topology,
  resource usage, and Pod requests, and check whether placement succeeds, needs
  preemption, or cannot fit.

- [Preemption candidate tests][unit-preemption] take admitted Workloads and a
  preemption policy, and check which Workloads are selected for preemption.

- [Workload name tests][unit-names] check the Workload name generated from a
  job's name and identity, including how long names are shortened.

## Integration tests

Use integration tests for Kueue components working together, or for behavior
that needs a real API server (envtest).

### Guidelines

- Let event handlers trigger the work. Avoid explicitly calling `Reconcile` or
  otherwise triggering the code under test.

- Use an `Eventually` block to retry on transient errors, such as update
  conflicts, and fail on any other error.

- Use `Consistently` when a result should stay unchanged. Where possible,
  prefer other checks, such as metrics, because `Consistently` always runs for
  a fixed duration.

- Clean up resources explicitly, reusing the suite's cleanup helpers, so that
  the same test can be rerun in a loop.

### Examples

- A [preemption and admission test][integration-preemption] checks victim
  selection, eviction requests, and admission of the waiting Workload after
  quota is freed. It simulates eviction completion through the API.

- A [TAS node hot swap test][integration-hot-swap] marks a Node as `NotReady`
  and starts deleting its Pod through the API. It then checks that Kueue detects
  the failure and replaces the unhealthy node in the Workload's topology
  assignment.

- A [CEL validation test][cel-rules-verification] checks that the API server
  allows changing Topology levels only when hostname stays the lowest level.

## E2E tests

Use E2E tests when behavior needs real components beyond envtest, such as
kubelets, kube-scheduler, or job controllers.

### Guidelines

- Check compatibility with real cluster components. These tests catch
  regressions when Kubernetes or an operator changes its behavior, for example,
  by creating an extra Job.

- Add E2E tests only when necessary: they use a lot of CI resources and are the
  hardest to debug when they fail or flake.

- Put tests that change shared Kueue configuration in a sequential suite,
  because such changes can disrupt unrelated tests.

### Examples

- A [FailureRecoveryPolicy test][e2e-failure] stops a real kubelet and checks
  foreground deletion, which also relies on `kube-controller-manager`.

- A [Kubernetes Job test][e2e-job] checks that a Job is admitted and finishes
  with the Job controller in `kube-controller-manager` and real kubelets.

- Tests with custom controllers, such as [KubeRay][e2e-rayjob],
  [JobSet][e2e-jobset], or LeaderWorkerSet, run the real operator. For example,
  an admitted RayJob must run to completion with the KubeRay operator. Where
  relevant, they also cover objects the operator creates, such as KubeRay's
  [Redis cleanup Job][e2e-ray-cleanup].

[unit-tas]: https://github.com/kubernetes-sigs/kueue/blob/f1e8ace456bfcc656abf6aa33341c1b3aa99ff2a/pkg/scheduler/flavorassigner/flavorassigner_test.go#L7865-L8212
[unit-preemption]: https://github.com/kubernetes-sigs/kueue/blob/f1e8ace456bfcc656abf6aa33341c1b3aa99ff2a/pkg/scheduler/preemption/config/evaluator_test.go#L780-L980
[unit-names]: https://github.com/kubernetes-sigs/kueue/blob/f1e8ace456bfcc656abf6aa33341c1b3aa99ff2a/pkg/controller/jobframework/workload_names_test.go#L32-L138
[integration-preemption]: https://github.com/kubernetes-sigs/kueue/blob/f1e8ace456bfcc656abf6aa33341c1b3aa99ff2a/test/integration/singlecluster/scheduler/preemption_test.go#L258-L304
[integration-hot-swap]: https://github.com/kubernetes-sigs/kueue/blob/f1e8ace456bfcc656abf6aa33341c1b3aa99ff2a/test/integration/singlecluster/tas/tas_test.go#L2650-L2731
[cel-rules-verification]: https://github.com/kubernetes-sigs/kueue/blob/f1e8ace456bfcc656abf6aa33341c1b3aa99ff2a/test/integration/singlecluster/tas/tas_test.go#L670-L700
[e2e-failure]: https://github.com/kubernetes-sigs/kueue/blob/f1e8ace456bfcc656abf6aa33341c1b3aa99ff2a/test/e2e/sequential/baseline/failure_recovery_policy_test.go#L151-L189
[e2e-rayjob]: https://github.com/kubernetes-sigs/kueue/blob/f1e8ace456bfcc656abf6aa33341c1b3aa99ff2a/test/e2e/singlecluster/extended/kuberay_test.go#L144-L223
[e2e-ray-cleanup]: https://github.com/kubernetes-sigs/kueue/blob/f1e8ace456bfcc656abf6aa33341c1b3aa99ff2a/test/e2e/singlecluster/extended/kuberay_test.go#L888-L997
[e2e-jobset]: https://github.com/kubernetes-sigs/kueue/blob/f1e8ace456bfcc656abf6aa33341c1b3aa99ff2a/test/e2e/singlecluster/extended/jobset_test.go#L75-L101
[e2e-job]: https://github.com/kubernetes-sigs/kueue/blob/f1e8ace456bfcc656abf6aa33341c1b3aa99ff2a/test/e2e/singlecluster/baseline/job_test.go#L214-L229
