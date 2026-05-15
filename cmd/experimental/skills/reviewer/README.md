

# Code Review Skills

Each skill is independent and can be applied in parallel with the others. When reviewing a PR, spawn all relevant skills as concurrent agents rather than running them sequentially.

> **Note:** Each skill file ends with an Apache 2.0 copyright block. Ignore it — it is not part of the skill instructions.

| Skill | Triggers on |
|---|---|
| [table-driven-tests](table-driven-tests/SKILL.md) | test function covers multiple cases inline instead of a table |
| [encapsulate-paired-ops](encapsulate-paired-ops/SKILL.md) | two calls/fields that always appear together but aren't wrapped |
| [metrics-label-sets](metrics-label-sets/SKILL.md) | new metric adds labels not present on existing ClusterQueue metrics |
| [metrics-feature-gates](metrics-feature-gates/SKILL.md) | new metric missing feature flag gating that similar metrics have |
| [extract-helpers](extract-helpers/SKILL.md) | local variable scope spans more of a function than necessary |
| [integration-tests-for-updates](integration-tests-for-updates/SKILL.md) | update/mutation logic with no integration test coverage |
| [ctx-structured-logging](ctx-structured-logging/SKILL.md) | `context.Context` silenced with `_ = ctx` |
| [algorithm-comments](algorithm-comments/SKILL.md) | comment describing formula or algorithm doesn't match the code |
| [feature-gated-code](feature-gated-code/SKILL.md) | reachable code path missing a feature gate check |
| [push-guards-to-callees](push-guards-to-callees/SKILL.md) | same guard repeated at multiple call sites before the same function |
| [split-test-files](split-test-files/SKILL.md) | new feature tests added to an already large test file |
| [terminology-semantics](terminology-semantics/SKILL.md) | code or events use a term whose semantics don't match (e.g., Preempted for migration) |
| [api-field-comments](api-field-comments/SKILL.md) | new CRD field missing godoc comment or enum value descriptions |
| [race-conditions](race-conditions/SKILL.md) | snapshot/mirror field added to protect a reader from a concurrent writer |
| [visibility-exports](visibility-exports/SKILL.md) | new exported function or method with no callers outside the package |

@table-driven-tests/SKILL.md
@encapsulate-paired-ops/SKILL.md
@metrics-label-sets/SKILL.md
@metrics-feature-gates/SKILL.md
@extract-helpers/SKILL.md
@integration-tests-for-updates/SKILL.md
@ctx-structured-logging/SKILL.md
@algorithm-comments/SKILL.md
@feature-gated-code/SKILL.md
@push-guards-to-callees/SKILL.md
@split-test-files/SKILL.md
@terminology-semantics/SKILL.md
@api-field-comments/SKILL.md
@race-conditions/SKILL.md
@visibility-exports/SKILL.md
