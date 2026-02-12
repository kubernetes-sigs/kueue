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

## Local verification (`make verify`)

`make verify` is the closest equivalent to “run what CI will enforce”. It is designed to catch missing generated artifacts (CRDs, docs, mocks, etc.) and formatting/lint issues **before** you push a PR.

- **What it does (high level)**:
  - **Regenerates** checked-in artifacts (codegen/mocks, docs site data, Helm-related outputs).
  - **Runs checks** (Go linters, Go formatting verification, shellcheck, TOC verification, Helm rendering + unit tests, frontend dependency checks).
  - **Asserts git cleanliness** for the main “generated/output” paths (see `PATHS_TO_VERIFY` in `Makefile-verify.mk`).

- **Requirements / notes**:
  - **Go**: uses the Go version declared in `go.mod`.
  - **Docker**: required for `shell-lint` (ShellCheck runs in a container) and `npm-depcheck` (runs in a container).
  - **Parallelism**: override with `VERIFY_NPROCS=<n> make verify` if you want to limit CPU usage.

- **Useful subsets**:
  - **Just regenerate everything**: `make verify-tree-prereqs`
  - **Just run checks**: `make verify-checks`
  - **Common single checks**: `make ci-lint`, `make fmt-verify`, `make helm-verify`, `make helm-unit-test`, `make toc-verify`, `make shell-lint`, `make npm-depcheck`

### Adding a new step to `make verify`

The `verify` pipeline is defined in `Makefile-verify.mk` and intentionally split into:

- **`verify-tree-prereqs`**: steps that **may update files** that are checked into git (generators, docs, Helm docs/manifests, etc.).
- **`verify-checks`**: steps that should be **read-only** (linters, formatting verification, helm rendering/unit tests, dependency checks, etc.).

To add a new verify step:

- **Decide which phase it belongs to**:
  - If your step **generates/updates tracked output** → add it to `verify-go-prereqs`, `verify-docs-prereqs`, or `verify-helm-prereqs` (which all roll up into `verify-tree-prereqs`).
  - If your step **only validates** and should not modify files → add it to `verify-checks`.

- **Pick where to implement the target**:
  - If it’s verify-specific, define it in `Makefile-verify.mk`.
  - If it belongs to another area (tests, tooling, etc.), define it in the relevant included fragment (for example `Makefile-test.mk`) and just reference it from `Makefile-verify.mk`.

- **Wire it into the aggregator**:
  - Add your target name as a dependency of the appropriate aggregator (`verify-*-prereqs` or `verify-checks`).
  - Prefer small, single-purpose targets so developers can run them directly (e.g. `make my-new-check`).

- **Make it self-documenting**:
  - Add a `##` help description on the target line so it appears in `make help`.
  - Use `.PHONY` for non-file targets.

## Feature gate documentation workflow

Kueue treats `pkg/features/kube_features.go` as the source of truth for every feature gate. After you change that file, run:

```shell
make generate-featuregates
```

This command builds (and caches) Kubernetes' `compatibility_lifecycle` tool from a pinned Kubernetes git ref derived from the `k8s.io/code-generator` version in `hack/tools/go.mod` (Dependabot-managed), then uses it to regenerate `test/compatibility_lifecycle/reference/versioned_feature_list.yaml` and copies the result to `site/data/featuregates/versioned_feature_list.yaml`. The docs under `site/content/*/docs/installation/_index.md` consume that YAML through the `feature-gates-table` shortcode, so no manual table edits are required.

CI enforces that the YAML and documentation stay in sync. Or simply rely on `make verify`, which now calls the verification target automatically.
