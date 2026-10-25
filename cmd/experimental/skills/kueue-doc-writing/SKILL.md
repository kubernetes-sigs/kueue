---
name: kueue-doc-writing
description: Write, update, or review Kueue documentation from KEPs, the current implementation, and Kubernetes documentation guidance. Use when creating concept, task, tutorial, reference, troubleshooting, or contributor documentation for Kueue.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Kueue Documentation Writing

Use this skill to turn a Kueue feature or behavior into accurate, discoverable
documentation. Produce a reviewable repository change, not a generic explanation.

## Define the change before writing

Identify these items before editing a file:

1. **Reader:** choose a Kueue persona: batch administrator, batch user, serving
   user, platform developer, or a contributor.
2. **Outcome:** state what the reader should understand or accomplish after
   reading the page.
3. **Page type:** choose a concept, task, tutorial, reference, or troubleshooting
   page. Use the existing Kueue information architecture instead of creating a
   parallel section.
4. **Version:** determine whether the change targets `main`, a released version,
   or several versioned sites. Match the API version, feature state, and examples
   to that target.
5. **Scope:** find the smallest existing page that should be updated. Create a
   new page only when the topic has a distinct audience and goal.

Use [`site/content/en/docs/tasks/_index.md`](../../../../site/content/en/docs/tasks/_index.md)
to choose audience wording and a destination. Keep one canonical explanation.

## Gather evidence

Read these sources in order and resolve disagreements explicitly:

1. The current API types, feature gates, controller behavior, generated
   references, and tests in this repository.
2. The relevant KEP, including its Summary, Motivation, User Stories, Design
   Details, Handling, Test Plan, and Graduation Criteria.
