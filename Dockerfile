# defaulted ARGs need to be declared first
ARG BUILDER_IMAGE=golang:1.26@sha256:3aff6657219a4d9c14e27fb1d8976c49c29fddb70ba835014f477e1c70636647
ARG BASE_IMAGE=gcr.io/distroless/static:nonroot@sha256:963fa6c544fe5ce420f1f54fb88b6fb01479f054c8056d0f74cc2c6000df5240
# compilation stage for the manager binary
FROM --platform=${BUILDPLATFORM} ${BUILDER_IMAGE} AS builder
WORKDIR /workspace
# The root module includes vendored dependencies; no module download is needed.
COPY . .
ARG GIT_TAG GIT_COMMIT TARGETARCH CGO_ENABLED=0
ARG IMAGE_BUILD_GOMAXPROCS
ARG IMAGE_BUILD_GO_PARALLELISM
RUN --mount=type=cache,target=/root/.cache/go-build \
    if [ -n "${IMAGE_BUILD_GOMAXPROCS}" ]; then export GOMAXPROCS="${IMAGE_BUILD_GOMAXPROCS}"; fi; \
    if [ -n "${IMAGE_BUILD_GO_PARALLELISM}" ]; then export GOFLAGS="${GOFLAGS:+${GOFLAGS} }-p=${IMAGE_BUILD_GO_PARALLELISM}"; fi; \
    bash hack/testing/image-build-observe.sh manager \
    make build GIT_TAG="${GIT_TAG}" GIT_COMMIT="${GIT_COMMIT}" GO_BUILD_ENV="GOARCH=${TARGETARCH} CGO_ENABLED=${CGO_ENABLED}"

# final image, implicitly --platform=${TARGETPLATFORM}
FROM ${BASE_IMAGE}
WORKDIR /
USER 65532:65532
ENTRYPOINT ["/manager"]
COPY --from=builder /workspace/bin/manager /manager
