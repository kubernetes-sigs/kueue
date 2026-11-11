ARG BUILDER_IMAGE=public.ecr.aws/docker/library/golang:1.27
ARG BASE_IMAGE=gcr.io/distroless/static:nonroot

FROM --platform=${BUILDPLATFORM} ${BUILDER_IMAGE} AS builder

WORKDIR /workspace
# Build from the root module with its vendored dependencies.
COPY . .

ARG CGO_ENABLED
ARG TARGETARCH
WORKDIR /workspace/cmd/experimental/kueue-priority-booster

RUN --network=none --mount=type=tmpfs,target=/tmp/kueue-empty-modcache \
    --mount=type=cache,target=/root/.cache/go-build \
    GOMODCACHE=/tmp/kueue-empty-modcache bash /workspace/hack/testing/image-build-observe.sh priority-booster \
    make build GO_BUILD_ENV='CGO_ENABLED=${CGO_ENABLED} GOOS=linux GOARCH=${TARGETARCH}'

FROM ${BASE_IMAGE}
WORKDIR /
COPY --from=builder /workspace/cmd/experimental/kueue-priority-booster/bin/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
