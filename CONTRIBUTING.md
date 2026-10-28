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
- [AI Tool Usage Policy](https://www.kubernetes.dev/docs/guide/pull-requests/#ai-guidance) - 🤖 Guidelines for using AI tools when contributing.
- [SIG Scheduling Contributor's Guide](https://git.k8s.io/community/sig-scheduling/CONTRIBUTING.md) - Guidelines specific to SIG Scheduling.
- [Coding Guidelines](https://kueue.sigs.k8s.io/community/contribution_guidelines/coding_guidelines/) - Coding conventions and patterns for Kueue product and test code.
- [Writing tests](https://kueue.sigs.k8s.io/community/contribution_guidelines/writing_tests/) - Coverage, test levels, and test design.

## Mentorship

- [Mentoring Initiatives](https://k8s.dev/community/mentoring) - We have a diverse set of mentorship programs available that are always looking for volunteers!

## Contact Information

- [Slack](https://kubernetes.slack.com/messages/sig-scheduling)
- [Mailing List](https://groups.google.com/a/kubernetes.io/g/sig-scheduling)

## Local verification (`make verify`)

`make verify` is the closest equivalent to “run what CI will enforce”. It is designed to catch missing generated artifacts (CRDs, docs, mocks, etc.) and formatting/lint issues **before** you push a PR.

- **What it does (high level)**:
  - **Regenerates** checked-in artifacts (codegen/mocks, docs site data, Helm-related outputs).
  - **Runs checks** (Go linters, Go formatting verification, shellcheck, TOC verification, Helm rendering + unit tests, frontend dependency checks).
  - **Asserts git cleanliness** for the main “generated/output” paths (see `PATHS_TO_VERIFY` in `hack/make/verify.mk`).

- **Requirements / notes**:
  - **Go**: uses the Go version declared in `go.mod`.
  - **Docker**: required for `shell-lint` (ShellCheck runs in a container) and `npm-depcheck` (runs in a container).
  - **Parallelism**: override with `VERIFY_NPROCS=<n> make verify` if you want to limit CPU usage.

- **Useful subsets**:
  - **Just regenerate everything**: `make verify-tree-prereqs`
  - **Just run checks**: `make verify-checks`
  - **Common single checks**: `make ci-lint`, `make fmt-verify`, `make helm-verify`, `make helm-unit-test`, `make toc-verify`, `make shell-lint`, `make npm-depcheck`


### Verification for local changes

Use change-aware verification for local iteration:

```shell
# Inspect the plan without generating files or running checks.
make verify-plan VERIFY_BASE_REF=upstream/main

# Run the selected generators, followed by the related checks.
make verify-changed VERIFY_BASE_REF=upstream/main

# For a PR targeting a release branch, compare against that branch instead.
make verify-changed VERIFY_BASE_REF=upstream/release-0.19
```

If `VERIFY_BASE_REF` is omitted, the selector uses `upstream/main`, then
`origin/main`. It compares the PR diff from the merge base and also includes
staged, unstaged, deleted, renamed, and non-ignored untracked files. It does not
use the feature branch's tracking ref, so pushing a branch does not hide its
committed changes.

| Changed inputs | Selected work |
| --- | --- |
| Skills or agent instructions | skillsaw |
| Markdown or site content | Hugo version synchronization and KEP TOCs |
| Ordinary Go source or tests | Go lint and formatting |
| API source | API/code/manifest/doc generation and related checks |
| CLI source or a production Go dependency | CLI docs and cross-platform CLI build |
| RBAC/webhook source, Helm charts or deployment configuration | Helm/manifests generation and checks |
| Shell scripts | ShellCheck, plus helper tests for testing scripts |
| Release utilities | Release utility tests |
| Frontend dependency inputs | npm dependency checks |
| Makefiles, generator tooling, module/toolchain inputs, or unclassified inputs | Full verification checks |

CLI dependency discovery uses every platform in `CLI_PLATFORMS`. If Go cannot
resolve the dependency graph, the selector includes the CLI build conservatively.
A test-only change does not by itself trigger a CLI build. `LICENSE` and the CLI
build script also trigger packaging. This selection is a local optimization; CI's
`make verify` continues to run every check and build all default CLI platforms.

The scoped driver allows existing uncommitted edits. It compares generated/output
files before and after execution, and fails if generation updates them. Review
those changes, include newly generated files in the PR, and rerun. An invalid base
ref or a failed command fails the run; it is never treated as "nothing changed".

The following scopes can also be run explicitly:

| Command | Scope |
| --- | --- |
| `make verify-go` | Go lint and formatting, without unrelated generation |
| `make verify-api` | API code, docs and CRD generation, then Go/API checks |
| `make verify-cli` | CLI binaries and archives for `CLI_PLATFORMS` |
| `make verify-manifests` | Helm and manifest artifacts, without CLI compilation |
| `make verify-helm` | Helm rendering and chart unit tests |
| `make verify-docs` | Hugo version synchronization and KEP TOCs |
| `make verify-scripts` | ShellCheck and shell helper tests |

Run relevant application tests separately. Neither `verify-changed` nor the full
`verify` target replaces the Go unit, integration, or cluster E2E test suites.

### Adding a new step to `make verify`

The `verify` pipeline is defined in `hack/make/verify.mk` and intentionally split into:

- **`verify-tree-prereqs`**: steps that **may update files** that are checked into git (generators, docs, Helm docs/manifests, etc.).
- **`verify-checks`**: steps that should be **read-only** (linters, formatting verification, helm rendering/unit tests, dependency checks, etc.).

To add a new verify step:

- **Decide which phase it belongs to**:
  - If your step **generates/updates tracked output** → add it to `verify-go-prereqs`, `verify-docs-prereqs`, or `verify-helm-prereqs` (which all roll up into `verify-tree-prereqs`).
  - If your step **only validates** and should not modify files → add it to `verify-checks`.

- **Pick where to implement the target**:
  - If it’s verify-specific, define it in `hack/make/verify.mk`.
  - If it belongs to another area (tests, tooling, etc.), define it in the relevant included fragment (for example `hack/make/test.mk`) and just reference it from `hack/make/verify.mk`.

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
