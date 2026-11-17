---
title: "Website contributions"
linkTitle: "Website"
weight: 30
description: >
  Build, preview, and verify links on the Kueue documentation site
type: docs
---

The Kueue website ([kueue.sigs.k8s.io](https://kueue.sigs.k8s.io/)) is built with Hugo and published through Netlify from the [`site/`](https://github.com/kubernetes-sigs/kueue/tree/main/site) directory.

## Local preview

From the repository root:

```bash
make site-server
```

See [`site/README.md`](https://github.com/kubernetes-sigs/kueue/blob/main/site/README.md) for Hugo version requirements and Netlify parity notes.

## Netlify deploy previews

Pull requests that change `site/` or `netlify.toml` receive a Netlify deploy preview. The preview URL follows:

```text
https://deploy-preview-<PR>--kubernetes-sigs-kueue.netlify.app/
```

Wait for the `deploy/netlify` GitHub commit status to succeed on your PR head commit before relying on the preview.

## Link verification

Broken documentation links are checked in two ways:

1. **Periodic job** — `periodic-kueue-verify-website-links-main` crawls the live site daily.
2. **Presubmit job** — `pull-kueue-verify-website-links-preview-main` crawls your PR's Netlify preview.

For website PRs, after Netlify succeeds, trigger the presubmit on your pull request:

```text
/test pull-kueue-verify-website-links-preview-main
```

The presubmit is optional and non-blocking while maintainers evaluate reliability. When your PR modifies `site/`, the job is scheduled automatically but still does not block merge.

Locally:

```bash
make verify-website-links
```

For details on scripts, skip conditions, and environment variables, see [`hack/testing/linkchecker/README.md`](https://github.com/kubernetes-sigs/kueue/blob/main/hack/testing/linkchecker/README.md).

## Agent-assisted documentation

Kueue maintains an experimental [`kueue-doc-writing` skill](https://github.com/kubernetes-sigs/kueue/tree/main/cmd/experimental/skills/kueue-doc-writing)
for drafting or reviewing documentation from KEPs, the current implementation,
and Kubernetes documentation guidance. The skill helps choose a page type,
preserve Kueue's personas and navigation, verify examples, and run the relevant
site checks.

Treat generated drafts as proposals: confirm behavior against the code and
tests, review version-specific claims, and follow the
[Kubernetes AI Tool Usage Policy](https://www.kubernetes.dev/docs/guide/pull-requests/#ai-guidance)
before submitting a pull request.
