# Build stage
ARG BUILDER_IMAGE=golang:1.23
ARG BASE_IMAGE=gcr.io/distroless/static:nonroot
FROM --platform=${BUILDPLATFORM} ${BUILDER_IMAGE} AS builder

# Copy Go modules manifests and download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy the application source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 go build -o /kueue-viz

# Runtime stage
FROM --platform=${BUILDPLATFORM} ${BASE_IMAGE}
USER 65532:65532

# Copy the built application from the builder stage
COPY --from=builder /kueue-viz /

# Set environment variables
ENV PORT=8080
# Expose the application port
EXPOSE 8080

# Command to run the application
ENTRYPOINT ["/kueue-viz"]


