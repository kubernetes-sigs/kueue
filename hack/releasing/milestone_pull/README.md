# `milestone_pull.sh`

Script: [`../milestone_pull.sh`](../milestone_pull.sh) · ChatOps: `/milestone-pull` · Tests:
[`../../testing/milestone_pull_test.sh`](../../testing/milestone_pull_test.sh)

Performs the "prepare the repo for the next version" milestone item of the release checklist
in [`NEW_RELEASE.md`](../../../.github/ISSUE_TEMPLATE/NEW_RELEASE.md). Applies to **major and
minor** releases only; patch releases have no milestone step and are refused.

It does two things:

1. **Milestone** — ensures an open `v0.X+1` milestone exists in `kubernetes-sigs/kueue`.
2. **Mapping pull request** — opens a PR against `kubernetes/test-infra` updating prow's
   `milestone_applier` config so new PRs get the right milestone automatically.

For release `v0.20.0` the PR is a two-line diff in `config/prow/plugins.yaml`:

```diff
   kubernetes-sigs/kueue:
-    main: v0.20
+    main: v0.21
+    release-0.20: v0.20
     release-0.19: v0.19
```

Reference: [kubernetes/test-infra#37139](https://github.com/kubernetes/test-infra/pull/37139).

For the release process as a whole see [`RELEASE.md`](../../../RELEASE.md); the ChatOps commands
are defined in [`release-utils.yml`](../../../.github/workflows/release-utils.yml).

---

## Why two phases

The halves write to **different repositories and need different credentials**, so they are
separately skippable and separately reported.

| Phase | Writes to | Credential in CI | Available today |
|---|---|---|---|
| Milestone | `kubernetes-sigs/kueue` | `secrets.GITHUB_TOKEN` (`issues: write`) | **yes** |
| Mapping PR | `kubernetes/test-infra` | `secrets.KUEUE_RELEASE_BOT_TOKEN` | not yet |

Until `KUEUE_RELEASE_BOT_TOKEN` is provisioned, `/milestone-pull` creates the milestone,
reports that the PR half is not enabled, and **still succeeds**. Open the PR locally in the
meantime with `SKIP_MILESTONE=1`. When the secret lands, the same command does both with no
change to the script or the workflow.

## Usage

### Manually, via the script

This is the path the release checklist points at, and it does both halves today. Requires a
`kubernetes/test-infra` clone with an `upstream` remote and your fork, plus an authenticated
`gh`:

```bash
GITHUB_USER=<your-gh-user> ./hack/releasing/milestone_pull.sh v0.20.0
```

Inspect before submitting — does everything except create the milestone, push, and open the PR,
leaving the branch and commit in place:

```bash
DRY_RUN=1 GITHUB_USER=<your-gh-user> ./hack/releasing/milestone_pull.sh v0.20.0
```

Then confirm the diff is the two lines and nothing else:

```bash
git -C ../../kubernetes/test-infra diff --stat HEAD~1
```

Expect `config/prow/plugins.yaml | 3 ++-`. Anything else is a bug, not a surprise to accept.

### Automatically, via the ChatOps command

Commenting on the release issue runs the same script:

```
/milestone-pull
```

The milestone half works today. The pull request half stays disabled until
`KUEUE_RELEASE_BOT_TOKEN` is configured, so the checklist still points at the script above; once
the secret lands, this command covers both and the checklist can switch to it.

## Environment

| Variable | Default | Effect |
|---|---|---|
| `GITHUB_USER` | *(required for the PR phase)* | Owner of the `kubernetes/test-infra` fork to push to |
| `GH_TOKEN` | `gh auth` session | Credential for `gh` |
| `DRY_RUN` | unset | Skip the milestone creation, the push, and the PR |
| `SKIP_MILESTONE` | unset | Skip phase 1 |
| `SKIP_PR` | unset | Skip phase 2 |
| `KUBERNETES_REPOS_PATH` | `../../kubernetes` | Where Kubernetes org clones live |
| `KUBERNETES_TEST_INFRA_PATH` | `$KUBERNETES_REPOS_PATH/test-infra` | The `test-infra` clone |
| `KUBERNETES_TEST_INFRA_UPSTREAM_REMOTE` | `upstream` | Remote to branch from |
| `KUBERNETES_TEST_INFRA_FORK_REMOTE` | `origin` | Remote to push to |

Names and defaults match [`ci_pull.sh`](../ci_pull.sh), so an environment that works for that
works here.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Both requested phases reached a good terminal state — created, opened, already present, already applied, already open, or skipped |
| `1` | A requested phase failed |
| `2` | Usage error or a precondition refusal — bad version, patch release, missing clone, dirty tree, missing release branch, no release issue, unexpected upstream state |

Exit `0` does not on its own mean a pull request exists. Read the summary the script always
prints:

```text
+++ Summary for v0.20.0
    milestone v0.21 ......... created
    mapping pull request .... https://github.com/kubernetes/test-infra/pull/NNNNN
```

## Safety properties

- **Idempotent.** Rerunning after success is a no-op that reports the existing state. It never
  bumps `main` twice, never opens a second PR, and never duplicates the milestone.
- **Scoped edit.** `plugins.yaml` contains **four** blocks keyed `kubernetes-sigs/kueue:` —
  under `milestone_applier`, `repo_milestone`, `plugins`, and `external_plugins`. The edit
  tracks the enclosing section so only the first is touched.
- **No reformatting.** The edit is a line-oriented `awk` pass, not a YAML round-trip, so every
  other byte of this org-wide shared file is unchanged. That is what keeps the diff reviewable.
- **Refuses rather than guesses.** An unrecognised upstream shape, or a `main` value that does
  not match the version being released, is an error naming the file and section — not a
  best-effort edit.
- **Existing milestones are never modified.** Not retitled, not reopened, not closed.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `is a patch release ... Nothing to do.` | `PATCH != 0` | Correct — the step is major/minor only |
| `Unexpected mapping state: found main: v0.19, expected v0.20` | Stale `test-infra` clone, or wrong version passed | `git fetch upstream` in the clone; a rerun for an applied version reports "already applied" instead |
| `Could not locate milestone_applier -> kubernetes-sigs/kueue -> main` | Upstream restructured `plugins.yaml` | Inspect the file; the edit deliberately refuses rather than guessing |
| `Dirty tree in <path>` | Uncommitted changes in the `test-infra` clone | Stash or commit them |
| `already present (closed, left as-is)` | A closed milestone has that title | Intentional — reopen by hand if that is what you want |
| `No release issue found` | No **open** `Release vX.Y.Z` issue | The step runs while the release issue is still open |
| Command comment gets no response | Actor not in `OWNERS_ALIASES`, or malformed issue title | Check `release-team` / `kueue-approvers` membership and the `Release vX.Y.Z` title |

## Tests

```bash
make milestone-pull-test
```

The unit tests stub `gh` on `PATH` and drive the script against fixtures, so they need no
network and no credentials. They run in CI via `verify-checks`. The central case builds a
fixture containing all four `kubernetes-sigs/kueue:` blocks and asserts the output differs by
exactly the two intended lines.

Shell lint (needs a container engine):

```bash
make shell-lint
```

The ChatOps path cannot be tested before merge: `issue_comment` workflows run from the default
branch, so a PR branch's version of the job never fires.
