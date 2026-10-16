---
name: kep-review
description: Review Kueue enhancement proposals (KEPs) for project-wide design concerns and readability. Use when reviewing a new or updated KEP.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# KEP Review

## Design

* Do not create KEPs dedicated to a specific out-of-tree Job integration. An
  out-of-tree Job integration uses a workload API that is not built into
  Kubernetes. Generalize KEPs around Kueue concepts and behavior so they apply
  across Job integrations.
* Check that the design is complete. Each user story needs the API or behavior
  that serves it, what happens when it cannot be served, and what the user sees
  (conditions, events, or metrics). Flag stories with no design and design with
  no story.
* Check for conflicts with the documentation under `site/content/en/docs` and
  with other KEPs under `keps/` that cover the same feature, API, or condition.
  For each conflict, ask the KEP to say how it is resolved: it supersedes the
  other text, amends the other KEP, or aligns with it.
* When the KEP changes or describes existing behavior, check its claims against
  the current code and flag where they differ. Put the code evidence in the
  review comment, not in the KEP.

## Readability

Check the KEP against the rules in [kep-writing](../kep-writing/SKILL.md).
Report at most five readability findings, most important first.
The cap is for readability only: report every place where the KEP contradicts
itself (two sections describing different behavior) as a separate finding.
For each finding:

1. Quote the section or line.
2. Name the rule it breaks.
3. Give the replacement text, or say where the content should move.

Prefer findings that remove or move content: restated content, code pointers,
external specs in the main text, notes that re-explain the design, and
unimplemented scope.

Flag wordiness: when a passage takes several lines to say what one line can,
quote it and give the one-line version.
Fix typos with a one-line suggestion.
Do not otherwise flag grammar mistakes or unusual idiom unless the meaning is ambiguous.
Many authors are not native English speakers; a short sentence with a small
grammar slip is better than a long, polished one.
