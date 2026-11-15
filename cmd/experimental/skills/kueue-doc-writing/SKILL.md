---
name: kueue-doc-writing
description: Write, update, or review Kueue documentation from KEPs, the current implementation, and Kubernetes documentation guidance. Use when creating concept, task, tutorial, reference, troubleshooting, or contributor documentation for Kueue.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Kueue Documentation Writing

Produce an accurate, discoverable, reviewable documentation change.

## Define the change before writing

Before editing, identify:

1. **Reader:** system administrator, batch user, serving user, platform developer,
   or contributor.
2. **Outcome:** what the reader should understand or accomplish.
3. **Page type:** concept, task, tutorial, reference, or troubleshooting. Use the
   existing information architecture.
4. **Version:** `main`, a release, or several versioned sites. Match APIs, feature
   states, and examples to that target.
5. **Scope:** update the smallest suitable page; add one for a distinct audience
   and goal.

Use the [documentation root](../../../../site/content/en/docs/_index.md) and
nearest section index for the selected page type, such as
[`concepts/_index.md`](../../../../site/content/en/docs/concepts/_index.md),
[`tasks/_index.md`](../../../../site/content/en/docs/tasks/_index.md), or
[`reference/_index.md`](../../../../site/content/en/docs/reference/_index.md),
to choose navigation and a destination.

## Gather evidence

Read sources in this order:

1. The current API types, feature gates, controller behavior, generated
   references, and tests in this repository.
