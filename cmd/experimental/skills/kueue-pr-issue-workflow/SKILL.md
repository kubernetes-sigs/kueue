---
name: kueue-pr-issue-workflow
description: Open or update Kueue pull requests and issues following the required templates, labels, titles, and AI disclosure rules.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Open Pull Requests and Issues

Follow this guidance whenever opening or updating a Kueue pull request or issue. The
requirements below come from `.github/PULL_REQUEST_TEMPLATE.md` and `.github/ISSUE_TEMPLATE/`.

## Opening pull requests

- Read `.github/PULL_REQUEST_TEMPLATE.md` and fill its existing sections.
  Preserve their titles and order; do not add, remove, rename, or combine
  sections.
- Before submitting or updating the description, check it against the template.
- CodeRabbit AI (@coderabbitai) may append its `AI summary` section,
  including `Suggested release note`.

## Opening issues

- Inspect `.github/ISSUE_TEMPLATE/`, select the single template that best
  matches the issue type, and read it in full.
- Apply all labels specified in the selected template's `labels` field,
  including every `kind/*` label when multiple are listed.
- Fill its existing sections. Preserve their titles and order; do not add,
  remove, rename, or combine sections.
- Start the title with the emoji from the template's `title` field.
- When CodeRabbit AI (@coderabbitai) opens an issue on behalf of a contributor,
  mention the requester as @username and include a link to the comment requesting the issue.
