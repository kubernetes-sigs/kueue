# Dependency Lifecycle Policy

This document describes how the Kueue project manages third-party
dependencies — how new dependencies are introduced, how existing ones
are kept current, how security updates are handled, and how
dependencies are removed.

It is referenced from the `dependencies.dependencies-lifecycle` section
of [`SECURITY-INSIGHTS.yaml`](SECURITY-INSIGHTS.yaml) and is intended
to satisfy the corresponding requirement of the
[OpenSSF Security Insights spec](https://github.com/ossf/security-insights-spec).

## Scope

The policy applies to all runtime and build-time dependencies declared
in this repository:

- Go modules tracked in [`go.mod`](go.mod) / `go.sum` and vendored under
  `vendor/`.
- GitHub Actions referenced from workflows under `.github/workflows`.
- Container base images and tooling images used by the Dockerfiles
  under `cmd/` and `hack/`.
- npm packages used by the `kueueviz` frontend, the documentation
  site, and end-to-end test harnesses.

The full list of Go modules that ship with a given release is
published as an SPDX SBOM attached to that release on GitHub. The
location of the SBOM for the current release is recorded in
`SECURITY-INSIGHTS.yaml`.

## Introducing a new dependency

A new dependency is added via a regular pull request that:

1. Updates the relevant manifest (`go.mod`, `package.json`, workflow
   file, Dockerfile, etc.) along with the lock/checksum file and, for
   Go modules, the contents of `vendor/`.
2. Explains in the PR description why the dependency is needed, what
   alternatives were considered, and what license it ships under.
3. Passes the `make verify` checks described in
   [`CONTRIBUTING.md`](CONTRIBUTING.md), which include linting,
   formatting, generated-artifact checks, and (for npm) dependency
   audits.
4. Is reviewed and approved by maintainers listed in the
   [`OWNERS`](OWNERS) file.

PRs that touch dependency manifests are labelled with `dependencies`
and `area/dependency` so they are easy to find in issue search and
review queues.

## Update cadence

Routine updates are driven by [Dependabot](https://github.com/dependabot),
configured in [`.github/dependabot.yml`](.github/dependabot.yml).
Dependabot is also declared as Kueue's SCA tool in
`SECURITY-INSIGHTS.yaml` and runs both in CI and before each release.

The current schedule is:

| Ecosystem        | Interval | Notes |
|------------------|----------|-------|
| `gomod`          | weekly   | `k8s.io/*` modules are grouped together and only patch / security updates are accepted automatically — see "Kubernetes version alignment" below. |
| `github-actions` | daily    | Minor + patch updates are grouped into a single PR (up to 10 open at a time). |
| `docker`         | weekly   | One entry per tracked image directory under `cmd/` and `hack/`. |
| `npm`            | weekly   | Updates are grouped by package family (`@mui/*`, the Vite / Vitest stack, then a catch-all for remaining minor + patch updates). |

All Dependabot PRs are labelled `ok-to-test`, `release-note-none`,
`dependencies`, and `area/dependency`. They are reviewed and merged
on the same schedule and with the same gating as human-authored PRs.

## Kubernetes version alignment

Kueue runs end-to-end tests against the three most recent Kubernetes
minor versions (see the test grids linked from
[`README.md`](README.md)).

To keep upgrades coordinated with that test matrix:

- Major and minor bumps of `k8s.io/*` modules are excluded from
  Dependabot's automatic updates and are instead performed deliberately
  as part of a Kueue release that targets a new Kubernetes minor.
- Patch and security updates of `k8s.io/*` modules are picked up on
  the normal weekly cadence.

The release cadence and version-skew expectations are described in
[`RELEASE.md`](RELEASE.md). In short, Kueue cuts a minor release
approximately every two months — every second minor is aligned to a
fresh Kubernetes minor — and maintains the previous two minors
(N-1 and N-2) for patch releases.

## Security updates

- Vulnerabilities in Kueue or in its dependencies are reported through
  the Kubernetes security process described in
  [`SECURITY.md`](SECURITY.md) (`security@kubernetes.io`).
- Dependabot security advisories produce expedited update PRs that
  are prioritised over routine version bumps.
- A security-relevant dependency update is a candidate for cherry-pick
  to the supported release branches according to the rules in
  `RELEASE.md`.

## Removing a dependency

Dependencies that are no longer used are removed in the same pull
request that removes their last importer. `go mod tidy` (run as part
of `make verify`) is responsible for keeping `go.sum`, `vendor/`, and
related manifests consistent. For non-Go ecosystems, the equivalent
lock files are updated in the same PR.

## Reviewing this policy

This policy is reviewed at least annually, or sooner if the dependency
tooling or release process changes in a material way. The most recent
review is recorded in the `last-reviewed` field of
`SECURITY-INSIGHTS.yaml`.
