---
name: comments
description: Review a diff for comment hygiene — over-commenting, what-not-why narration, inaccurate comments, and missing TODOs on compatibility shims.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Comments

Use this when reviewing a diff for comment hygiene: too many, too few in the wrong
places, or — worst — comments that lie about what the code does.

## Rules

Each rule in this domain is its own sub-skill. Scan the diff for violations of every
rule below.

| Rule | Triggers on |
|---|---|
| [over-commenting](./over-commenting/SKILL.md) | comments that explain what self-documenting code already says |
| [wrong-kind-of-comment](./wrong-kind-of-comment/SKILL.md) | comments describing *what* instead of *why* |
| [inaccurate-comments](./inaccurate-comments/SKILL.md) | comments or docstrings that no longer match the code |
| [missing-deferred-removal-markers](./missing-deferred-removal-markers/SKILL.md) | compatibility shim kept without a deferred-removal comment |
| [comment-typos](./comment-typos/SKILL.md) | typos and obvious errors in comment text |

@./over-commenting/SKILL.md
@./wrong-kind-of-comment/SKILL.md
@./inaccurate-comments/SKILL.md
@./missing-deferred-removal-markers/SKILL.md
@./comment-typos/SKILL.md

## How to report

Cite the exact file and line, describe the problem, and classify severity
(high / medium / low). Inaccurate comments that will mislead a future maintainer, and
missing deferred-removal markers on compatibility shims, are high. Over-commenting
and what-not-why narration are typically low. Full marks are warranted when comments
are absent or limited to explaining the non-obvious *why* — do not invent findings.

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
