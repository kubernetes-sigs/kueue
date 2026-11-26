

# Code Review Skills

Running every review skill on every change is expensive. Do not read or run the whole
set. Pick a subset first, then read only the `SKILL.md` files that subset needs.

> **Note:** Each skill file ends with an Apache 2.0 copyright block. Ignore it — it is not part of the skill instructions.

When reviewing tests or test coverage, follow
[Writing tests](../../../../site/content/en/community/contribution_guidelines/writing_tests.md).

## Choosing what to review

1. If the request names areas or skills (for example "architecture only" or
   "security review"), run exactly those and do not ask.
2. Otherwise show the menu below, ask which options to run, and wait for the answer.
3. If you cannot ask (a bot or other non-interactive run), run option 1 only and
   state which areas were skipped.

| Option | Runs | Use when |
|---|---|---|
| 1. First pass (default) | [architectural-decisions](architectural-decisions/SKILL.md) | you only want high-level structural problems before deeper review |
| 2. Correctness | [buggy-behavior](buggy-behavior/SKILL.md) | checking logic errors, feature-gate bugs, and rolling-upgrade compatibility |
| 3. Security | [security](security/SKILL.md) | the change touches validation, webhooks, RBAC, logging of objects, or dependencies |
| 4. Style | [code-style](code-style/SKILL.md) | naming, conventions, helper reuse, and log verbosity |
| 5. Comments | [comments](comments/SKILL.md) | comment quality and accuracy |
| 6. Tests | [table-driven-tests](table-driven-tests/SKILL.md), [inline-test-steps](inline-test-steps/SKILL.md), [integration-coverage](integration-coverage/SKILL.md), [tests-run-in-ci](tests-run-in-ci/SKILL.md), [split-test-files](split-test-files/SKILL.md) | the change adds or modifies tests |
| 7. Targeted | only the standalone skills below whose trigger matches the diff | the change is narrow, such as one new metric or CRD field |
| 8. Full scored report | [code-eval](code-eval/SKILL.md), with all five domains named: Architectural Decisions, Code Style, Buggy Behavior, Comments, Security | you want a scored report across all domains |

Run the selected skills in parallel, one agent per domain or standalone skill.
Domain skills and `code-eval` read their own rule files, so do not read those rules
in advance.

## Skills

| Skill | Triggers on |
|---|---|
| [multikueue](multikueue/SKILL.md) | reviewing MultiKueue changes |
| [table-driven-tests](table-driven-tests/SKILL.md) | independent unit scenarios duplicate setup or bypass a suitable existing table |
| [inline-test-steps](inline-test-steps/SKILL.md) | new test moves steps or assertions into a local closure, callback, or test-only helper |
| [encapsulate-paired-ops](encapsulate-paired-ops/SKILL.md) | two calls/fields that always appear together but aren't wrapped |
| [metrics-label-sets](metrics-label-sets/SKILL.md) | new metric adds labels not present on existing ClusterQueue metrics |
| [metrics-feature-gates](metrics-feature-gates/SKILL.md) | new metric missing feature flag gating that similar metrics have |
| [extract-helpers](extract-helpers/SKILL.md) | local variable scope spans more of a function than necessary |
| [integration-coverage](integration-coverage/SKILL.md) | relevant interaction or API-server behavior lacks feasible integration coverage |
| [tests-run-in-ci](tests-run-in-ci/SKILL.md) | tests added or changed without evidence that a presubmit CI job selects and runs them |
| [algorithm-comments](algorithm-comments/SKILL.md) | comment describing formula or algorithm doesn't match the code |
| [feature-gated-code](feature-gated-code/SKILL.md) | reachable code path missing a feature gate check |
| [push-guards-to-callees](push-guards-to-callees/SKILL.md) | same guard repeated at multiple call sites before the same function |
| [split-test-files](split-test-files/SKILL.md) | new feature tests added to an already large test file |
| [terminology-semantics](terminology-semantics/SKILL.md) | code or events use a term whose semantics don't match (e.g., Preempted for migration) |
| [api-field-comments](api-field-comments/SKILL.md) | new CRD field missing godoc comment or enum value descriptions |
| [race-conditions](race-conditions/SKILL.md) | snapshot/mirror field added to protect a reader from a concurrent writer |
| [visibility-exports](visibility-exports/SKILL.md) | new exported function or method with no callers outside the package |
| [ctx-structured-logging](ctx-structured-logging/SKILL.md) | `context.Context` silenced with `_ = ctx`, `var _ = ctx`, or an equivalent discard |
| [code-eval](code-eval/SKILL.md) | runs code evaluation and produces a scored report |
| [architectural-decisions](architectural-decisions/SKILL.md) | domain — unjustified complexity, duplicated logic across types/adapters, scope creep, or misplaced code (see ToC inside) |
| [buggy-behavior](buggy-behavior/SKILL.md) | domain — logic errors, broken edge cases, feature-gate bugs, deletions that break backwards compatibility during rolling upgrades (see ToC inside) |
| [code-style](code-style/SKILL.md) | domain — imprecise names, convention drift, reinvented helpers, wrong log verbosity, misaligned test names, typos (see ToC inside) |
| [comments](comments/SKILL.md) | domain — over-commenting, what-not-why narration, inaccurate comments, missing TODOs on compatibility shims (see ToC inside) |
| [security](security/SKILL.md) | domain — input-validation gaps, injection, DoS, nil-safety crashes, authz relaxation, info disclosure, supply-chain weakening, webhook safety regressions (see ToC inside) |
