# Release process

To begin the process, open an [issue](https://github.com/kubernetes-sigs/kueue/issues/new/choose)
using the **New Release** template.

## Release cycle

- Kueue aims for one minor release every two months, which allows to align with
  the Kubernetes release cycle every second minor release of Kueue.
- The "aligned" minor release of Kueue is around a month after the release of
  Kubernetes to compile Kueue against that release when the first patch
  release of Kubernetes is already available.
- The release cadence is not rigid and we allow a release may slip, for example,
  due to waiting for an important feature or bug-fixes. However, we strive it
  does not slip more than two weeks.
- When a release slips it does not impact the target release date for the next
  minor release.

## Versioned docs

The docs site at [kueue.sigs.k8s.io](https://kueue.sigs.k8s.io) serves the `main` branch. Each
release branch gets its own subdomain (e.g. `v0-20.kueue.sigs.k8s.io`) via a Netlify branch
deploy, and a version switcher dropdown in the site header lets users navigate between versions.

### Retention policy

Kueue maintains N-2 releases in active support. One additional release (N-3) is kept in the
docs dropdown for read-only access. Older versions (N-4 and earlier) are dropped from the
dropdown and their Netlify branch deploys are disabled.

### Automated steps (handled by `prepare-release-branch`)

When `prepare_pull.sh` runs for a new minor release (e.g. `v0.20`), the
`prepare-release-branch` Makefile target automatically updates `site/hugo.toml`:

- **On the release branch** (`release-0.20`):
  - Sets `archived_version = true` (shows the "archived version" banner in the site header)
  - Sets the top-level `githubbranch` to `"release-0.20"`
  - Prepends a `[[params.versions]]` entry: `v0.20 → https://v0-20.kueue.sigs.k8s.io`

- **On `main`**:
  - Prepends the same `[[params.versions]]` entry for `v0.20`
  - Drops the oldest release entry from the dropdown when the total exceeds four release versions

### Manual steps

The following steps require access to external systems and are tracked in the release checklist
([`.github/ISSUE_TEMPLATE/NEW_RELEASE.md`](.github/ISSUE_TEMPLATE/NEW_RELEASE.md)):

1. **Netlify domain alias** — In the Netlify dashboard → Domains, add
   `v0-20.kueue.sigs.k8s.io` as a domain alias for the `release-0.20` branch deploy.
   The CNAME target will be `release-0-20--<site-name>.netlify.app`.

2. **DNS CNAME** — File a PR at [kubernetes/k8s.io](https://github.com/kubernetes/k8s.io):
   ```text
   v0-20.kueue.sigs.k8s.io  CNAME  release-0-20--<site-name>.netlify.app
   ```

3. **Active release branches** — The `prepare-release-branch` script updates only the new
   release branch and `main`. Send PRs to the active N-1 and N-2 release branches to add
   the new version to their dropdowns as well.

4. **Drop procedure** — When the oldest release version is removed from the dropdown (N-4),
   disable or archive its Netlify branch deploy to avoid serving stale content indefinitely.

## Patch releases

When working on the next N-th minor version of Kueue we continue to maintain
N-1 and N-2 releases. The release branches corresponding to the next patch
releases are regularly tested by CI. Patch releases are released on as-needed
basis.

We follow the Kubernetes cherry-pick [principles](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-release/cherry-picks.md#what-kind-of-prs-are-good-for-cherry-picks), but the choice of cherry-picks
is more relaxed, e.g. we allow to cherry-pick minor improvements for [alpha Kueue features](https://kueue.sigs.k8s.io/docs/installation/#change-the-feature-gates-configuration).
