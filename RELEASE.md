# Release process

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

## Release milestones

Each minor release normally uses an eight-week cycle. The public release is planned for the middle of W8.

| Milestone | Normal timing | Meaning |
| --- | --- | --- |
| **D1 — Planning opens** | W1–W2 | Scope is preferably discussed during a WG Batch meeting or a dedicated planning meeting, but may also be discussed asynchronously in the issue. |
| **D2 — Larger KEP review** | W1–W4 | Larger KEPs should become visible early and discussed. |
| **D3 — Scope cooldown** | W5 | Maintainers finalize the scope for the release in terms of larger KEPs. Small KEPs or KEP updates remain eligible until D4. |
| **D4 — KEP publication** | W6 | Publish accepted KEPs for the release. |
| **C1 — Code cooldown** | W6 | All big feature PRs must be open, substantially complete, and reviewable. |
| **C2 — Code freeze** | W7 | Feature PRs are merged or deferred. Focus on bug fixes or explicitly approved exceptions. |
| **R — Public release** | W8 | Release is published. |

### Tentative timelines

You can find the tentative release timelines in the  [`release-timelines`](./release-timelines) directory.

## Exceptions

Exceptions are possible, and should be discussed between maintainers and feature owners.
A dedicated Slack group will be open per exception request. The decision is recorded in the feature tracking issue.

## Clarifications

Maintainers provide judgment on:
- Scope determination for features depending on their review capacity.
- Approval of individual feature PRs.
- Potential update on the tentative release schedule.
- Exception approvals.

The release publication process is executed by the release team.

The release publication process is tracked by opening an [issue](https://github.com/kubernetes-sigs/kueue/issues/new/choose)
using the **New Release** template.

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
releases are regularly tested by CI.

Patch releases are published as needed, generally targeting a weekly cadence.

We follow the Kubernetes cherry-pick [principles](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-release/cherry-picks.md#what-kind-of-prs-are-good-for-cherry-picks), but the choice of cherry-picks
is more relaxed, e.g. we allow to cherry-pick minor improvements for [alpha Kueue features](https://kueue.sigs.k8s.io/docs/installation/#change-the-feature-gates-configuration).
