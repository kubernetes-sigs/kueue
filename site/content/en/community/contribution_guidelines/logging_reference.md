---
title: "Dependency Logging Reference"
linkTitle: "Dependency Logging Reference"
weight: 17
description: >
  Reviewed logging behavior for Kueue dependencies.
---

Version-specific companion to the [logging policy]({{< relref "logging_policy.md" >}}), with no additional rules for
Kueue-owned code. Dependencies use their own levels; output also depends on the configured logger and Kueue wrappers.

This analysis is based on controller-runtime **v0.25.1** and client-go **v0.37.1**. After upgrades, recheck behavior,
source links, and affected policy statements.

## Selected diagnostics

### controller-runtime

| Level | Behavior |
|---|---|
| `Error` | [Errors returned from `Reconcile`](https://github.com/kubernetes-sigs/controller-runtime/blob/v0.25.1/pkg/internal/controller/controller.go#L480-L497). |
| `V(1)` | [Manager-recorded `Normal` and `Warning` Events](https://github.com/kubernetes-sigs/controller-runtime/blob/v0.25.1/pkg/internal/recorder/recorder.go#L110-L132). API Event recording is independent of log verbosity. |
| Plain `Info` | [API-server warning headers](https://github.com/kubernetes-sigs/controller-runtime/blob/v0.25.1/pkg/log/warning_handler.go#L46-L65). |
| `V(5)` | [`Reconcile` start and non-error completion](https://github.com/kubernetes-sigs/controller-runtime/blob/v0.25.1/pkg/internal/controller/controller.go#L480-L512). |
| `V(5)` | [Webhook metadata](https://github.com/kubernetes-sigs/controller-runtime/blob/v0.25.1/pkg/webhook/admission/http.go#L117-L164). |
| `V(5)` | [Queue snapshots every ten seconds](https://github.com/kubernetes-sigs/controller-runtime/blob/v0.25.1/pkg/controller/priorityqueue/priorityqueue.go#L525-L559). Preparation uses `Enabled()`; copying occurs under lock, emission after unlock. Copying and encoding can still be costly. |

### client-go

| Level | Behavior |
|---|---|
| `V(1–4)` | [Reflector list/watch diagnostics](https://github.com/kubernetes/client-go/blob/v0.37.1/tools/cache/reflector.go). |
| `V(3)`; rate-limited `V(2)` or `V(0)` for longer waits | [Request throttling delays](https://github.com/kubernetes/client-go/blob/v0.37.1/rest/request.go#L664-L755). |
| `V(4)` | [Retry-After diagnostics](https://github.com/kubernetes/client-go/blob/v0.37.1/rest/with_retry.go#L234). |
| `V(8–10)` | [REST-body logs](https://github.com/kubernetes/client-go/blob/v0.37.1/rest/request.go#L1270-L1305) may expose confidential data. Truncation limits: `V(8)` approximately 1 KiB; `V(9)` approximately 10 KiB; `V(10)` no truncation. |

## Kueue logger integration

[`CustomLogProcessor`](https://github.com/kubernetes-sigs/kueue/blob/9306b3bc1254963c3aa042bdfcf79cc708a8ba66/pkg/util/logging/logging.go#L56-L73)
matches concurrent-modification errors by message text, relabels them from `Error` to `V(3)`, and removes the
stack trace. The level check runs before relabeling, so these records are emitted as long as the logger configuration enables `Error` entries.
