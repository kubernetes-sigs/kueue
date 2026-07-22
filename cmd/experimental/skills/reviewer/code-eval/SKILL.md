---
name: code-eval
description: Evaluate the quality of a git diff between two commits by delegating per-skill analysis (Code Style, Buggy Behavior, Comments, Architectural Decisions, Security) to subagents, then aggregating their findings and recommendations into a scored report.
argument-hint: <BaseCommit> <HeadCommit>
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Code Evaluation Skill

Evaluate the quality of a git diff between two commits. Each evaluation skill listed
below contributes findings and recommendations for one domain of concern; this skill's
job is to fan out to each one in parallel, then assemble the results into a single
scored report.

Use this when the user wants a code-quality evaluation. If the user expresses the need
without providing **BaseCommit** and **HeadCommit**, ask them for both before
proceeding.

## Arguments

The user invoked this with: $ARGUMENTS

Parse two positional arguments from `$ARGUMENTS`:
- **BaseCommit** — the base (older) commit
- **HeadCommit** — the head (newer) commit

## Principles

- Ground every penalty in a specific line or pattern from the diff. No vague criticism.
- Do not penalize stylistic choices that are consistent with the surrounding code, even if you personally prefer something else.
- Do not invent bugs that aren't there. If the code looks correct, say so.
- Reward simplicity. If the diff is clean and well-structured, say so explicitly — a clean diff scores 0 in every domain.
- Severity matters: backwards-compatibility violations and logic errors on the hot path are more serious than naming nits. Reflect this in point weights.
- **Higher score = worse code.** Every finding adds points; nothing subtracts. There is no upper cap.

## Step 1 — Retrieve the diff

Run:
```
git diff <BaseCommit> <HeadCommit>
```

Read the full output. This is the **Diff Code** that the subagent evaluators will work
against.

Also run:
```
git diff <BaseCommit> <HeadCommit> --stat
```

to get a high-level overview of changed files for the final report.

## Step 2 — Fan out to per-domain subagents

For each **domain** in the table below, spawn one subagent **in parallel** (single
message, multiple Agent tool calls). One subagent per domain — not one per sub-skill.
Each domain SKILL.md is a table-of-contents over many small rule sub-skills and
inlines them via `@<rule>/SKILL.md` references. Each subagent must:

1. Read the domain SKILL.md end-to-end. That file pulls in every rule sub-skill in the
   domain plus the shared "How to report" instructions.
2. Read the diff between `<BaseCommit>` and `<HeadCommit>` and load enough surrounding
   context (unchanged lines, nearby functions, imports, existing helpers, log-verbosity
   conventions in the package) to make grounded judgments.
3. Identify every violation of any rule sub-skill in the domain.
4. Classify each violation as **high**, **medium**, or **low**.
5. Return findings AND recommendations in the format defined in the domain SKILL.md.

Domains:
| Domain | Domain weight |
|---|---|
| [../architectural-decisions/SKILL.md](../architectural-decisions/SKILL.md) | 1.5 |
| [../code-style/SKILL.md](../code-style/SKILL.md) | 1.0 |
| [../buggy-behavior/SKILL.md](../buggy-behavior/SKILL.md) | 2.0 |
| [../comments/SKILL.md](../comments/SKILL.md) | 0.5 |
| [../security/SKILL.md](../security/SKILL.md) | 2.0 |

The domain weight is a multiplier applied to every finding in that domain — bugs and
security defects cost more than style issues, comments cost less.

This skill does **not** perform the rule-checking itself. Its job is to dispatch,
gather, score, and format.

## Step 3 — Reclassify and Score each skill

Each skill starts at **0** points. Every finding **adds** points based on its severity,
multiplied by the domain weight. There is no cap — the more findings (and the worse
they are), the higher the score. **A higher score means worse code quality.** A clean
domain with no findings scores 0.

Base severity weights (before domain multiplier):

