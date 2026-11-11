# Build stage
ARG BUILDER_IMAGE=public.ecr.aws/docker/library/golang:1.27
ARG BASE_IMAGE=gcr.io/distroless/static:nonroot
FROM --platform=${BUILDPLATFORM} ${BUILDER_IMAGE} AS builder

COPY cmd/kueueviz/backend/go.mod cmd/kueueviz/backend/go.sum ./
COPY hack/testing/retry.sh /usr/local/bin/retry.sh
RUN retry.sh --stream -- go mod download

# Copy the application source code
COPY cmd/kueueviz/backend/ .

# Build the application
ARG TARGETARCH
ARG IMAGE_BUILD_TRIMPATH=0
RUN --mount=type=cache,target=/root/.cache/go-build \
    if [ "${IMAGE_BUILD_TRIMPATH}" = "1" ]; then export GOFLAGS="${GOFLAGS:+${GOFLAGS} }-trimpath"; fi; \
    CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -v -o /kueueviz

# Runtime stage
FROM ${BASE_IMAGE}
USER 65532:65532

# Copy the built application from the builder stage
COPY --from=builder /kueueviz /

# Set environment variables
ENV KUEUEVIZ_PORT=8080
# Expose the application port
EXPOSE 8080

# Command to run the application
ENTRYPOINT ["/kueueviz"]
