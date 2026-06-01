---
name: code-style
description: Review a diff for naming precision, convention drift, reinvented helpers, log-verbosity mistakes, misaligned test names, and typos.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Code Style

Use this when reviewing a diff for naming, convention, and stylistic violations against
the surrounding code. The goal is consistency with the existing package, not the
reviewer's personal taste.

## Rules

Each rule in this domain is its own sub-skill. Scan the diff for violations of every
rule below.

| Rule | Triggers on |
|---|---|
| [imprecise-names](./imprecise-names/SKILL.md) | identifier whose name does not describe exactly what it contains |
| [convention-drift](./convention-drift/SKILL.md) | new code that breaks naming conventions already established in the same file or package |
| [reinvented-helpers](./reinvented-helpers/SKILL.md) | logic that duplicates an existing util/helper function instead of reusing it |
| [wrong-log-verbosity](./wrong-log-verbosity/SKILL.md) | per-reconcile-cycle log lines emitted at `V(2)` |
| [misaligned-test-names](./misaligned-test-names/SKILL.md) | test function names that do not reflect the function or behavior under test |
| [code-style-typos](./code-style-typos/SKILL.md) | typos in identifiers, strings, or any text introduced by the diff |

@./imprecise-names/SKILL.md
@./convention-drift/SKILL.md
@./reinvented-helpers/SKILL.md
@./wrong-log-verbosity/SKILL.md
@./misaligned-test-names/SKILL.md
@./code-style-typos/SKILL.md

## What not to include

Do not report personal preferences or taste-based objections. This skill is about
consistency, precision, and established local conventions, not about the reviewer's
favorite naming style or formatting habits.

In particular, do not flag:

- code that is consistent with the surrounding file or package, even if you would have
  named or structured it differently;
- short helpers, direct expressions, or naming choices that are reasonable and accurate,
  even if you would personally prefer a different phrasing;
- harmless wording differences, minor prose preferences, or cosmetic style opinions with
  no maintenance or readability impact.

## How to report

Cite the exact file and line, name the convention or rule being violated, and classify
severity (high / medium / low). Naming-precision issues that introduce semantic
ambiguity (a name that lies about its contents) are high. Convention drift and
reinvented helpers are typically medium. Stray typos and minor wording choices are low.

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
