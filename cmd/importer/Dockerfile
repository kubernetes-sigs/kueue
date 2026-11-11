ARG BUILDER_IMAGE
ARG BASE_IMAGE
# Build the manager binary
FROM --platform=${BUILDPLATFORM} ${BUILDER_IMAGE} AS builder

ARG CGO_ENABLED
ARG TARGETARCH

WORKDIR /workspace

# Copy the go source
COPY . .

# Build
ARG IMAGE_BUILD_TRIMPATH=0
RUN --mount=type=cache,target=/root/.cache/go-build \
    if [ "${IMAGE_BUILD_TRIMPATH}" = "1" ]; then export GOFLAGS="${GOFLAGS:+${GOFLAGS} }-trimpath"; fi; \
    make importer-build GO_BUILD_ENV='CGO_ENABLED=${CGO_ENABLED} GOOS=linux GOARCH=${TARGETARCH}'

FROM ${BASE_IMAGE}
WORKDIR /
COPY --from=builder /workspace/bin/importer .
USER 65532:65532

ENTRYPOINT ["/importer"]
