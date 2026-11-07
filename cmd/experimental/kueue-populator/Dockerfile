ARG BUILDER_IMAGE=public.ecr.aws/docker/library/golang:1.27
ARG BASE_IMAGE=gcr.io/distroless/static:nonroot

FROM --platform=${BUILDPLATFORM} ${BUILDER_IMAGE} AS builder

ARG CGO_ENABLED
ARG TARGETARCH

WORKDIR /workspace

# Build from the root module with its vendored dependencies.
COPY . .

WORKDIR /workspace/cmd/experimental/kueue-populator

COPY hack/testing/retry.sh /usr/local/bin/retry.sh
RUN retry.sh --stream -- go mod download
RUN --mount=type=cache,target=/root/.cache/go-build \
    make build GO_BUILD_ENV='CGO_ENABLED=${CGO_ENABLED} GOOS=linux GOARCH=${TARGETARCH}'

FROM ${BASE_IMAGE}
WORKDIR /
COPY --from=builder /workspace/cmd/experimental/kueue-populator/bin/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
