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

## AI Contribution Policy

Kueue follows the [Kubernetes AI Tool Usage Policy](https://www.kubernetes.dev/docs/guide/pull-requests/#ai-guidance). Key rules:

- **Disclose AI usage** in the PR description.
- **Disclose AI usage** when commenting or filing issues.
- **No AI authorship markers.** Do not add AI co-author lines, `assisted-by`, `co-developed`, or similar commit trailers.
- Always use `PULL_REQUEST_TEMPLATE.md` or `ISSUE_TEMPLATE`, in the .github directory, before creating pull requests or issues.

## Skills

Operational runbook index for common Kueue debugging tasks are in [cmd/experimental/skills](cmd/experimental/skills/README.md). Consult these proactively when a user asks about workload preemption, eviction, ownership, or pod tracing — even if the question is informal or incomplete.

When a skill is triggered, read the corresponding SKILL.md file directly and follow its steps. These are plain markdown guides, not plugins in the Claude Code skill system.

@cmd/experimental/skills/README.md

## Code Review

Code review patterns are in [cmd/experimental/skills/reviewer/README.md](cmd/experimental/skills/reviewer/README.md). Apply these proactively when reviewing any kueue PR or code change.

@cmd/experimental/skills/reviewer/README.md

## Quality Assurance & Definition of Done (DoD) Protocol

To guarantee the highest quality and eliminate manual testing overhead for the user, the agent must strictly enforce the following verification steps before declaring any task complete or requesting user review:

1. **Autonomous Local Compilation & Verification (The "Zero-User-Testing" Rule):**
   - Whenever any webpage, layout, template, stylesheet, or configuration is modified, the agent **must proactively run the local compiler/build server** (e.g., `hugo`, `npm run build`, or `blaze build`) in a background terminal.
   - The agent must parse the build logs and verify that the site compiles with **zero warnings, zero compilation errors, and zero deprecation notices**.

2. **Automated Path, Link, & Fallback Validation:**
   - If any routing, URL paths, translation-keyed links, or navigation structures are added or modified, the agent **must write or run a validation script** (e.g., a Python scraper) against the compiled HTML output in the `public/` directory.
   - The script must verify that:
     - All links (including localized prefixes like `/zh-cn/...`) resolve to valid, existing compiled files.
     - Symmetrical language fallbacks are tested and display the target fallback content without rendering 404s or broken buttons on untranslated pages.

3. **Symmetrical i18n & Translation Auditing:**
   - The agent must perform a line-by-line review of modified layouts to ensure **100% string internationalization** (no hardcoded English or Chinese display strings in shared HTML/Go templates).
   - All translation keys must be added symmetrically and accurately to both the English (`en.yaml`) and Chinese (`zh-CN.yaml`) catalogs.
   - **Frontmatter Synchronization:** When creating, moving, or modifying translated Markdown files, the agent MUST compare the YAML frontmatter against the English source file. Layout-critical metadata (such as `type: docs`) must be perfectly mirrored to prevent missing sidebars or broken navigation.

4. **Framework Utility Conformance:**
   - The agent must never guess utility classes (e.g., Bootstrap spacing). All classes used must conform strictly to the standard, active framework version configured in the repository.

5. **Self-Correction & Log Auditing:**
   - If a build fails or a warning occurs, the agent must immediately inspect its own logs, diagnose the root cause, and apply the fix autonomously, rather than asking the user for assistance or feedback.

6. **Strict Git Push Constraints:**
   - The agent MUST NEVER run `git push` to a remote repository unless the user explicitly orders it (e.g., by saying "push it"). All changes must be kept local for user verification.

