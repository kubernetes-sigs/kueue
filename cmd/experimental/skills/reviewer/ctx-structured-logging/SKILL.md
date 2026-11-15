---
name: ctx-structured-logging
description: Review that a silenced context.Context (`_ = ctx`) is used for structured logging or passed to context-aware calls, unless discarding it is justified.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Silenced Context Must Be Used or Justified

**Flag:** A function that receives a `context.Context` and silences it instead of using it,
for example `_ = ctx`, `var _ = ctx`, or a `ctx` parameter kept only to satisfy the
compiler or the linter.

**Ask:** Whether the context should be used:

- Obtain the logger with `log := ctrl.LoggerFrom(ctx)` and log with structured key/value pairs, so the
  log lines carry the request or reconciliation values (see the Logging section of the
  [Coding Guidelines](../../../../../site/content/en/community/contribution_guidelines/coding_guidelines.md)).
- Pass `ctx` to the context-aware APIs the function calls (client calls, `Eventually`-style
  waits, outgoing HTTP requests), so cancellation and deadlines propagate.

If the context is not needed, ask to remove the parameter when the function is not bound to an
interface.

## Do not flag

- A method that implements an interface or a callback type whose signature requires
  `context.Context`. Use `_ context.Context` there; it is not a defect.
- A no-op, stub, or fake implementation, including test doubles.
- A deliberate discard that a comment explains, for example detaching from the caller's
  cancellation on purpose (prefer `context.WithoutCancel(ctx)` over dropping `ctx`).
- An unused `ctx` on a function that has nothing to log and calls no context-aware API. This is
  not an automatic defect.
