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

The docs site at [kueue.sigs.k8s.io](https://kueue.sigs.k8s.io) publishes from the `website`
branch, which tracks `main` (doc PRs are cherry-picked to `website`, and `main` is merged into
`website` on each minor release). Docs are versioned **by path in that single deploy** (the same
model [Karpenter](https://karpenter.sh) uses): the development docs live at
`site/content/<locale>/docs` (served at `/docs/`), and each release is a frozen copy at
`site/content/<locale>/v0.X/docs` (served at `/v0.X/docs/`). A version-switcher dropdown in the
header navigates between them. There are no per-version subdomains, DNS records, or Netlify
aliases.

### Retention policy

The dropdown keeps `main` plus the two most recent releases (current and N-1); `main` tracks the
upcoming release and is served at the site root. When a new minor ships, the oldest release is
dropped from the dropdown and its snapshot directories are pruned. (`archived_version` is a
site-wide Docsy flag, so it can't mark an individual path-based version; the dropdown is the
version affordance.)

### Automated steps

When `prepare_pull.sh` runs for the newest minor release (e.g. `v0.20`), the `main`-update PR
automatically runs [`hack/releasing/snapshot-docs.py`](hack/releasing/snapshot-docs.py), which:

- Copies each locale's `docs/` from the `release-0.20` branch (the source of truth for that
  release's docs) into `site/content/<locale>/v0.20/docs`.
- Rewrites internal `/docs/` links in the copy to `/v0.20/docs/` so it navigates within itself.
- Prunes snapshot directories beyond the retention window.
- Prepends a `[[params.versions]]` entry (`v0.20 → /v0.20/docs/`) to `site/hugo.toml` and drops
  the oldest.

The snapshot lands on `main` and reaches the live site on the next `main` -> `website` merge.
No manual infrastructure steps are required.

## Patch releases

When working on the next N-th minor version of Kueue we continue to maintain
N-1 and N-2 releases. The release branches corresponding to the next patch
releases are regularly tested by CI. Patch releases are released on as-needed
basis.

We follow the Kubernetes cherry-pick [principles](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-release/cherry-picks.md#what-kind-of-prs-are-good-for-cherry-picks), but the choice of cherry-picks
is more relaxed, e.g. we allow to cherry-pick minor improvements for [alpha Kueue features](https://kueue.sigs.k8s.io/docs/installation/#change-the-feature-gates-configuration).