2. The relevant KEP: user stories, design, handling, tests, and graduation criteria.
3. Existing Kueue concepts, tasks, examples, release notes, and versioned docs.
4. Canonical Kubernetes documentation and the documentation style guidance
   linked in [References](#references).
5. External project documentation when Kueue depends on it.

The implementation is the source of truth; a KEP can describe an earlier design.
Document implemented behavior and flag discrepancies for maintainers.

For API or configuration changes, inspect the type, validation, defaulting,
feature gate, generated reference, and tests. For behavior changes, inspect
observable status, events, logs, and metrics.

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

Preserve the user problem and supported behavior. Omit implementation-only details
and abandoned alternatives; link to the KEP for design history.

## Choose a page structure

- **Concept:** overview, model and behavior, then "What's next". Link to tasks
  for procedures.
- **Task:** purpose, prerequisites, numbered steps, and expected results.
- **Tutorial:** objectives, a complete ordered example, and cleanup.
- **Reference:** exact fields, flags, defaults, and constraints. Prefer generated
  API and CLI references over hand-written copies.
- **Troubleshooting:** symptom, safe inspection commands, output interpretation,
  corrective actions, and escalation.

Keep each page focused on one outcome. Split pages for different personas,
unrelated prerequisites, or independent goals.

Cross-link related pages: tasks and tutorials to concepts, concepts to tasks,
fields and flags to API/CLI references, and design context to KEPs. Use descriptive
link text and one canonical explanation.

## Write Kueue documentation

### Front matter and navigation

- Match neighboring pages' front matter fields, title style, date, weight, and
  description.
- Put new pages under the appropriate `site/content/en/docs` subsection and
  update the nearest `_index.md` when the section uses an explicit list.
- For pages under `tasks/manage/`, `tasks/run/`, or `tasks/dev/`, also update the
  relevant persona list in
  [`tasks/_index.md`](../../../../site/content/en/docs/tasks/_index.md), in
  addition to setting front matter `weight`. The current task index groups system
  administrators under "Batch administrator"; match that existing navigation label.
- Reuse Hugo shortcodes such as `feature-state` and `include`. Feature-state
  notices use angle-bracket delimiters:
  `{{< feature-state state="beta" for_version="v0.20" >}}`.
- Keep the English page canonical. Update versioned pages only when behavior or
  supported APIs differ; check compatibility before copying `main` content.

### Terminology and style

- Use the Kubernetes style guide's U.S. English and sentence case, and Google's
  [Technical Writing resources](https://developers.google.com/tech-writing/resources):
  active voice, focused paragraphs, concrete words and examples, and no filler.
- Refer to API kinds as `Workload`, `ClusterQueue`, `LocalQueue`, `ResourceFlavor`,
  and `AdmissionCheck`; use code formatting for fields, annotations, commands,
  paths, resource names, and values where the style guide calls for it.
- Define Kueue-specific terms and use them consistently. Put results before detail.
- Use headings, numbered steps, parallel lists, and tables to clarify structure.
- Use angle-bracket placeholders such as `<namespace>` and `<workload-name>`;
  use realistic names in complete examples.
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
- For files marked as generated, edit their source or generator inputs and
  regenerate the output. In hand-written documentation, organize reference
  information into readable lists or tables, such as a table of feature gates.
  Preserve field names, values, defaults, and feature states, and link to the
  canonical generated reference for the full details.

### Examples and operational safety

- Keep names, namespaces, queues, requests, versions, and commands consistent.
- State prerequisites such as Kubernetes/Kueue versions, CRDs, feature gates,
  RBAC, external controllers, or an existing queue setup before the first step.
- Explain cleanup for resources created by a tutorial or task.
- Highlight destructive, disruptive, security-sensitive, or irreversible operations
  with an alert and a safe alternative or rollback. Percent delimiters render inner
  Markdown links and code blocks:
  `{{% alert title="Note" color="primary" %}}...{{% /alert %}}`.
- Prefer read-only inspection commands in troubleshooting guides. When a change
  is required, show how to verify the current state and how to confirm the fix.
- Link to canonical Kubernetes or dependency docs rather than copying material
  that will drift. Keep Kueue-specific behavior in the Kueue page.

## Review before delivery

Check as a user and maintainer:

1. **Behavior:** claims match implementation, APIs, gates, and integrations,
   including success and likely rejected, pending, or disabled paths.
2. **KEP alignment:** cover the user story; distinguish available behavior from
   non-goals, abandoned alternatives, and future milestones.
3. **Discoverability:** index/persona entries and cross-links use canonical paths.
4. **Examples:** ordered steps, consistent names and values, real output, and
   explicit version-specific details.
5. **Style and accessibility:** clear headings, short paragraphs, consistent API
   formatting, descriptive links, readable tables, and image text alternatives.
6. **Maintenance:** identify generated sources, avoid unnecessary version forks,
   and remove stale instructions.

Record material unanswered questions and ask a maintainer.

## Validate the change

Run the narrowest checks that cover the files changed, then report the commands
and results:

- In Prow, `make verify-website-links-preview` checks the PR Netlify preview
  when `site/` or `netlify.toml` changes. For a local preview check, set
  `LINK_CHECK_URL` to the deploy-preview URL and run
  `hack/testing/linkchecker/verify.sh` as documented in its README. Use
  `make verify-website-links` to check the deployed public site.
- `make site-server` provides a local Hugo preview. Inspect desktop and mobile
  widths when layout or shortcodes change.
- For a fast local Markdown, shortcode, and template check without the production
  PostCSS pipeline, run:

  ```shell
  hugo --environment development --source site --renderToMemory --noBuildLock
  ```

  For a full production build check, install the site's Node dependencies and run:

  ```shell
  npm --prefix site ci &&
    PATH="$PWD/site/node_modules/.bin:$PATH" \
    hugo --environment production --source site --renderToMemory --noBuildLock
  ```

- `make verify-skills-lint` validates this skill and the skills index.
- Before running `make toc-update` for a KEP or another document with a
  generated `<!-- toc -->` block, ask the user for explicit confirmation. After
  confirmation, inspect and report the resulting diff; ordinary site pages do
  not need this command.
- Before running `make verify-docs-prereqs`, ask the user for explicit
  confirmation because it regenerates checked-in documentation artifacts when
  API, CLI, metrics, or feature-gate sources change. After confirmation, inspect
  and report the resulting diff, retaining only changes in the request.

If a check cannot run, state why, what was checked instead, and the remaining
risk. Report previews, crawls, and generated references only when run.
Report changed files and reader outcome, source evidence, validation results,
version or feature-state assumptions, and open questions or operational follow-up.

## References

- [Kubernetes Documentation Content Guide](https://kubernetes.io/docs/contribute/style/content-guide/)
- [Kubernetes Documentation Style Guide](https://kubernetes.io/docs/contribute/style/style-guide/)
- [Kubernetes Page Content Types](https://kubernetes.io/docs/contribute/style/page-content-types/)
- [Google Technical Writing Resources](https://developers.google.com/tech-writing/resources)
- [Hugo shortcode notation](https://gohugo.io/content-management/shortcodes/#notation)
- [Hugo production environment](https://gohugo.io/functions/hugo/isproduction/)
- [Kueue Website Contributions](../../../../site/content/en/community/contribution_guidelines/website.md)
- [Kueue KEP Writing Skill](../kep-writing/SKILL.md)
- [Kueue Skills Index](../README.md)
