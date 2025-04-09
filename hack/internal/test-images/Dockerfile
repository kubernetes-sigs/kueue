# Stage 1: Build Ray and dependencies
FROM --platform=${BUILDPLATFORM} python:3.12-slim AS builder

# Set environment variables to avoid interactive prompts during package installation
ENV DEBIAN_FRONTEND=noninteractive

# Define a build argument for the Ray version
ARG RAY_VERSION=2.41.0

# Update and install dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
        curl \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

# Install Ray using pip
RUN pip3 install --no-cache-dir ray==${RAY_VERSION} \
    && rm -rf /root/.cache

# Stage 2: Create the final distroless image
FROM gcr.io/distroless/python3

# Copy Python dependencies from the builder stage
COPY --from=builder /usr/local/lib/python3.12/site-packages /usr/local/lib/python3.12/site-packages

# Expose the default Ray ports
EXPOSE 6379 6380 8265
