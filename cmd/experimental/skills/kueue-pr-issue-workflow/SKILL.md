---
name: kueue-pr-issue-workflow
description: Open or update Kueue pull requests and issues following the required templates, labels, titles, and AI disclosure rules. Before a PR is opened or updated, the agent completes the applicable release-note, Useful notes, and feature-gate checks.
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
  including `Suggested release note`. That block is not the release-note
  fence, not the Useful notes test rows, and not the AI disclosure.
- Run the relevant tests and report the results in the Useful notes rows
  under [Before gh pr create](#before-gh-pr-create). Choose them with
  [Writing tests](../../../../site/content/en/community/contribution_guidelines/writing_tests.md)
  and run them as described in
  [Running and debugging tests](../../../../site/content/en/community/contribution_guidelines/testing.md).
  `make verify` is a separate [kueue-verify](../kueue-verify/SKILL.md) check
  and is not a row.

### Before gh pr create

Run these checks before `gh pr create`, and before `gh pr edit` when updating
the body. Fill existing template sections only.

#### Classify

- Dependabot (`dependabot[bot]`, or the `dependencies` or `release-note-none`
  label): leave the `release-note` fence as `NONE`. Skip test rows and the
  feature-gate preflight.
- Docs-only or typo-only: leave `NONE`. Add one line under
  `Useful notes for your reviewer`: `No behavior change (docs/typo).`
  A change to an error string, metric, event, log line, or CLI output is
  user-visible.
- Cherry-pick: copy the source PR release note when it is already one
  user-facing sentence. When the patch matches the source, add one Useful
  notes line that points at the source PR and its tests. Run the three checks
  below only for behavior or tests this cherry-pick itself changed. When the
  source fence is empty, `NONE`, or a commit-title restatement and the change
  is user-visible, replace the fence.

#### Release-note content

When the `release-note` fence is empty, is `NONE` on a user-visible behavior
change, or restates the commit title, replace it with one user-facing
sentence. Call [kueue-release-notes](../kueue-release-notes/SKILL.md) for
wording, the narrow prefix, and the `ACTION REQUIRED:` shape. Do not run
`/release-note-edit`. Put one sentence in the fence. Do not paste the
Detailed, Concise, Balanced, or Rationale variants into the PR body. Use
`ACTION REQUIRED:` when admission or validation can reject an existing
object. Leave `NONE` for docs-only, test-only, and refactor-only diffs.

#### Test intent in Useful notes

Under `Useful notes for your reviewer`, add one row per new or changed
behavior. Columns: behavior, level (`unit` / `integration` / `e2e`), command
and result, not run and why. Use the lowest level that can prove the behavior
([Writing tests](../../../../site/content/en/community/contribution_guidelines/writing_tests.md)).
A bug fix includes a case that fails without the fix. When a gate changes the
result, add a gate-off row. A new or changed test must be selected by a
presubmit before the PR is opened
([tests-run-in-ci](../reviewer/tests-run-in-ci/SKILL.md)). When it is not
selected, add it to an existing target or update the filter. A row that only
says `not selected` is not done. Do not invent `pull-kueue-*` names. When
the job name is not in this repo, name the check that failed and still fix
selection before opening. When an e2e covers the behavior only with an alpha
gate enabled, say so. `make verify` is required by
[kueue-verify](../kueue-verify/SKILL.md) and is not a row.

#### Feature-gate preflight

When the diff changes a gate constant or a `defaultVersionedFeatureGates`
entry in `pkg/features/kube_features.go`, or product code adds or changes a
`features.Enabled(...)` branch, add these five lines to Useful notes:

```text
Gate: <name>
Default: <copied from the spec>
PreRelease: <copied from the spec>
Version: <copied from the spec>
Off-path test: <test or none>
```

Copy `Default`, `PreRelease`, and `Version` from the spec entry this change
adds or edits. When the slice has more than one entry, the highest `Version`
is the current stage. When the design did not decide `Default`, write
`Default: needs human judgment`. Do not choose `true` or `false`. Do not add
a gate the change does not already have. Run `make generate-featuregates`
only when a spec literal changed, and include the generated files in the
diff. Do not run it for a comment-only edit or a branch on an existing gate.
A gate that
changes the result needs an off-path test. `none` there means do not open,
unless this diff does not change the off-path result.

#### Refuse to open

Do not run `gh pr create` or `gh pr edit` on the body while any of these remain:

- A user-visible change still has an empty fence, `NONE`, or a title
  restatement.
- Admission or validation can reject an existing object and the fence has no
  `ACTION REQUIRED:`.
- A changed behavior has no Useful notes row, or the only evidence is
  `make verify`.
- A bug fix has no test that fails without the fix.
- A new or changed test is not selected by a presubmit.
- A gate changes the result and the off-path test is missing.
- Feature-gate preflight applies, but its five lines are missing or
  `Default`, `PreRelease`, or `Version` was not copied from the spec.
- A spec literal changed and `make generate-featuregates` was not run, or its
  output is still uncommitted.
- A commit message contains an auto-close keyword or a `#` mention. Put issue
  links in `What this PR does / why we need it?`.

`Default: needs human judgment` is a finished line. Open with it.

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
