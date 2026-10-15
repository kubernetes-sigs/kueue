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
* Check that each example and user story works with Kueue's default
  configuration. Name every non-default setting it needs.
* For each API or controller outside Kueue that the design depends on, check
  that the KEP says how mature it is and which implementations support it.
* Apply the [reviewer skills](../reviewer/README.md) to the design that the KEP
  describes. Ask the question of each skill about the proposal, not about code.
  These skills apply to most KEPs:
  * [architectural-decisions](../reviewer/architectural-decisions/SKILL.md):
    compare the design with the simplest existing Kueue mechanism.
  * [api-field-comments](../reviewer/api-field-comments/SKILL.md) and
    [imprecise-names](../reviewer/code-style/imprecise-names/SKILL.md): new API
    fields.
  * [buggy-behavior](../reviewer/buggy-behavior/SKILL.md): the gate turned off
    while the new fields are set. Also ask what happens
    when another controller changes the same object between two steps of Kueue.
  * [security](../reviewer/security/SKILL.md): new trust boundaries and relaxed
    authentication or authorization.
  * [integration-coverage](../reviewer/integration-coverage/SKILL.md) and
    [tests-run-in-ci](../reviewer/tests-run-in-ci/SKILL.md): each test in the
    Test Plan can run at its level.
* When the KEP changes or describes existing behavior, check its claims against
  the current code and flag where they differ. Put the code evidence in the
  review comment, not in the KEP.

## Readability

Check the KEP against the rules in [kep-writing](../kep-writing/SKILL.md).
Report every place where the KEP contradicts itself (two sections describing
different behavior) as a separate finding.
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

## Procedure

Do not stop at the first findings. Go through the KEP once per item below, and
search the whole file each time:

1. Each Design check above.
2. Each numbered rule in kep-writing, in order.
3. Each step of the kep-writing checklist "Before opening or pushing the PR",
   including `kep.yaml`.
4. The Language style section. Search for "the controller", "the webhook" and
   other role names, and for sentences that repeat the sentence before them.

## Which findings to report

Report a finding only when all of these are true:

* The author would change the KEP if you told them.
* You can quote the KEP text and name the rule, or give the code evidence.
* The text is in this PR's diff.
* A maintainer would not call it pedantic.

A missing required section passes the quote and diff tests: name the section and where it belongs.

Merge findings with the same cause or the same fix into one comment, and list
every location in it.
Report every finding that passes. Do not add weaker findings to fill the review.
If no finding passes, say so.

## Output

1. Start with a summary. Give a verdict (approve, approve after the blocking
   items, or redesign) and list each blocking item in one line.
2. Label each comment "Blocking", "Non-blocking" or "Nit". A blocking comment is
   about scope, API shape, correctness, upgrade or rollback, a contradiction, a
   wrong claim about the code, or a missing required section.
3. When the verdict is redesign, post only the design comments. Say in the
   summary that you held the wording comments until the design settles.
4. Give replacement text as a suggestion, not as a description of the change.
