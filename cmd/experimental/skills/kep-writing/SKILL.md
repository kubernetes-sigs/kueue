---
name: kep-writing
description: Write or revise a Kueue enhancement proposal (KEP) so maintainers can review it quickly. Use when drafting a new KEP, updating an existing one, or before opening or pushing a KEP pull request.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# KEP Writing

Short is not the goal. One home per question is.

## Rules

1. **Each section answers one question.**
   Summary says what the KEP delivers.
   Motivation says why users need it.
   Proposal is the high-level direction.
   Design Details holds the technical content.
   Implementation details belong in Design Details, not in Motivation or Proposal.
   For example, a Summary can be one sentence: "This KEP adds X so that users can Y."

2. **Say it once.**
   Notes, Risks, and Graduation Criteria must not restate the design.
   If a paragraph repeats another section, delete it or move the content to its home.
   Notes are for what is surprising, not for re-explaining the design.

3. **Describe behavior, not code.**
   No file paths, function names, or line pointers.
   Put implementation links in the PR description.

4. **Keep other projects' specs out of the main text.**
   Explanations of Kubernetes or DRA features go to an Appendix, so the reader can see what is new in Kueue.
   Drop paragraphs about features that are only conceptually related.

5. **Show, then explain.**
   Turn lists of cases into user stories whose titles state the problem.
   For example, "Story 1: small jobs wait behind one large job", not "Story 1".
   Each story shows the YAML a user would write; the core part of the manifest is enough.
   Prefer a table or numbered steps to long prose.
   For state transitions, such as new conditions and reasons, add a small state diagram.
   Keep a clear list of steps as text; do not replace it with a diagram.
   Simplify formulas into named terms a reader can follow.

6. **Define new words before using them.**
   Add a Terminology section for any informal term.
   If a new term clashes with an existing one, qualify it.
   Use the qualified term everywhere so the two cannot be confused.

7. **Keep scope honest.**
   Unimplemented ideas, or knobs without a stated use case, go to "Future work ideas" or Alternatives.
   Move material there; do not just delete it.
   Non-Goals must agree with the rest of the document.
   If Alpha supports only part of an API, say how the rest is rejected, for example by validation.
   Describe Kueue behavior, not one Job integration; list the integrations covered, and make support for all of them a later-stage goal.

8. **Risks are risks of the design.**
   "We might introduce bugs" is not a risk.
   Admin or user confusion is.
   Give each risk its own heading that names the risk,
   for example "Admins may set X and get Y", not "Risk 1".

9. **Graduation criteria say what must be true to graduate.**
   Start from the usual baseline: Alpha behind a feature gate disabled by default, with docs and examples;
   Beta enabled by default, with all reported bugs fixed.
   Add each known open problem as a named Beta or GA must-have, or keep the feature in Alpha.
   Give a part that graduates on its own its own feature gate.
   Avoid promises nobody will check, such as "no flakes for two releases".

10. **Interactions have one home.**
    List every feature or feature gate this one interacts with in one section, and say what changes for each.

## Language style

Write to the point. Say the point first, then the detail.
Cut words that carry no information.
This is about each sentence, not the length of the KEP. Cut repetition, not content.

Keep lines short, as the KEP template asks, so reviewers can cite exact text.
One sentence per line is the simplest way.
If a point fits in one line, use one line.
Delete sentences that announce or repeat the next one.
Use short, direct sentences.
Prefer the shorter form:

| Instead of | Write |
|---|---|
| As mentioned above, X | X |
| In order to | To |
| This ensures that X is able to | X can |
| The mechanism described above | Name the mechanism |

Do not polish grammar for its own sake.
A plain sentence that is clear is better than a fluent paragraph that hides the point.

## Before opening or pushing the PR

Do this first review yourself before asking for review.
For AI-assisted changes, the [Kubernetes AI guidance](https://www.kubernetes.dev/docs/guide/pull-requests/#ai-guidance) requires it.

1. List every section with its word count.
   Justify any section over about 500 words, or split it into named sub-sections.
2. Search for file paths, function names, and `.go` references (rule 3).
3. For each paragraph, ask: is this already said elsewhere? (rule 2)
4. Remove unused optional sections and leftover template text.
5. Check that Non-Goals, Proposal, and Graduation Criteria agree.
   Also search the docs and other KEPs for the feature you change, and say how any conflict is resolved.
6. Update `kep.yaml`: `feature-gates`, `latest-milestone`, and `see-also` for related KEPs.
7. Use the current API version in API examples and links.
8. In the Test Plan, say which level (unit, integration, e2e) covers each new behavior.
9. Disclose AI use in the PR description, as `AGENTS.md` requires.
