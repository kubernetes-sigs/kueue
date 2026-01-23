# Contributing Guidelines

Welcome to Kubernetes. We are excited about the prospect of you joining our [community](https://git.k8s.io/community)! The Kubernetes community abides by the CNCF [code of conduct](code-of-conduct.md). Here is an excerpt:

_As contributors and maintainers of this project, and in the interest of fostering an open and welcoming community, we pledge to respect all people who contribute through reporting issues, posting feature requests, updating documentation, submitting pull requests or patches, and other activities._

## Getting Started

We have full documentation on how to get started contributing here:

<!---
If your repo has certain guidelines for contribution, put them here ahead of the general k8s resources
-->

- [Contributor License Agreement](https://git.k8s.io/community/CLA.md) - Kubernetes projects require that you sign a Contributor License Agreement (CLA) before we can accept your pull requests
- [Kubernetes Contributor Guide](https://k8s.dev/guide) - Main contributor documentation, or you can just jump directly to the [contributing page](https://k8s.dev/docs/guide/contributing/)
- [Contributor Cheat Sheet](https://k8s.dev/cheatsheet) - Common resources for existing developers
- [SIG Scheduling Contributor's Guide](https://git.k8s.io/community/sig-scheduling/CONTRIBUTING.md) - Guidelines specific to SIG Scheduling.

## Mentorship

- [Mentoring Initiatives](https://k8s.dev/community/mentoring) - We have a diverse set of mentorship programs available that are always looking for volunteers!

## Contact Information

- [Slack](https://kubernetes.slack.com/messages/sig-scheduling)
- [Mailing List](https://groups.google.com/forum/#!forum/kubernetes-sig-scheduling)

## Feature gate documentation workflow

Kueue treats `pkg/features/kube_features.go` as the source of truth for every feature gate. After you change that file, run:

```shell
make generate-featuregates
```

This command builds (and caches) Kubernetes' `compatibility_lifecycle` tool from a pinned Kubernetes git ref derived from the `k8s.io/code-generator` version in `hack/internal/tools/go.mod` (Dependabot-managed), then uses it to regenerate `test/compatibility_lifecycle/reference/versioned_feature_list.yaml` and copies the result to `site/data/featuregates/versioned_feature_list.yaml`. The docs under `site/content/*/docs/installation/_index.md` consume that YAML through the `feature-gates-table` shortcode, so no manual table edits are required.

CI enforces that the YAML and documentation stay in sync. Or simply rely on `make verify`, which now calls the verification target automatically.
