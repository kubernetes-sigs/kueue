# Environment Dependencies Policy

This document describes how the Kueue project adopts and consumes
third-party dependencies in each of its environments — development,
test / CI, and production (released artifacts).

It is referenced from the `dependencies.env-dependencies-policy` section
of [`SECURITY-INSIGHTS.yaml`](SECURITY-INSIGHTS.yaml) and is intended
to satisfy the corresponding requirement of the
[OpenSSF Security Insights spec](https://github.com/ossf/security-insights-spec).

How dependencies are introduced, updated, and removed over time is
covered separately in the
[Dependency Lifecycle Policy](DEPENDENCY_LIFECYCLE.md). This document
focuses on *where dependencies come from* and *how they are consumed*
in each environment.

## Guiding principles

- **Declared and version-controlled.** Every dependency, in every
  environment, is declared in a manifest that is committed to this
  repository. There are no undeclared, manually installed, or
  environment-specific dependencies.
- **Pinned and reproducible.** Each manifest is accompanied by a
  lock or checksum file (`go.sum` and `vendor/`, `package-lock.json`,
  hash-pinned `requirements.txt`, digest-pinned container images,
  SHA-pinned GitHub Actions) so that the same inputs produce the same
  result regardless of where or when a build runs.
- **One set of manifests for all environments.** Development, CI, and
  release builds all consume the same manifests. The differences
  between environments are limited to *which* dependencies are used
  (for example, test tooling is not shipped in the production image),
  not *which versions* of them.
- **Trusted sources only.** Dependencies are fetched from the public
  Go module proxy and checksum database, the public npm registry,
  PyPI, well-known container registries (`public.ecr.aws`,
  `gcr.io`, `registry.k8s.io`, `docker.io`, `quay.io`, `ghcr.io`),
  and, for test-only operator manifests, the upstream projects'
  GitHub releases. Mirrors and private registries are not used in
  the project's own build and release pipelines.

## Development environment

The development environment is a contributor's workstation running the
`make` targets described in [`CONTRIBUTING.md`](CONTRIBUTING.md).

- **Go modules.** Runtime dependencies of the Kueue binaries are
  declared in [`go.mod`](go.mod), verified against `go.sum`, and
  vendored under `vendor/`. Go builds from the vendored tree, so a
  development build does not require network access to the module
  proxy once the repository is checked out.
- **Build and generation tooling.** Tools such as `golangci-lint`,
  `controller-gen`, `kustomize`, `helm`, `kind`, `setup-envtest`,
  `ginkgo`, `yq`, `hugo`, and `mockgen` are declared as Go tool
  dependencies in [`hack/tools/go.mod`](hack/tools/go.mod). The
  `make` targets in [`hack/make/deps.mk`](hack/make/deps.mk) read the
  pinned version from that file and install the tool into the local
  `bin/` directory, so every contributor and every CI job uses the
  same tool version.
- **Container builds.** The Go builder image and the distroless base
  image used for local image builds are pinned by digest in
  [`hack/images/golang/Dockerfile`](hack/images/golang/Dockerfile) and
  [`hack/images/distroless/Dockerfile`](hack/images/distroless/Dockerfile).
  The top-level [`Makefile`](Makefile) reads those values and passes
  them to the Dockerfiles as build arguments.
- **npm packages.** The `kueueviz` frontend
  (`cmd/kueueviz/frontend`), the documentation site (`site`), and the
  kueueviz end-to-end tests (`test/e2e/kueueviz`) each declare their
  dependencies in a `package.json` with a committed
  `package-lock.json`.
- **Python packages.** The release automation scripts under
  [`hack/releasing`](hack/releasing) declare their dependencies in a
  hash-pinned [`requirements.txt`](hack/releasing/requirements.txt)
  and are installed with `pip install --require-hashes`.

## Test and CI environment

Tests and continuous integration run in Kubernetes
[Prow](https://prow.k8s.io/) (results are published to the
[testgrid](https://testgrid.k8s.io/sig-scheduling) dashboards linked
from [`README.md`](README.md)) and, for release automation, in GitHub
Actions.

- **Same manifests as development.** CI jobs install tooling through
  the same `make` targets and therefore use exactly the versions
  pinned in `hack/tools/go.mod`; there is no separate CI-only tool
  manifest.
- **Kubernetes control-plane binaries.** Integration tests run against
  `envtest` binaries for the Kubernetes version selected by
  `ENVTEST_K8S_VERSION` in [`hack/make/test.mk`](hack/make/test.mk).
  End-to-end tests run against `kind` clusters for each of the
  Kubernetes minor versions listed in `E2E_K8S_VERSIONS` in the same
  file, which is the authoritative list of the minor versions Kueue
  currently tests against. The alignment policy between these pins and
  the `k8s.io/*` Go modules is described in `DEPENDENCY_LIFECYCLE.md`.
- **External operators.** End-to-end tests that exercise integrations
  (JobSet, Kubeflow Training / Trainer / MPI, KubeRay, AppWrapper,
  LeaderWorkerSet, Spark Operator, cert-manager, and so on) install the
  version of each operator that matches the corresponding Go module in
  `go.mod`. The version is resolved with `go list -m` in
  `hack/make/deps.mk`, so the operator deployed in the test cluster and
  the client library compiled into Kueue cannot drift apart. The one
  exception is the Prometheus Operator, which Kueue does not import as
  a Go module: its version is pinned independently by
  `PROMETHEUS_OPERATOR_VERSION` in `hack/make/test.mk`.
- **Test helper images.** Auxiliary images used only by tests (for
  example, `agnhost`, Ray, Redis, Spark, Cypress, and shellcheck) are
  built from the Dockerfiles under
  [`hack/testing`](hack/testing), each of which pins its base image
  by digest. The exception is the `skillsaw` image used by
  `verify-skills-lint`, which is pinned to a version tag only because
  `hack/make/verify.mk` reads the tag from the Dockerfile and runs
  the published image directly rather than building it.
- **GitHub Actions.** Every third-party action referenced from
  `.github/workflows` and `.github/actions` is pinned to a full commit
  SHA, with the human-readable tag recorded in a trailing comment.
  Dependabot keeps these pins current on a daily schedule.
- **Gates.** `make verify` (see
  [`hack/make/verify.mk`](hack/make/verify.mk)) enforces that
  `go.mod` and `go.sum` are tidy and unchanged (`gomod-verify`), that
  npm manifests contain no unused or missing
  packages (`npm-depcheck`), and that lint, formatting, and generated
  artifacts are up to date. Pull requests must pass these checks
  before they can be merged.

## Production environment

The production environment consists of the artifacts Kueue publishes
for each release: container images, Kubernetes manifests, Helm charts,
the `kueuectl` binaries, and the accompanying SBOM and OpenVEX
documents.

- **Container images.** The `kueue` manager, `importer`, and
  `kueueviz` backend images are multi-stage builds: the Go source is
  compiled as a statically linked binary in the digest-pinned builder
  image and copied into the digest-pinned
  `gcr.io/distroless/static:nonroot` base image, which contains no
  shell or package manager and runs as a non-root user. The
  `kueueviz` frontend image is built from a digest-pinned `node`
  image. The only third-party code in a production image is therefore
  the Go modules recorded in `go.mod` (or the npm packages recorded in
  `package-lock.json` for the frontend) plus the base image itself.
- **Image publication.** Images are built by the Kubernetes image
  build infrastructure (see [`cloudbuild.yaml`](cloudbuild.yaml)) into
  the Kubernetes staging registry and then promoted to
  `registry.k8s.io` through the
  [Kubernetes image promotion process](https://github.com/kubernetes/k8s.io),
  which requires a reviewed pull request that records the image digest.
- **Manifests and Helm charts.** The release manifests and Helm charts
  reference the promoted images from `registry.k8s.io` at the release
  tag. They do not pull any other third-party images.
- **SBOM and VEX.** Each GitHub release includes an SPDX SBOM,
  generated with the Kubernetes `bom` tool from the release source
  tree, and an OpenVEX document (see
  [`.github/actions/generate-sbom`](.github/actions/generate-sbom/action.yml)
  and
  [`.github/actions/generate-openvex`](.github/actions/generate-openvex/action.yml)).
  The location of the SBOM for the current release is recorded in
  `SECURITY-INSIGHTS.yaml`.
- **No runtime downloads.** Kueue components do not download code,
  plugins, or other dependencies at runtime. Everything a release
  needs is contained in the published image.

## Consumers of Kueue

Kueue is itself a dependency for downstream users. To make it easy to
consume safely:

- Released images are addressable by immutable digest as well as by
  tag, and the promotion pull request in `kubernetes/k8s.io` provides
  a public record of which digest corresponds to which release.
- Helm charts are published as OCI artifacts to `registry.k8s.io`
  alongside the images, and `kueuectl` is distributed through the
  [krew](https://krew.sigs.k8s.io/) plugin index as well as through
  the GitHub release, so each can be pinned to a specific version.
- Supported release branches and the patch-release policy are described
  in [`RELEASE.md`](RELEASE.md).

## Reviewing this policy

This policy is reviewed at least annually, or sooner if the build,
test, or release infrastructure changes in a material way. The most
recent review is recorded in the `last-reviewed` field of
`SECURITY-INSIGHTS.yaml`.
