---
name: architectural-decisions
description: Review a diff for small-scale structural problems — unjustified complexity, duplicated logic, scope creep, or misplaced code.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Architectural Decisions

Use this when reviewing a diff for small-scale structural problems: how code is organized,
how parts connect, and whether complexity is justified. Complex problems may need complex
solutions, but every unit of complexity must earn its keep.

## Rules

Each rule in this domain is its own sub-skill. Scan the diff for violations of every
rule below.

| Rule | Triggers on |
|---|---|
| [illogical-structure](./illogical-structure/SKILL.md) | code a future maintainer will struggle to follow, modify, or extend |
| [nonsensical-decisions](./nonsensical-decisions/SKILL.md) | unnecessary indirection, mismatched abstractions, confusing data flow |
| [avoidable-complexity](./avoidable-complexity/SKILL.md) | solutions more elaborate than the problem requires |
| [pointless-intermediate-variables](./pointless-intermediate-variables/SKILL.md) | redundant local variables that add noise without clarity |
| [duplicated-logic](./duplicated-logic/SKILL.md) | identical blocks across types/adapters/call sites that should be shared — or new helpers with one caller |
| [scope-creep](./scope-creep/SKILL.md) | diffs bundling a bugfix with an unrelated refactor, or over-generalizing a change |
| [misplaced-logic](./misplaced-logic/SKILL.md) | code placed somewhere a reader would not expect to find it |

@./illogical-structure/SKILL.md
@./nonsensical-decisions/SKILL.md
@./avoidable-complexity/SKILL.md
@./pointless-intermediate-variables/SKILL.md
@./duplicated-logic/SKILL.md
@./scope-creep/SKILL.md
@./misplaced-logic/SKILL.md

## What not to include

Do not report personal preferences or architectural taste. This skill is for structural
problems that materially hurt maintainability, simplicity, or evolution of the code.

In particular, do not flag:

- a design choice simply because you would have organized the code differently;
- small helpers, wrappers, locals, or indirections that are reasonable in context, even if
  you would personally inline or restructure them;
- broad redesign ideas unless the current structure creates a concrete maintenance risk,
  ambiguous ownership, duplicated change burden, or extension hazard.

## How to report

For each finding, cite the exact file and line, name which rule it violates, and
classify severity (high / medium / low). Severity scales with how much the issue hurts
maintainability, simplicity, or backward compatibility. Decisions that make the code
materially harder to extend are high; cosmetic structural nits are low.

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
