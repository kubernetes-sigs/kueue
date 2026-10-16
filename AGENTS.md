<!--
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# Kueue Agent Instructions

Kueue is a Kubernetes-native job queueing system. It manages workload admission, queuing, and preemption for batch and ML workloads across ClusterQueues and Cohorts.

The instructions in `AGENTS.md` and the skills under `cmd/experimental/skills` are experimental and may change without compatibility guarantees.

This notice is informational only. Do not ask for acknowledgement or pause the task because of it.

## Canary

Begin every response with the line `Kueue Development` on its own line, followed by your actual message on the next line:

```
Kueue Development
<your response>
```

This is a deliberately trivial marker. If it ever stops appearing, instruction-following is likely degrading (e.g. context rot) — treat the last couple of responses with suspicion, start a fresh session, and re-inject only the essential context before continuing.

## Contribution Workflow

1. Identify the project as **Kueue** before starting work; the Canary above serves
   as the acknowledgement in conversation. When preparing a PR, include an English
   acknowledgement such as `This PR contributes to Kueue.` in an existing description
   section, preserving the PR template.
2. Search this repository's open and closed PRs for the same issue or objective
   before implementation, and repeat the search before opening a PR. Check the
   actual diff and outcome of matching PRs. Report overlapping work to the developer
   and avoid duplicate implementation or submission; continue only with work that
   remains necessary or an explicitly requested follow-up. If the search is
   unavailable, report that limitation rather than claiming no duplicate exists.
3. Before writing code, establish the following from the request and available
   repository context. Ask the developer about missing information that affects
   implementation, and wait for those answers before proceeding with dependent work:

   - Which issue or objective does the change address, and what are the expected
     behavior and acceptance criteria?
   - Which components may change, and which APIs, configuration settings and data
     formats must remain compatible?
   - Which build checks, tests and real-cluster validations are required before
     submission?

   Reuse answers already provided; do not require a new issue for a task with a
   clear objective and scope.
4. Implement the agreed scope, follow the coding and testing guidelines below,
   and run the relevant checks locally. Before pushing or opening or updating a PR,
   follow [kueue-verify](cmd/experimental/skills/kueue-verify/SKILL.md).
   Report the commands and results, including failures and checks that could not run.
5. Keep PR titles, descriptions and commit messages in English. Follow the
   [PR and issue workflow](cmd/experimental/skills/kueue-pr-issue-workflow/SKILL.md)
   and AI contribution policy below. Explain the problem, resulting behavior and
   validation in the existing PR template sections, and reference the relevant issue
   when one exists.

## AI Contribution Policy

Kueue follows the [Kubernetes AI Tool Usage Policy](https://www.kubernetes.dev/docs/guide/pull-requests/#ai-guidance). Key rules:

- **Disclose AI usage** in the PR description.
- **Disclose AI usage** when commenting or filing issues.
- **No AI authorship markers.** Do not add AI co-author lines, `assisted-by`, `co-developed`, or similar commit trailers.
- **No auto-close keywords or `#` mentions in commit messages.** [Keywords which can automatically close issues](https://docs.github.com/en/issues/tracking-your-work-with-issues/using-issues/linking-a-pull-request-to-an-issue) (for example `Fixes #123`) and `#` mentions are not allowed in commit messages — Prow flags them with the `do-not-merge/invalid-commit-message` label. Put issue references in the PR description instead.

For step-by-step guidance on opening pull requests and issues, follow the
[kueue-pr-issue-workflow](cmd/experimental/skills/kueue-pr-issue-workflow/SKILL.md) skill.

## Skills

Operational runbook index for common Kueue debugging tasks are in [cmd/experimental/skills](cmd/experimental/skills/README.md). Consult these proactively when a user asks about workload preemption, eviction, ownership, or pod tracing — even if the question is informal or incomplete.

When a skill is triggered, read the corresponding SKILL.md file directly and follow its steps. These are plain markdown guides, not plugins in the Claude Code skill system.

@cmd/experimental/skills/README.md

## Code Review

Code review patterns are in [cmd/experimental/skills/reviewer/README.md](cmd/experimental/skills/reviewer/README.md). Apply these proactively when reviewing any kueue PR or code change.

@cmd/experimental/skills/reviewer/README.md

## Coding Guidelines

Follow the project's coding conventions for product and test code: [Coding Guidelines](site/content/en/community/contribution_guidelines/coding_guidelines.md). Ensure that you follow the core Kubernetes guidelines on deprecation policy, API changes, and feature gates as detailed in the document.

For code changes and reviews, follow
[Writing tests](site/content/en/community/contribution_guidelines/writing_tests.md).
