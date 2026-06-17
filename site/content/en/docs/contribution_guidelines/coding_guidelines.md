---
title: "Coding Guidelines"
linkTitle: "Coding Guidelines"
weight: 15
description: >
  Coding conventions and patterns for Kueue product and test code
---

These guidelines are derived from the existing Kueue codebase. They capture the
conventions that make the code consistent, maintainable, and reusable. Follow
these when writing new code or reviewing pull requests.

Kueue is a Kubernetes project and follows the upstream Kubernetes conventions.
Be familiar with these core guidelines:

- [Kubernetes Deprecation Policy](https://kubernetes.io/docs/reference/using-api/deprecation-policy/)
  — rules for API version lifetimes (alpha, beta, GA) and removal timelines.
- [Kubernetes API Changes](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api_changes.md)
  — how to evolve API types, add new fields behind feature gates, and handle
  compatibility across versions.
- [Kubernetes API Conventions](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md)
  — naming, status conventions, conditions, and general API design patterns.
- [Feature Gates](https://kubernetes.io/docs/reference/command-line-tools-reference/feature-gates/)
  — the mechanism for staging new features through alpha/beta/GA lifecycles.

---

## Product Code Guidelines

### Package Organization

- **`pkg/`** contains public-facing library code. Consumers may import these
  packages.
- **`apis/`** holds CRD type definitions organized by group and version
  (e.g. `apis/kueue/v1beta2/`).
- **`cmd/`** contains executable entry points.
- Group utilities by domain under `pkg/util/` (e.g. `pkg/util/client/`,
  `pkg/util/resource/`, `pkg/util/slices/`).

### Controller Structure

Controllers follow the controller-runtime reconciler pattern:

- Implement `reconcile.Reconciler` via a struct with injected dependencies.
- Use the **functional options pattern** for controller configuration:

  ```go
  type Option func(*MyReconciler)

  func WithClock(c clock.Clock) Option {
      return func(r *MyReconciler) { r.clock = c }
  }
  ```

- Register controllers with `builder.TypedControllerManagedBy[reconcile.Request](mgr)`.
- Use **typed predicates** and **typed event handlers** where available
  (e.g. `predicate.TypedPredicate[*kueue.Workload]`).
- Verify interface compliance at the package level:

  ```go
  var _ reconcile.Reconciler = (*MyReconciler)(nil)
  ```

### Reconcile Loop

- Return `(ctrl.Result{}, nil)` when no action is needed.
- Return `(ctrl.Result{}, err)` for errors that should trigger a retry with
  backoff.
- Return `(ctrl.Result{RequeueAfter: d}, nil)` for time-based requeue.
- Swallow not-found errors with `client.IgnoreNotFound(err)` when the resource
  was deleted.
- Do not retry unretryable errors. Use domain-specific error types like
  `UnretryableError` with corresponding `IsUnretryableError()` predicates.

### API Types (CRDs)

- Define types in `apis/<group>/<version>/` with standard kubebuilder markers.
- Always include `metav1.TypeMeta`, `metav1.ObjectMeta`, `Spec`, and `Status`.
- Use kubebuilder validation markers (`+kubebuilder:validation:*`) for field-level
  constraints.
- Use **XValidation rules** for cross-field validation.
- Mark optional fields with `+optional` and use pointer types for them.
- Define condition type and reason constants in the types file.

### Webhooks

- Register webhooks via `ctrl.NewWebhookManagedBy(mgr, obj).WithDefaulter(wh).WithValidator(wh).Complete()`.
- Return validation errors as `field.ErrorList` with hierarchical paths:

  ```go
  specPath := field.NewPath("spec")
  allErrs = append(allErrs, field.Invalid(specPath.Child("podSets").Index(i), val, "message"))
  ```

- Gate defaulting behind feature flags when the defaulted behavior is
  experimental.

### Error Handling

- Always wrap errors with `%w` to preserve the error chain:

  ```go
  return fmt.Errorf("failed to update status: %w", err)
  ```

- Use `errors.Is()` and `errors.As()` for type checking, never direct type
  assertions.
- Check Kubernetes API errors with `apierrors.IsNotFound(err)`,
  `apierrors.IsConflict(err)`, etc.
- Define domain-specific error types with constructor, type, and predicate:

  ```go
  func UnretryableError(msg string) error       { return &unretryableError{msg: msg} }
  func IsUnretryableError(e error) bool          { var target *unretryableError; return errors.As(e, &target) }
  ```

### Client Usage

- **Prefer patch-based updates** over full object updates, especially for status:

  ```go
  clientutil.PatchStatus(ctx, c, obj, func() (bool, error) {
      obj.Status.Field = newValue
      return true, nil
  })
  ```

- Use `client.FieldOwner(...)` for server-side apply patches to support concurrent
  controllers.
- Define **field indexes** as constants and register them in `SetupWithManager`:

  ```go
  const WorkloadQueueKey = "spec.queueName"
  client.List(ctx, list, client.MatchingFields{WorkloadQueueKey: name})
  ```

### Logging

- Use the **logr** interface obtained from context: `log := ctrl.LoggerFrom(ctx)`.
- Use **klog helpers** for Kubernetes objects: `klog.KObj(obj)`, `klog.KRef(ns, name)`.
- Follow the verbosity convention:
  - **V(1)**: Important state changes and key decisions.
  - **V(2)**: Detailed reconciliation flow.
  - **V(3)**: Debug details.
  - **V(4)–V(5)**: Trace-level verbosity.
- Always use structured key-value pairs, not formatted strings:

  ```go
  log.V(2).Info("Reconcile Workload", "workload", klog.KObj(wl))
  ```

### Status Conditions

- Always set `ObservedGeneration` to the object's current `.Generation`.
- Truncate unbounded-length condition messages with `api.TruncateConditionMessage(msg)`.
- Use `apimeta.SetStatusCondition()` which handles transition timestamps
  automatically.
- Use `apimeta.FindStatusCondition()` to query conditions.

### Event Recording

- Inject `events.EventRecorder` as a dependency.
- Truncate unbounded-length event messages with `api.TruncateEventMessage(msg)`.
- Use `corev1.EventTypeNormal` for happy-path events and
  `corev1.EventTypeWarning` for problems.
- Use machine-readable reason strings (PascalCase) and human-readable messages.

### Feature Gates

- Define feature gates in `pkg/features/kube_features.go` with owner, KEP link,
  and description.
- Register with versioned specs including Alpha/Beta/GA lifecycle stages.
- Check at runtime with `features.Enabled(features.MyFeature)`.
- After modifying feature gates, run `make generate-featuregates`.

### Metrics

- Define metrics in `pkg/metrics/metrics.go` with `kueue_` prefix.
- Always include `replica_role` as a label for HA awareness.
- Use `+metricsdoc` annotations for automatic documentation generation.
- Prefer `CounterVec`, `HistogramVec`, and `GaugeVec` for labeled metrics.

### Constants

- Centralize cross-package constants in `pkg/constants/constants.go`.
- Define resource-specific status constants near the resource's domain logic.
- Use typed constants (`type MyEnum string`) with kubebuilder `+enum`
  annotations.

### RBAC

- Declare RBAC permissions with `+kubebuilder:rbac` markers on the reconciler.
- Use separate annotations for each resource group.
- Separate main resource and subresource (status, finalizers) permissions.

### Concurrency

- Use `routine.Wrapper` for goroutine lifecycle management with before/after
  hooks.
- Use `routine.ErrorChannel` for non-blocking error collection from parallel
  workers.
- Use `pkg/util/parallelize` for bounded parallel execution.
- Use `roletracker.RoleTracker` for leader/follower-aware behavior in HA setups.

### Dependencies and Testability

- Inject dependencies (clock, client, recorder, caches) via constructor options.
- Use `clock.Clock` interface instead of `time.Now()` directly.
- Keep interfaces small and focused (1–3 methods).
- Define interfaces where they are consumed, not where they are implemented.

### Code Generation

- All generated files use the `zz_generated.*.go` naming convention.
- Run `make generate` after modifying API types or adding markers.
- Run `make manifests` after changing RBAC or webhook markers.
- All generated files are checked into version control.
- Every file must include the Apache 2.0 license header from
  `hack/boilerplate.go.txt`.

### Linter

- Run `make ci-lint` to enforce linter rules.
- Run `make lint-api` to enforce linter rules for API.

---

## Test Code Guidelines

### Test Organization

- **Unit tests**: co-located with source code in `*_test.go` files in the same
  package.
- **Integration tests**: under `test/integration/` organized by feature
  (e.g. `singlecluster/`, `multikueue/`).
- **E2E tests**: under `test/e2e/` organized by area.
- **Shared test utilities**: `pkg/util/testing/` and `pkg/util/testingjobs/`.
- **Integration framework**: `test/integration/framework/`.

### Test Frameworks

Use the right framework for the right level:

| Level | Framework | Assertions |
|-------|-----------|------------|
| Unit | Standard `testing` package | `go-cmp/cmp` with `cmpopts` |
| Integration | Ginkgo v2 | Gomega |
| E2E | Ginkgo v2 | Gomega |

Do not mix: unit tests should not use Gomega, and Ginkgo tests should not use
`cmp.Diff`.

### Table-Driven Unit Tests

Use **map-based** table-driven tests with descriptive string keys:

```go
cases := map[string]struct {
    localQueue     *kueue.LocalQueue
    clusterQueue   *kueue.ClusterQueue
    wantLocalQueue *kueue.LocalQueue
    wantError      error
}{
    "local queue with Hold StopPolicy": {
        // setup ...
    },
    "cluster queue is inactive": {
        // setup ...
    },
}

for name, tc := range cases {
    t.Run(name, func(t *testing.T) {
        // test logic using tc
    })
}
```

Conventions:
- Use `tc` as the variable name for the test case.
- Prefix expected-value fields with `want` (e.g. `wantError`,
  `wantLocalQueue`).
- Use human-readable, narrative case names that describe the scenario.

### Test Function Naming

- Unit tests: `TestSubjectAction` (e.g. `TestLocalQueueReconcile`,
  `TestValidateImmutablePodSpec`).
- Integration/E2E: `ginkgo.Describe("Controller name", ...)` with `ginkgo.It`
  blocks describing behavior.
- Use Ginkgo labels for filtering: `ginkgo.Label("controller:workload", "area:core")`.

### Object Builders (Wrappers)

Build test objects using the **fluent wrapper pattern**:

```go
wl := utiltestingapi.MakeWorkload("test-wl", "default").
    Queue("test-queue").
    Request(corev1.ResourceCPU, "4").
    SimpleReserveQuota("cq", "rf", now).
    Obj()
```

Key conventions:
- Wrapper types embed the real struct: `type WorkloadWrapper struct{ kueue.Workload }`.
- Factory functions use the `Make` prefix: `MakeWorkload`, `MakeClusterQueue`,
  `MakeLocalQueue`.
- Chainable methods return `*Wrapper` for fluent building.
- Call `.Obj()` to extract the inner Kubernetes object.
- Core Kubernetes type wrappers live in `pkg/util/testing/wrappers.go`.
- Kueue API type wrappers live in `pkg/util/testing/v1beta2/wrappers.go`.
- Job type wrappers live in `pkg/util/testingjobs/<jobtype>/wrappers.go`.

When adding a new field or feature, extend existing wrappers rather than
constructing objects by hand.

### Fake Client Setup

Use the project's client builder for consistent scheme and index setup:

```go
cl := utiltesting.NewClientBuilder().
    WithObjects(objs...).
    WithStatusSubresource(objs...).
    WithInterceptorFuncs(interceptor.Funcs{
        SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge,
    }).
    Build()
```

- Use `WithInterceptorFuncs` to simulate errors or intercept specific
  operations.
- Use `utiltesting.NewFakeClient(objs...)` for quick setup in simple tests.
- Always register status subresources with `WithStatusSubresource` when the test
  updates status.

### Context and Logging in Tests

Use the project helper to create a context with a test-scoped logger:

```go
ctx, log := utiltesting.ContextWithLog(t)
```

This ensures log output is captured by the test framework and visible on failure.

### Unit Test Assertions

Use `go-cmp/cmp` for comparing complex objects:

```go
if diff := cmp.Diff(tc.wantError, gotError); diff != "" {
    t.Errorf("unexpected reconcile error (-want/+got):\n%s", diff)
}
```

Use predefined comparison options from `test/util/constants.go`:

```go
cmpOpts := cmp.Options{
    cmpopts.EquateEmpty(),
    util.IgnoreConditionTimestamps,
    util.IgnoreObjectMetaResourceVersion,
}
if diff := cmp.Diff(want, got, cmpOpts...); diff != "" {
    t.Errorf("unexpected result (-want,+got):\n%s", diff)
}
```

Common options:
- `util.IgnoreConditionTimestamps` — ignore `LastTransitionTime` on conditions.
- `util.IgnoreObjectMetaResourceVersion` — ignore `ResourceVersion` on
  ObjectMeta.
- `cmpopts.EquateEmpty()` — treat nil and empty slices/maps as equal.

### Integration Test Assertions (Gomega)

Use `gomega.Eventually` for asynchronous assertions:

```go
gomega.Eventually(func(g gomega.Gomega) {
    g.Expect(k8sClient.Get(ctx, key, obj)).To(gomega.Succeed())
    g.Expect(obj.Status.Phase).To(gomega.Equal("Ready"))
}, util.Timeout, util.Interval).Should(gomega.Succeed())
```

Use custom matchers from `pkg/util/testing/`:
- `HaveConditionStatusTrue(conditionType)` / `HaveConditionStatusFalse(conditionType)`.
- `HaveConditionStatusAndReason(type, status, reason)`.
- `ContainMetrics(...)` / `ExcludeMetrics(...)`.

### Integration Test Framework (envtest)

Set up the test suite with the shared framework:

```go
var fwk *framework.Framework

var _ = ginkgo.BeforeSuite(func() {
    fwk = &framework.Framework{
        WebhookPath: util.WebhookPath,
    }
    cfg = fwk.Init()
    ctx, k8sClient = fwk.SetupClient(cfg)
})

var _ = ginkgo.AfterSuite(func() {
    fwk.Teardown()
})
```

- Use `fwk.StartManager(ctx, cfg, setupFn)` to start the controller manager.
- Use `ginkgo.Ordered` when tests must run sequentially.
- Use `ginkgo.ContinueOnFailure` to collect all failures in a suite.

### Timeout Constants

Use the predefined constants from `test/util/constants.go` for timeouts and polling intervals.

### Mocking and Error Injection

Use controller-runtime's interceptor pattern to inject errors:

```go
funcs := interceptor.Funcs{
    Get: func(ctx context.Context, client client.WithWatch, key client.ObjectKey,
        obj client.Object, opts ...client.GetOption) error {
        return errors.New("simulated error")
    },
}
cl := utiltesting.NewClientBuilder().WithInterceptorFuncs(funcs).Build()
```

Generated mocks live in `internal/mocks/` — regenerate with `make generate`.

### Integration/E2E Test Lifecycle

Follow this cleanup pattern:

```go
ginkgo.AfterEach(func() {
    gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
    util.ExpectObjectToBeDeleted(ctx, k8sClient, obj, true)
})
```

Use `ginkgo.By("description", func() { ... })` to document substeps within a test for readability
in failure output.

### Clock Injection in Tests

Use `testingclock.NewFakeClock(time.Now())` for deterministic time control:

```go
clock := testingclock.NewFakeClock(time.Now().Truncate(time.Second))
reconciler := NewMyReconciler(cl, WithClock(clock))
```

This allows tests to control time-dependent behavior without flakiness.

### Running Tests

For detailed instructions on running and debugging tests, see the [Testing Guide](/docs/contribution_guidelines/testing).
