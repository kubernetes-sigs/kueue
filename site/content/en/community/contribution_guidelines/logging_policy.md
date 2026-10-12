---
title: "Logging Policy"
linkTitle: "Logging Policy"
weight: 16
description: >
  Logging levels and practices for Kueue-owned code.
---

This policy defines logging levels and practices for Kueue-owned code, building on the [Kubernetes logging
conventions](https://github.com/kubernetes/community/blob/main/contributors/devel/sig-instrumentation/logging.md) and
[Google Go logging guidance](https://google.github.io/styleguide/go/best-practices#logging-errors).

Apply this policy to new or modified records. Existing records are being migrated incrementally.

## Choosing a level

Use the level table below to guide your choice.

For unclear cases, consider the diagnostic purpose, preparation cost, frequency, and payload size at realistic load.

At configured verbosity `n`, informational records at levels `0` through `n` are enabled.

### Intended use table

| Level | Intended use |
|---|---|
| `Error` | Operational failures that prevent or materially disrupt an operation and may require operator attention. |
| `V(0)` | Rare anomalies affecting Kueue as a whole, such as invariant violations. Use `Error` if the condition prevents or materially disrupts an operation. |
| `V(1)` | Infrequent lifecycle and health transitions: startup, configuration, leadership, and connection health. Rare anomalies confined to a controller or the scheduler. |
| `V(2)` | Concise summaries of component activity: material changes in overall state, and the start and completion of top-level scheduler cycles. |
| `V(3)` | Main per-object diagnostics: `Reconcile` start, end, and duration; key informer observations; main scheduling and controller decisions, their reasons, and outcomes; useful context for changes saved through the API; and relevant temporary differences between cache and API state. |
| `V(4)` | Selected details that help explain a decision or change: relevant old and new values, aggregated decision context, and brief plans for grouped writes, fan-out, or enqueue activity. |
| `V(5)` | Implementation traces: individual scheduling calculations and comparisons, heap/cache mechanics, individual API-write attempts and retries, additional informer/webhook metadata, and repeated steps within searches or fan-out. |
| `V(6+)` | Deep tracing and broad internal-state dumps. |

The table assigns levels to records; it does not require a separate record for every activity.

## Guidelines

Errors:

- Avoid `V(n).Error(...)` and `Enabled()` guards around error logging.
- Return operational errors from lower-level code and log them once at a higher control layer.
- Preserve each cause when combining failures.
- `NotFound` or `Conflict` errors may be logged as diagnostics when normal control flow handles them.

API writes:

- Report a persisted transition after confirming the change.
- Prefer emitting an Event after a successful write. If controller-runtime also logs the Event, a separate log record
  can be omitted.
- Logging before a write is optional and should use `V(4+)`.

Performance:

- Check `Enabled()` on the emitting logger before costly computation, allocation, serialization, or locking solely for
  logging.
- Use lower verbosity levels for records containing information of bounded size, such as counters or enum values.
- Use higher verbosity levels for potentially large fields that are already computed.
- Use deep tracing (`V(6+)`) for fields that require dedicated state dump computations.

Log construction:

- Use the logger from the propagated context rather than creating a logger without that context.
- Use stable lowerCamelCase keys, such as `workload`, `job`, `clusterQueue`, and `resourceFlavor`.

Security:

- Avoid logging confidential data, such as tokens, secrets, and credentials, at any verbosity. Note that client-go may
  log REST bodies containing confidential data at high verbosity.

## Dependency logging

Libraries use their own log levels and logger context. See the
[dependency logging reference]({{< relref "logging_reference.md" >}}) for reviewed behavior, versions, and source links.
