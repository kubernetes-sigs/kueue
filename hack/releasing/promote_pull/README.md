# `promote_pull.sh`

Script: [`../promote_pull.sh`](../promote_pull.sh) · ChatOps: `/promote-pull`

Performs the "Promote images and Helm Charts to production" step of the release checklist in
[`NEW_RELEASE.md`](../../../.github/ISSUE_TEMPLATE/NEW_RELEASE.md). It moves a release's
container images and Helm charts from the staging registry to production `registry.k8s.io` by
adding their digests to Kueue's image manifest in `kubernetes/k8s.io` and opening a pull
request there.

For release `v0.19.0` the PR adds one line per released artifact to
`registry.k8s.io/images/k8s-staging-kueue/images.yaml`:

```diff
 - name: kueue
   dmap:
+    "sha256:6fe2cbe4...": ["v0.19.0"]
     "sha256:eb37cb0c...": ["v0.18.4"]
```

The command **submits** the PR. Review and merge stay with `kubernetes/k8s.io` approvers.

For the release process as a whole see [`RELEASE.md`](../../../RELEASE.md); the ChatOps
commands are defined in [`release-utils.yml`](../../../.github/workflows/release-utils.yml).

---

## Repository configuration

Promotion writes to **another repository**, so `secrets.GITHUB_TOKEN` cannot do it — that token
is scoped to `kubernetes-sigs/kueue` and can neither push to a `kubernetes/k8s.io` fork nor open
a pull request there. Two settings must exist before `/promote-pull` can succeed:

| Setting | Kind | Value |
|---|---|---|
| `KUEUE_RELEASE_BOT_TOKEN` | repository **secret** | A token belonging to the bot account |
| `KUEUE_RELEASE_BOT_USER` | repository **variable** | That account's GitHub login |

### Step by step

1. **Choose the account that will own the fork.** It must be a real GitHub account — the login
   is used as the pull request head owner. A dedicated bot account is recommended so the
   capability does not depend on any individual maintainer.

2. **Fork `kubernetes/k8s.io` as that account.** Nothing in the automation creates the fork;
   it only pushes to one that already exists.

   ```bash
   gh repo fork kubernetes/k8s.io --clone=false
   ```

3. **Create a token for that account.** It needs write access to the fork only — not to
   `kubernetes/k8s.io`, and not to `kubernetes-sigs/kueue`.

   - Fine-grained PAT (preferred): repository access limited to `<owner>/k8s.io`, with
     **Contents: Read and write** and **Pull requests: Read and write**.
   - Classic PAT: `public_repo`.

4. **Add the secret** in `kubernetes-sigs/kueue` → Settings → Secrets and variables → Actions →
   Secrets → New repository secret, named `KUEUE_RELEASE_BOT_TOKEN`.

5. **Add the variable** in the same place under Variables → New repository variable, named
   `KUEUE_RELEASE_BOT_USER`, set to the account login from step 1.

6. **Verify** by commenting `/promote-pull` on a release issue. Before the settings exist the
   command reports exactly which piece is missing, so a partial setup is easy to diagnose:

   | State | Reported |
   |---|---|
   | Neither configured | `missing secret KUEUE_RELEASE_BOT_TOKEN and variable KUEUE_RELEASE_BOT_USER` |
   | Secret only | `missing variable KUEUE_RELEASE_BOT_USER` |
   | Variable only | `missing secret KUEUE_RELEASE_BOT_TOKEN` |
   | Both set, no fork | `missing fork <owner>/k8s.io (create it with gh repo fork kubernetes/k8s.io as that account)` |

### Before the settings exist

The command still runs. It performs the checkout, the remote wiring, the digest lookups and the
manifest insertion under `DRY_RUN`, then stops before the push and reports the missing piece.
That makes an unconfigured `/promote-pull` a useful rehearsal rather than a no-op — a genuine
failure in that path is reported as itself, not masked by the credential message.

## Usage

From the release issue, after `/wait-for-images` confirms the staging artifacts:

```
/promote-pull
```

