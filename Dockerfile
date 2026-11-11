# defaulted ARGs need to be declared first
ARG BUILDER_IMAGE=public.ecr.aws/docker/library/golang:1.27
ARG BASE_IMAGE=gcr.io/distroless/static:nonroot
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
