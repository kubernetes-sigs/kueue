ARG BUILDER_IMAGE
ARG BASE_IMAGE
# Build the manager binary
FROM --platform=${BUILDPLATFORM} ${BUILDER_IMAGE} as builder

ARG CGO_ENABLED
ARG TARGETARCH

WORKDIR /workspace

# Copy the go source
COPY . .

# Build
RUN make build GO_BUILD_ENV='CGO_ENABLED=${CGO_ENABLED} GOOS=linux GOARCH=${TARGETARCH}'

FROM --platform=${BUILDPLATFORM} ${BASE_IMAGE}
WORKDIR /
COPY --from=builder /workspace/bin/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
