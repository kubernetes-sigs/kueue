---
name: buggy-behavior
description: Review a diff for behavioral defects — logic errors, broken edge cases, feature-gate bugs, and deletions that break backwards compatibility during rolling upgrades.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Buggy Behavior

Use this when reviewing a diff for behavioral defects: logic errors, broken edge cases,
and changes that break compatibility with objects created by older versions of the
controller.

## Rules

Each rule in this domain is its own sub-skill. Scan the diff for violations of every
rule below.

| Rule | Triggers on |
|---|---|
| [logic-errors](./logic-errors/SKILL.md) | incorrect conditionals, inverted checks, off-by-one, unhandled edge cases, races, mishandled errors |
| [deleted-backwards-compatibility-code](./deleted-backwards-compatibility-code/SKILL.md) | removal or relocation of code that reconciles state owned by previous controller versions |
| [feature-gate-interaction-bugs](./feature-gate-interaction-bugs/SKILL.md) | gated behavior whose gate-off path is untested or incorrect |
| [unnecessary-guard-conditions](./unnecessary-guard-conditions/SKILL.md) | extra checks that are logically unreachable at the call site |

@./logic-errors/SKILL.md
@./deleted-backwards-compatibility-code/SKILL.md
@./feature-gate-interaction-bugs/SKILL.md
@./unnecessary-guard-conditions/SKILL.md

## How to report

Cite the exact file and line, describe the failure mode (what input triggers it, what
goes wrong), and classify severity (high / medium / low). Hot-path logic errors and
deleted compatibility code are high. Untested gate-off paths are typically medium.
Stray unreachable guards are low.

Return two sections:

### Findings

A bullet list, one per violation, in this format:

```
- <High/Medium/Low> | <Finding Title>: <one-sentence explanation grounded in the diff>
```

### Recommendations

One recommendation per finding above, numbered sequentially within this skill. Each
recommendation MUST contain all four sections below.

```
### Recommendation N: <short title>

**Problem**: Describe the specific issue in the diff.
**Reason**: Explain why this is a problem — what goes wrong or degrades over time.
**Solution**: Give a concrete, actionable fix. Where possible, show a before/after code snippet.
**Locations**: List every place this issue occurs, one per line, in `filepath:line` form.
```
