

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
| [algorithm-comments](algorithm-comments/SKILL.md) | comment describing formula or algorithm doesn't match the code |
| [feature-gated-code](feature-gated-code/SKILL.md) | reachable code path missing a feature gate check |
| [push-guards-to-callees](push-guards-to-callees/SKILL.md) | same guard repeated at multiple call sites before the same function |
| [split-test-files](split-test-files/SKILL.md) | new feature tests added to an already large test file |
| [terminology-semantics](terminology-semantics/SKILL.md) | code or events use a term whose semantics don't match (e.g., Preempted for migration) |
| [api-field-comments](api-field-comments/SKILL.md) | new CRD field missing godoc comment or enum value descriptions |
| [race-conditions](race-conditions/SKILL.md) | snapshot/mirror field added to protect a reader from a concurrent writer |
| [visibility-exports](visibility-exports/SKILL.md) | new exported function or method with no callers outside the package |
| [code-eval](code-eval/SKILL.md) | runs code evaluation and produces a scored report |
| [architectural-decisions](architectural-decisions/SKILL.md) | domain — unjustified complexity, duplicated logic across types/adapters, scope creep, or misplaced code (see ToC inside) |
| [buggy-behavior](buggy-behavior/SKILL.md) | domain — logic errors, broken edge cases, feature-gate bugs, deletions that break backwards compatibility during rolling upgrades (see ToC inside) |
| [code-style](code-style/SKILL.md) | domain — imprecise names, convention drift, reinvented helpers, wrong log verbosity, misaligned test names, typos (see ToC inside) |
| [comments](comments/SKILL.md) | domain — over-commenting, what-not-why narration, inaccurate comments, missing TODOs on compatibility shims (see ToC inside) |
| [security](security/SKILL.md) | domain — input-validation gaps, injection, DoS, nil-safety crashes, authz relaxation, info disclosure, supply-chain weakening, webhook safety regressions (see ToC inside) |
| [imprecise-names](code-style/imprecise-names/SKILL.md) | identifier whose name does not describe exactly what it contains (code-style) |
| [convention-drift](code-style/convention-drift/SKILL.md) | new code that breaks naming conventions already established in the same file or package (code-style) |
| [reinvented-helpers](code-style/reinvented-helpers/SKILL.md) | logic that duplicates an existing util/helper function instead of reusing it (code-style) |
| [wrong-log-verbosity](code-style/wrong-log-verbosity/SKILL.md) | per-reconcile-cycle log lines emitted at `V(2)` (code-style) |
| [misaligned-test-names](code-style/misaligned-test-names/SKILL.md) | test function names that do not reflect the function or behavior under test (code-style) |
| [code-style-typos](code-style/code-style-typos/SKILL.md) | typos in identifiers, strings, or any text introduced by the diff (code-style) |
| [illogical-structure](architectural-decisions/illogical-structure/SKILL.md) | code a future maintainer will struggle to follow, modify, or extend (architectural-decisions) |
| [nonsensical-decisions](architectural-decisions/nonsensical-decisions/SKILL.md) | unnecessary indirection, mismatched abstractions, confusing data flow (architectural-decisions) |
| [avoidable-complexity](architectural-decisions/avoidable-complexity/SKILL.md) | solutions more elaborate than the problem requires (architectural-decisions) |
| [pointless-intermediate-variables](architectural-decisions/pointless-intermediate-variables/SKILL.md) | redundant local variables that add noise without clarity (architectural-decisions) |
| [duplicated-logic](architectural-decisions/duplicated-logic/SKILL.md) | identical blocks across types/adapters/call sites that should be shared — or new helpers with one caller (architectural-decisions) |
| [scope-creep](architectural-decisions/scope-creep/SKILL.md) | diffs bundling a bugfix with an unrelated refactor, or over-generalizing a change (architectural-decisions) |
| [misplaced-logic](architectural-decisions/misplaced-logic/SKILL.md) | code placed somewhere a reader would not expect to find it (architectural-decisions) |
| [logic-errors](buggy-behavior/logic-errors/SKILL.md) | incorrect conditionals, inverted checks, off-by-one, unhandled edge cases, races, mishandled errors (buggy-behavior) |
| [deleted-backwards-compatibility-code](buggy-behavior/deleted-backwards-compatibility-code/SKILL.md) | removal or relocation of code that reconciles state owned by previous controller versions (buggy-behavior) |
| [feature-gate-interaction-bugs](buggy-behavior/feature-gate-interaction-bugs/SKILL.md) | gated behavior whose gate-off path is untested or incorrect (buggy-behavior) |
| [unnecessary-guard-conditions](buggy-behavior/unnecessary-guard-conditions/SKILL.md) | extra checks that are logically unreachable at the call site (buggy-behavior) |
| [over-commenting](comments/over-commenting/SKILL.md) | comments that explain what self-documenting code already says (comments) |
| [wrong-kind-of-comment](comments/wrong-kind-of-comment/SKILL.md) | comments describing *what* instead of *why* (comments) |
| [inaccurate-comments](comments/inaccurate-comments/SKILL.md) | comments or docstrings that no longer match the code (comments) |
| [missing-deferred-removal-markers](comments/missing-deferred-removal-markers/SKILL.md) | compatibility shim kept without a deferred-removal comment (comments) |
| [comment-typos](comments/comment-typos/SKILL.md) | typos and obvious errors in comment text (comments) |
| [input-validation](security/input-validation/SKILL.md) | missing validation on user-settable CR fields, webhook payloads, labels, annotations, or untrusted deserialization (security) |
| [injection](security/injection/SKILL.md) | exec/SSRF/template/query/selector built from user-supplied strings (security) |
| [path-traversal](security/path-traversal/SKILL.md) | file paths or archive extraction without root containment (security) |
| [resource-bounds-dos](security/resource-bounds-dos/SKILL.md) | uncapped loops/allocations, missing timeouts, reconciler-wedging input, unbounded metric labels (security) |
| [nil-safety](security/nil-safety/SKILL.md) | unguarded dereferences from malformed CRs; goroutines without recover (security) |
| [authn-authz-relaxation](security/authn-authz-relaxation/SKILL.md) | webhook token skipping, failurePolicy Ignore, wildcard RBAC, privileged pod specs (security) |
| [information-disclosure](security/information-disclosure/SKILL.md) | full-object logging, request echoing, credentials in status/annotations, kubeconfig logging (security) |
| [supply-chain-hygiene](security/supply-chain-hygiene/SKILL.md) | unpinned images, TLS bypass, unjustified replace directives, curl\|sh, moving-tag Actions (security) |
| [annotation-label-namespace-abuse](security/annotation-label-namespace-abuse/SKILL.md) | generic annotation/label loops that copy or forward values (security) |
| [feature-gated-insecure-paths](security/feature-gated-insecure-paths/SKILL.md) | gated behavior whose security assumptions fail when accidentally enabled (security) |
| [integration-adapter-trust-boundary](security/integration-adapter-trust-boundary/SKILL.md) | adapters reading credential-like fields from third-party CRDs without treating them as untrusted (security) |
| [webhook-safety-regressions](security/webhook-safety-regressions/SKILL.md) | loosened failurePolicy, shortened timeouts, TLS bypass, non-idempotent mutating webhooks (security) |