- **High** = 15 pt
- **Medium** = 4 pt
- **Low** = 1 pt

Per-finding points = base severity weight × domain weight.

Per-domain score = sum of points across all findings in that domain.

Final score = sum of all per-domain scores. No floor, no ceiling.

Use these severity definitions when reviewing or reclassifying subagent findings:

- **High** — the finding describes a concrete defect or design problem with serious impact,
  such as a hot-path panic, data corruption or loss, broken backward compatibility,
  incorrect admission or scheduling behavior, security boundary violation, or a public/API
  semantic mismatch that is likely to mislead maintainers or users.
- **Medium** — the finding describes a real issue with meaningful but narrower impact,
  such as a realistic edge-case bug, feature-gate behavior leak, maintainability problem
  likely to cause follow-up bugs, wrong high-volume log verbosity, or misleading naming or
  comments that materially reduce readability.
- **Low** — the finding describes a real but minor issue with limited impact, such as a
  small convention drift, low-risk structural noise, isolated typo in important text, or
  minor comment/style issue that is still worth cleaning up.

Each finding could be mislabeled or misclassified, so you must reclassify each finding
based on the actual impact shown by the diff and surrounding code, not only on the label
returned by the subagent.

## Step 4 — Produce the report

Output the evaluation in this exact structure (skills and scores are examples; the set
of skills is whatever appears in the table above):

```
## Code Evaluation Report

**Commits**: <BaseCommit>..<HeadCommit>
**Files changed**: (from --stat output)

**Note**: Higher score = worse code. 0 means no findings in that domain.

---

### Skill: Code Style (weight <weight defined in Skills table>)
**Domain score**: X pt  (sum of all findings below)
**Findings**: (bullet list of specific observations, in accordance with the rules in the skill file)
  - <High/Medium/Low> | +x pt | <Finding Title>: <Explanation>

### Skill: Buggy Behavior (weight <weight defined in Skills table>)
**Domain score**: 0 pt
**Findings**:
  - No findings

### Skill: Comments (weight <weight defined in Skills table>)
**Domain score**: 0 pt
**Findings**:
  - No findings

### Skill: Architectural Decisions (weight <weight defined in Skills table>)
**Domain score**: X pt
**Findings**:
  - <High/Medium/Low> | +x pt | <Finding Title>: <Explanation>

### Skill: Security (weight <weight defined in Skills table>)
**Domain score**: X pt
**Findings**:
  - <High/Medium/Low> | +x pt | <Finding Title>: <Explanation>

---

### Final Score: X pt
(sum of all domain scores — higher is worse; 0 is a clean diff)

---

## Recommendations

(One recommendation per issue found. Only include recommendations whose corresponding finding contributed points. Each recommendation must have all sections below. Recommendations MUST be ordered by severity: all **High** first, then all **Medium**, then all **Low**. Within the same severity, order by the skill order in the table above. Number recommendations sequentially across all skills after sorting by severity.)

### Recommendation N: <short title>

**Severity**: High | Medium | Low
**Domain**: The skill domain this recommendation came from (e.g., Code Style, Buggy Behavior, Comments, Architectural Decisions, Security).
**Findings**: The exact finding title(s) this recommendation addresses, copied from the Findings section above.
**Problem**: Describe the specific issue in the diff.
**Reason**: Explain why this is a problem — what goes wrong or degrades over time.
**Solution**: Give a concrete, actionable fix. Where possible, show a before/after code snippet.
**Locations**: List every place this issue occurs, one per line, in `filepath:line` form.
```

Rules:
- The report format above MUST NOT be broken. Headings, ordering, and the four-section
  recommendation layout are normative.
- Recommendations come from the subagents — this skill aggregates them, renumbers them
  globally, and omits any whose corresponding finding added 0 points.
- Code evaluation MUST promote these principles: **Maintainability, Simplicity,
  Backward Compatibility**.
