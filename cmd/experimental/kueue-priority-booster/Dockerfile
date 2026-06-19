ARG BUILDER_IMAGE=golang:1.26
ARG BASE_IMAGE=gcr.io/distroless/static:nonroot

FROM --platform=${BUILDPLATFORM} ${BUILDER_IMAGE} AS builder

ARG CGO_ENABLED
ARG TARGETARCH

WORKDIR /workspace

# Build from repo root (same module as kueue); no per-component go.mod.
COPY . .

WORKDIR /workspace/cmd/experimental/kueue-priority-booster

COPY hack/testing/retry.sh /usr/local/bin/retry.sh
RUN retry.sh --attempts 7 --delay 2 --exponential --stream -- go mod download
RUN make build GO_BUILD_ENV='CGO_ENABLED=${CGO_ENABLED} GOOS=linux GOARCH=${TARGETARCH}'

FROM ${BASE_IMAGE}
WORKDIR /
COPY --from=builder /workspace/cmd/experimental/kueue-priority-booster/bin/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