@table-driven-tests/SKILL.md
@encapsulate-paired-ops/SKILL.md
@metrics-label-sets/SKILL.md
@metrics-feature-gates/SKILL.md
@extract-helpers/SKILL.md
@integration-tests-for-updates/SKILL.md
@algorithm-comments/SKILL.md
@feature-gated-code/SKILL.md
@push-guards-to-callees/SKILL.md
@split-test-files/SKILL.md
@terminology-semantics/SKILL.md
@api-field-comments/SKILL.md
@race-conditions/SKILL.md
@visibility-exports/SKILL.md
@code-eval/SKILL.md
@architectural-decisions/SKILL.md
@buggy-behavior/SKILL.md
@code-style/SKILL.md
@comments/SKILL.md
@security/SKILL.md
@code-style/imprecise-names/SKILL.md
@code-style/convention-drift/SKILL.md
@code-style/reinvented-helpers/SKILL.md
@code-style/wrong-log-verbosity/SKILL.md
@code-style/misaligned-test-names/SKILL.md
@code-style/code-style-typos/SKILL.md
@architectural-decisions/illogical-structure/SKILL.md
@architectural-decisions/nonsensical-decisions/SKILL.md
@architectural-decisions/avoidable-complexity/SKILL.md
@architectural-decisions/pointless-intermediate-variables/SKILL.md
@architectural-decisions/duplicated-logic/SKILL.md
@architectural-decisions/scope-creep/SKILL.md
@architectural-decisions/misplaced-logic/SKILL.md
@buggy-behavior/logic-errors/SKILL.md
@buggy-behavior/deleted-backwards-compatibility-code/SKILL.md
@buggy-behavior/feature-gate-interaction-bugs/SKILL.md
@buggy-behavior/unnecessary-guard-conditions/SKILL.md
@comments/over-commenting/SKILL.md
@comments/wrong-kind-of-comment/SKILL.md
@comments/inaccurate-comments/SKILL.md
@comments/missing-deferred-removal-markers/SKILL.md
@comments/comment-typos/SKILL.md
@security/input-validation/SKILL.md
@security/injection/SKILL.md
@security/path-traversal/SKILL.md
@security/resource-bounds-dos/SKILL.md
@security/nil-safety/SKILL.md
@security/authn-authz-relaxation/SKILL.md
@security/information-disclosure/SKILL.md
@security/supply-chain-hygiene/SKILL.md
@security/annotation-label-namespace-abuse/SKILL.md
@security/feature-gated-insecure-paths/SKILL.md
@security/integration-adapter-trust-boundary/SKILL.md
@security/webhook-safety-regressions/SKILL.md
