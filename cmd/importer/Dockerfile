ARG BUILDER_IMAGE
ARG BASE_IMAGE
# Build the manager binary
FROM --platform=${BUILDPLATFORM} ${BUILDER_IMAGE} as builder

ARG CGO_ENABLED
ARG TARGETARCH

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY . .

# Build
RUN make importer-build GO_BUILD_ENV='CGO_ENABLED=${CGO_ENABLED} GOOS=linux GOARCH=${TARGETARCH}'

FROM --platform=${BUILDPLATFORM} ${BASE_IMAGE}
WORKDIR /
COPY --from=builder /workspace/bin/importer .
USER 65532:65532

ENTRYPOINT ["/importer"]
