ARG BUILDER_IMAGE=public.ecr.aws/docker/library/golang:1.27
ARG BASE_IMAGE=gcr.io/distroless/static:nonroot

FROM --platform=${BUILDPLATFORM} ${BUILDER_IMAGE} AS builder

WORKDIR /workspace
# Build from the root module with its vendored dependencies.
COPY . .

ARG CGO_ENABLED
ARG TARGETARCH
WORKDIR /workspace/cmd/experimental/kueue-priority-booster

ARG IMAGE_BUILD_TRIMPATH=0
RUN --mount=type=cache,target=/root/.cache/go-build \
    if [ "${IMAGE_BUILD_TRIMPATH}" = "1" ]; then export GOFLAGS="${GOFLAGS:+${GOFLAGS} }-trimpath"; fi; \
    make build GO_BUILD_ENV='CGO_ENABLED=${CGO_ENABLED} GOOS=linux GOARCH=${TARGETARCH}'

FROM ${BASE_IMAGE}
WORKDIR /
COPY --from=builder /workspace/cmd/experimental/kueue-priority-booster/bin/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
