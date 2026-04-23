ARG BUILDER_IMAGE=golang:1.26
ARG BASE_IMAGE=gcr.io/distroless/static:nonroot

FROM --platform=${BUILDPLATFORM} ${BUILDER_IMAGE} AS builder

ARG CGO_ENABLED
ARG TARGETARCH

WORKDIR /workspace

# Build from repo root so the local replace in
# cmd/experimental/kueue-populator/go.mod:
#   replace sigs.k8s.io/kueue => ../../..
# resolves correctly inside the container.
COPY . .

WORKDIR /workspace/cmd/experimental/kueue-populator

RUN go mod download
RUN make build GO_BUILD_ENV='CGO_ENABLED=${CGO_ENABLED} GOOS=linux GOARCH=${TARGETARCH}'

FROM --platform=${BUILDPLATFORM} ${BASE_IMAGE}
WORKDIR /
COPY --from=builder /workspace/cmd/experimental/kueue-populator/bin/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
