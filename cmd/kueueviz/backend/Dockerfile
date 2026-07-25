# Build stage
ARG BUILDER_IMAGE=golang:1.26@sha256:3aff6657219a4d9c14e27fb1d8976c49c29fddb70ba835014f477e1c70636647
ARG BASE_IMAGE=gcr.io/distroless/static:nonroot@sha256:963fa6c544fe5ce420f1f54fb88b6fb01479f054c8056d0f74cc2c6000df5240
FROM --platform=${BUILDPLATFORM} ${BUILDER_IMAGE} AS builder

COPY cmd/kueueviz/backend/go.mod cmd/kueueviz/backend/go.sum ./
COPY hack/testing/retry.sh /usr/local/bin/retry.sh
RUN retry.sh --attempts 7 --delay 2 --exponential --stream -- go mod download

# Copy the application source code
COPY cmd/kueueviz/backend/ .

# Build the application
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -o /kueueviz

# Runtime stage
FROM ${BASE_IMAGE}
USER 65532:65532

# Copy the built application from the builder stage
COPY --from=builder /kueueviz /

# Set environment variables
ENV PORT=8080
# Expose the application port
EXPOSE 8080

# Command to run the application
ENTRYPOINT ["/kueueviz"]