3. Existing Kueue concepts, tasks, examples, release notes, and versioned docs.
4. Canonical Kubernetes documentation and the documentation style guidance
   linked in [References](#references).
5. External project documentation when Kueue depends on it.

The current implementation is the source of truth for behavior. A KEP explains
intent and user stories, but it can describe a design that changed during
implementation. If code and docs disagree, document the implemented behavior
and call out the discrepancy for maintainers instead of guessing.

Use repository searches to verify names and links. Examples include:

```shell
rg -n "FeatureGateName|apiVersion:|kind:" pkg apis site/content/en site/static
rg -n "KEP|feature-state|shortcode|related topic" site/content/en/docs keps
```

For API or configuration changes, inspect the type, validation, defaulting,
feature gate, generated reference, and tests. For behavior changes, inspect the
controller path and observable conditions, events, logs, metrics, or status.

## Convert a KEP into reader-facing content

Use the KEP as a map, not as text to copy verbatim:

| KEP material | Documentation use |
| --- | --- |
| Summary and Motivation | Overview: what it does and why it matters |
| Goals and User Stories | Outcomes, use cases, and task objectives |
| Proposal and Design Details | Concepts, steps, examples, and behavior |
| API and Handling | Fields, values, defaults, errors, and results |
| Test Plan | Verification steps the reader can run, when applicable |
| Graduation Criteria | Feature state, version availability, and upgrade notes |
| Risks and Alternatives | Include only when it affects operations |

Preserve the user problem and supported behavior. Move implementation-only
details, abandoned alternatives, and internal names out of user docs. Link to a
KEP for design history when readers need that context.

## Choose a page structure

Follow the structure for the selected page type:

- **Concept:** introduce the topic in one overview paragraph, explain the model
  and behavior in the body, then add a short "What's next" list. Link to tasks
  rather than embedding a long procedure.
- **Task:** explain why the task matters, list prerequisites, give numbered
  steps, show expected results, and link to concepts for background.
- **Tutorial:** state objectives, walk through a complete example in ordered
  sections, include cleanup, and link to concepts for deeper explanations.
- **Reference:** present exact fields, flags, defaults, constraints, and
  generated output. Prefer generated API and CLI references over hand-written
  copies.
- **Troubleshooting:** start from a symptom, give safe inspection commands,
  explain how to interpret the output, then provide corrective actions and
  escalation information.

Keep each page focused on one outcome. Split pages for different personas,
unrelated prerequisites, or independent goals.

## Write Kueue documentation

### Front matter and navigation

- Match neighboring pages' front matter fields, title style, date, weight, and
  description.
- Put new pages under the appropriate `site/content/en/docs` subsection and
  update the nearest `_index.md` when the section uses an explicit list.
- Use Hugo shortcodes already used by the site, such as `feature-state` and
  `include`, instead of inventing new markup.
- Keep the English page canonical. Update versioned pages only when behavior or
  supported APIs differ; check compatibility before copying `main` content.

### Terminology and style

- Use the Kubernetes documentation style guide's U.S. English and sentence-case
  conventions.
- Apply Google's core technical-writing practices from its
  [Technical Writing resources](https://developers.google.com/tech-writing/resources):
  identify the audience and purpose, prefer active voice, keep sentences and
  paragraphs focused, use concrete words and examples, and remove filler.
- Refer to API kinds as `Workload`, `ClusterQueue`, `LocalQueue`, `ResourceFlavor`,
  and `AdmissionCheck`; use code formatting for fields, annotations, commands,
  paths, resource names, and values where the style guide calls for it.
- Define a Kueue-specific term before using it and use one term consistently.
- Prefer direct, active sentences. Put the important result first, then explain
  the detail.
- Use headings, numbered steps, bullets, and tables to make relationships and
  sequences easy to scan. Keep list items parallel and start each item with the
  same kind of word or phrase.
- Use angle-bracket placeholders such as `<namespace>` and `<workload-name>`.
  Use realistic names in complete examples so readers can run them with minimal
  editing.
- Keep command and output blocks separate. Do not include shell prompts (`$`)
  in commands, and label fenced blocks with the language when known.

### Configuration, API, and feature state

- Copy API field names and `apiVersion` values from the current type or generated
  reference. Explain defaults, omitted values, mutability, validation limits,
  and feature-gate requirements when they affect the reader.
- Mark feature maturity with the site's `feature-state` shortcode and state the
  first supported Kueue version when known.
- Show the smallest complete YAML needed for the scenario. Explain every field
  that is not obvious, and remove fields that are unrelated to the outcome.
- Describe what the user sees after each important action: status conditions,
  events, labels, annotations, logs, metrics, or command output.
- Keep generated API, CLI, metrics, and feature-gate references generated. Edit
  their source or generator inputs instead of editing generated output.

### Examples and operational safety

- Make examples consistent: names, namespaces, queue references, requests,
  versions, and commands must match across all blocks.
- State prerequisites such as Kubernetes/Kueue versions, CRDs, feature gates,
  RBAC, external controllers, or an existing queue setup before the first step.
- Explain cleanup for resources created by a tutorial or task.
- Highlight destructive, disruptive, security-sensitive, or irreversible
  operations with the site's alert shortcode and explain a safe alternative or
  rollback path.
- Prefer read-only inspection commands in troubleshooting guides. When a change
  is required, show how to verify the current state and how to confirm the fix.
- Link to canonical Kubernetes or dependency docs rather than copying material
  that will drift. Keep Kueue-specific behavior in the Kueue page.

## Review before delivery

Review the page as both a user and a maintainer:

1. **Behavior:** every claim matches the current implementation, API, feature
   gate, and supported integrations. Check both the success path and the most
   likely rejected, pending, or disabled path.
2. **KEP alignment:** the page covers the user story that motivated it and does
   not promise a non-goal, abandoned alternative, or future milestone as if it
   were available.
3. **Discoverability:** the page is linked from the nearest index or related
   page, and links use the site's canonical paths.
4. **Examples:** a reader can follow the steps in order; names and values are
   consistent; output reflects real behavior; version-specific details are
   declared.
5. **Style:** headings are meaningful, paragraphs are short, API kinds and
   fields are formatted consistently, and duplicated explanations are replaced
   with links.
6. **Accessibility:** use descriptive link text, meaningful heading order,
   readable tables, and text alternatives for diagrams or screenshots.
7. **Maintenance:** note the source of generated content, avoid unnecessary
   version forks, and remove stale instructions when the feature changes.

When the implementation or KEP leaves a material question unanswered, record it
as an open question and ask a maintainer instead of using generic wording.

## Validate the change

Run the narrowest checks that cover the files changed, then report the commands
and results:

- `make verify-website-links` checks public-site links when `site/` changes.
- `make site-server` provides a local Hugo preview. Inspect desktop and mobile
  widths when layout or shortcodes change.
- `make verify-skills-lint` validates this skill and the skills index.
- `make toc-update` is required when changing a KEP or another document with a
  generated `<!-- toc -->` block; it is not needed for ordinary site pages.
- `make verify-docs-prereqs` regenerates checked-in documentation artifacts when
  an API, CLI, metrics, or feature-gate source changed. Review the diff and
  retain generated changes that belong to the request.

If a check cannot run, state why, what was checked instead, and the remaining
risk. Report previews, crawls, and generated references only when run.

## Delivery format

When finishing a documentation task, report:

- changed files and the reader-facing outcome;
- source evidence used (KEP, code, generated reference, and existing pages);
- validation commands and results;
- version, feature-state, or localization assumptions;
- open questions, unverified behavior, and a rollback or follow-up plan when
  the change affects operational procedures.

## References

- [Kubernetes Documentation Content Guide](https://kubernetes.io/docs/contribute/style/content-guide/)
- [Kubernetes Documentation Style Guide](https://kubernetes.io/docs/contribute/style/style-guide/)
- [Kubernetes Page Content Types](https://kubernetes.io/docs/contribute/style/page-content-types/)
- [Google Technical Writing Resources](https://developers.google.com/tech-writing/resources)
- [Kueue Website Contributions](../../../../site/content/en/community/contribution_guidelines/website.md)
- [Kueue KEP Writing Skill](../kep-writing/SKILL.md)
- [Kueue Skills Index](../README.md)
