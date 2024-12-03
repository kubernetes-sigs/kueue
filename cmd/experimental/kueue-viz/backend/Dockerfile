# Build stage
FROM golang:latest as builder

# Install necessary tools for building
# RUN apk add --no-cache gcc musl-dev

# Set the working directory
WORKDIR /app

# Copy Go modules manifests and download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy the application source code
COPY . .

# Build the application
RUN go build -o app .

# Runtime stage
FROM golang:latest
USER root
# Install CA certificates for HTTPS support
# RUN apk add --no-cache ca-certificates

# Set environment variables
ENV PORT=8080

# Create a non-root user
# RUN adduser -D  appuser
# USER nobody

# Set the working directory
WORKDIR /app

# Copy the built application from the builder stage
COPY --from=builder /app/app .

# Change ownership to the non-root user
RUN useradd nobody || true && groupadd nobody || true
RUN chown -R nobody:nobody /app

# Run as the non-root user
USER nobody

# Expose the application port
EXPOSE 8080

# Command to run the application
ENTRYPOINT ["./app"]


