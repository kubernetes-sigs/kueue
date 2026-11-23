---
name: wrong-log-verbosity
description: Flag log levels that violate the Kueue Logging Policy, such as per-object reconcile logs at V(2) or below, V(n).Error calls, or hot-loop traces below V(5).
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Wrong Log Verbosity

**Wrong log verbosity** — log records whose verbosity violates the
[Kueue Logging Policy](../../../../../../site/content/en/community/contribution_guidelines/logging_policy.md):

- Per-object reconciliation or scheduling diagnostics (such as `Reconcile` start/end, informer
  observations, or per-object decisions) emitted at `V(2)` or below — `V(1)` is for infrequent
  lifecycle/health transitions and `V(2)` is for concise summaries of overall component activity;
  main per-object diagnostics belong at `V(3)`.
- Pre-write logs, old/new value diffs, or grouped fan-out/enqueue details emitted below `V(4)`.
- Implementation traces (heap/cache mechanics, per-attempt retries, additional informer/webhook
  metadata, repeated inner-loop steps) emitted below `V(5)`.
- Error logs guarded by `V(n).Error(...)` or `Enabled()` checks — use `log.Error(...)` directly
  at the control layer for operational failures, or an appropriate `V(n).Info(...)` diagnostic
  record for expected `NotFound`/`Conflict` errors handled by normal control flow.
