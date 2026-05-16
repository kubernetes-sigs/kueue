# Build stage
ARG BUILDER_IMAGE=golang:1.26
ARG BASE_IMAGE=gcr.io/distroless/static:nonroot
FROM --platform=${BUILDPLATFORM} ${BUILDER_IMAGE} AS builder

# Copy Go modules manifests and download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy the application source code
COPY . .

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


