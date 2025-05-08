# defaulted ARGs need to be declared first
ARG BUILDER_IMAGE=golang:1.24
ARG BASE_IMAGE=gcr.io/distroless/static:nonroot
# compilation stage for the manager binary
FROM --platform=${BUILDPLATFORM} ${BUILDER_IMAGE} AS builder
WORKDIR /workspace
# fetch dependencies first, for iterative development
COPY go.mod go.sum ./
RUN go mod download
# copy the rest of the sources and build
COPY . .
ARG CGO_ENABLED TARGETARCH
RUN make build GO_BUILD_ENV='CGO_ENABLED=${CGO_ENABLED} GOOS=linux GOARCH=${TARGETARCH}'

# final image, implicitly --platform=${TARGETPLATFORM}
FROM ${BASE_IMAGE}
WORKDIR /
USER 65532:65532
ENTRYPOINT ["/manager"]
COPY --from=builder /workspace/bin/manager /manager
