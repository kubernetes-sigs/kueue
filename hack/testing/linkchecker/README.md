# Website link checker

Kueue documentation is published at [kueue.sigs.k8s.io](https://kueue.sigs.k8s.io/) and built by Netlify. This directory contains scripts that crawl the site and report broken links.

## Targets

| Make target | Script | URL checked |
|-------------|--------|-------------|
| `make verify-website-links` | `verify.sh` | Production site (`https://kueue.sigs.k8s.io/` by default) |
| `make verify-website-links-preview` | `verify-preview.sh` | PR Netlify deploy preview (Prow presubmit only) |

Set `LINK_CHECK_URL` to override the crawl target when running `verify.sh` directly.

## Local development

Check the live site (same as the daily periodic job):

```bash
make verify-website-links
```

Check a specific Netlify deploy preview:

```bash
LINK_CHECK_URL="https://deploy-preview-12345--kubernetes-sigs-kueue.netlify.app/" \
  hack/testing/linkchecker/verify.sh
```

Requirements: Docker (or Podman), network access. The checker runs inside a small container image defined by `Dockerfile`.

## Pull request workflow

Website changes under `site/` or `netlify.toml` trigger a Netlify deploy preview. After the `deploy/netlify` commit status succeeds on your PR head commit, run:

```text
/test pull-kueue-verify-website-links-preview-main
```

The presubmit is **optional and non-blocking**. It waits for a fresh same-commit Netlify preview, then runs the same link checker against:

```text
https://deploy-preview-<PR>--kubernetes-sigs-kueue.netlify.app/
```

The job skips (and passes) when:

- the PR does not modify `site/`
- Netlify did not publish a preview
- the preview is not ready within the bounded wait
- GitHub API or tooling is unavailable

Broken links on a resolved preview **fail** the job.

## CI integration

| Job | Type | Dashboard |
|-----|------|-----------|
| `periodic-kueue-verify-website-links-main` | Daily periodic | [TestGrid](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-verify-website-links-main) |
| `pull-kueue-verify-website-links-preview-main` | Presubmit (auto-runs on `site/` changes, optional) | [TestGrid](https://testgrid.k8s.io/sig-scheduling#pull-kueue-verify-website-links-preview-main) |

Prow job definitions live in [kubernetes/test-infra](https://github.com/kubernetes/test-infra/tree/master/config/jobs/kubernetes-sigs/kueue).

## Environment variables

| Variable | Used by | Purpose |
|----------|---------|---------|
| `LINK_CHECK_URL` | `verify.sh` | Base URL to crawl |
| `PULL_NUMBER` | `verify-preview.sh` | PR number (set by Prow) |
| `PULL_PULL_SHA` | `verify-preview.sh` | Head commit SHA (set by Prow) |
| `PULL_BASE_SHA` | `verify-preview.sh` | Merge base SHA for `site/` diff detection |
| `GH_TOKEN` | `verify-preview.sh` | Optional GitHub API token (raises rate limits) |
| `PREVIEW_WAIT_ATTEMPTS` | `verify-preview.sh` | Poll attempts for `deploy/netlify` (default: 20) |
| `PREVIEW_WAIT_DELAY` | `verify-preview.sh` | Seconds between polls (default: 30) |
