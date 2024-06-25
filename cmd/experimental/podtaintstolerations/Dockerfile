# Build the manager binary
FROM --platform=${BUILDPLATFORM} golang:1.21 AS builder

ARG TARGETARCH

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY main.go .
COPY controller ./controller

# Build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build .

FROM --platform=${BUILDPLATFORM} gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/podtaintstolerations .
USER 65532:65532

ENTRYPOINT ["/manager"]
