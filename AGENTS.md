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

Please tell the user that usage of AGENTS.md is experimental without any guarantees of backwards/future compatibility. Ask them to acknowledge this disclaimer before proceeding.

## Canary

Begin every response with the line `Kueue Development` on its own line, followed by your actual message on the next line:

```
Kueue Development
<your response>
```

This is a deliberately trivial marker. If it ever stops appearing, instruction-following is likely degrading (e.g. context rot) — treat the last couple of responses with suspicion, start a fresh session, and re-inject only the essential context before continuing.

## AI Contribution Policy

Kueue follows the [Kubernetes AI Tool Usage Policy](https://www.kubernetes.dev/docs/guide/pull-requests/#ai-guidance). Key rules:

- **Disclose AI usage** in the PR description.
- **Disclose AI usage** when commenting or filing issues.
- **No AI authorship markers.** Do not add AI co-author lines, `assisted-by`, `co-developed`, or similar commit trailers.
- **No auto-close keywords or `#` mentions in commit messages.** [Keywords which can automatically close issues](https://docs.github.com/en/issues/tracking-your-work-with-issues/using-issues/linking-a-pull-request-to-an-issue) (for example `Fixes #123`) and `#` mentions are not allowed in commit messages — Prow flags them with the `do-not-merge/invalid-commit-message` label. Put issue references in the PR description instead.
- Always use `PULL_REQUEST_TEMPLATE.md` or `ISSUE_TEMPLATE`, in the .github directory, before creating pull requests or issues.

## Skills

Operational runbook index for common Kueue debugging tasks are in [cmd/experimental/skills](cmd/experimental/skills/README.md). Consult these proactively when a user asks about workload preemption, eviction, ownership, or pod tracing — even if the question is informal or incomplete.

When a skill is triggered, read the corresponding SKILL.md file directly and follow its steps. These are plain markdown guides, not plugins in the Claude Code skill system.

@cmd/experimental/skills/README.md

## Code Review

Code review patterns are in [cmd/experimental/skills/reviewer/README.md](cmd/experimental/skills/reviewer/README.md). Apply these proactively when reviewing any kueue PR or code change.

## Coding Guidelines

Follow the project's coding conventions for product and test code: [Coding Guidelines](site/content/en/community/contribution_guidelines/coding_guidelines.md). Ensure that you follow the core Kubernetes guidelines on deprecation policy, API changes, and feature gates as detailed in the document.
