ARG BUILDER_IMAGE=golang:1.26@sha256:32c0e6e5c4f6707717051091b4d0b077464a679eaab563e11474efc5328e2aa5
ARG BASE_IMAGE=gcr.io/distroless/static:nonroot@sha256:963fa6c544fe5ce420f1f54fb88b6fb01479f054c8056d0f74cc2c6000df5240

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