The command takes no arguments; the version comes from the `Release vX.Y.Z` issue title.
Re-running is safe — see [Safety properties](#safety-properties).

Locally — requires a `kubernetes/k8s.io` clone with an `upstream` remote and your fork as
`origin`, plus an authenticated `gh` and `crane` on `PATH`:

```bash
GITHUB_USER=<your-gh-user> ./hack/releasing/promote_pull.sh v0.19.0
```

Inspect before submitting — does everything except push and open the PR, leaving the branch and
commit in place:

```bash
DRY_RUN=1 GITHUB_USER=<your-gh-user> ./hack/releasing/promote_pull.sh v0.19.0
```

Then confirm the diff touches one file and nothing else:

```bash
git -C ../../kubernetes/k8s.io diff --stat HEAD~1
```

Expect `registry.k8s.io/images/k8s-staging-kueue/images.yaml`. Anything else is a bug.

## Environment

| Variable | Default | Effect |
|---|---|---|
| `GITHUB_USER` | *(required)* | Owner of the `kubernetes/k8s.io` fork to push to, and the PR head owner |
| `GH_TOKEN` | `gh auth` session | Credential for `gh` |
| `DRY_RUN` | unset | Skip the push and the PR |
| `RELEASE_ISSUE_NUMBER` | unset | Use this release issue instead of searching by title |
| `KUBERNETES_REPOS_PATH` | `../../kubernetes` | Where Kubernetes org clones live |
| `KUBERNETES_K8S_IO_PATH` | `$KUBERNETES_REPOS_PATH/k8s.io` | The `k8s.io` clone |
| `KUBERNETES_K8S_IO_UPSTREAM_REMOTE` | `upstream` | Remote to branch from |
| `KUBERNETES_K8S_IO_FORK_REMOTE` | `origin` | Remote to push to |
| `KUBERNETES_K8S_IO_MAIN_REPO_ORG` / `_NAME` | derived from the upstream remote | Repository the PR is opened against |

Names and defaults match [`ci_pull.sh`](../ci_pull.sh) and
[`prepare_pull.sh`](../prepare_pull.sh), so an environment that works for those works here.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | A promotion pull request exists — created, or already open and updated by the push |
| `1` | A precondition failed, a digest could not be resolved or inserted, the push was declined, or the push/PR failed |
| `2` | Usage error — wrong argument count |

Every failure prints a `!!! `-prefixed line. That prefix is the contract the workflow greps to
build the failure message, so new failure paths must use it.

## Safety properties

- **Idempotent.** The remote branch name is stable per version (`kueue-promote-vX.Y.Z`) and the
  push is a force push, so a re-run refreshes the open pull request with current digests. The
  script finds the existing PR and reports it rather than trying to open a second one.
- **No partial promotions.** Every artifact digest is resolved before anything is committed, and
  after insertion each digest is confirmed present in `images.yaml`. A digest that fails to land
  aborts the run before the push. Promoting an incomplete set is worse than promoting nothing.
- **Only the manifest changes.** The edit is a line-oriented `awk` pass, not a YAML round-trip,
  so every other byte of this shared file is untouched — which is what keeps the diff reviewable.
- **Historical release lines are respected.** An artifact introduced after the release line being
  promoted (for example `kueue-priority-booster` on `0.17`) is skipped, not treated as an error.
- **Charts drop the `v`.** Image entries are recorded as `vX.Y.Z`, chart entries as `X.Y.Z`,
  matching the manifest's existing convention.
- **Never merges.** The pull request is submitted only.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `credential ... is not configured (missing ...)` | Setup incomplete | Follow [Repository configuration](#repository-configuration); the message names the missing piece |
| `missing fork <owner>/k8s.io` | The bot account has no fork | `gh repo fork kubernetes/k8s.io --clone=false` as that account |
| `Failed to resolve the digest for "..."` | Staging artifact not published, or a registry/auth error | Run `/wait-for-images` first; the crane error just above says which it was |
| `Failed to insert N digest(s) into images.yaml` | The manifest has an unexpected shape | Inspect it — the script refuses rather than promoting a partial set |
| `Aborted: the push was not confirmed` | The confirmation prompt got no `y` | Locally, answer `y`; in CI the workflow feeds it |
| `!!! Dirty tree. Clean up and try again.` | Uncommitted changes in the `k8s.io` clone | Stash or commit them |
| `crane is not installed` | Missing dependency | Install [crane](https://github.com/google/go-containerregistry/tree/main/cmd/crane); CI installs it via `setup-crane` |
| `No release issue found` | No open `Release vX.Y.Z` issue | Promotion runs while the release issue is still open |
| Command comment gets no response | Actor not in `OWNERS_ALIASES`, or malformed issue title | Check `release-team` / `kueue-approvers` membership and the `Release vX.Y.Z` title |

## Tests

Shell lint (needs a container engine):

```bash
make shell-lint
```

The ChatOps path cannot be tested before merge: `issue_comment` workflows run from the default
branch, so a PR branch's version of the job never fires. Validate changes with a local
`DRY_RUN=1` run, or a full rehearsal against a personal fork by pointing
`KUBERNETES_K8S_IO_MAIN_REPO_ORG`, `KUBERNETES_K8S_IO_UPSTREAM_REMOTE` and
`KUBERNETES_K8S_IO_FORK_REMOTE` at it, so nothing upstream is touched.
