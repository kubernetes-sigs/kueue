FROM python:3.12-slim

# Set environment variables to avoid interactive prompts during package installation
ENV DEBIAN_FRONTEND=noninteractive

ARG RAY_VERSION=2.41.0

# Install Ray directly and clean up
RUN apt-get update && apt-get install -y --no-install-recommends curl \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/* \
    && pip3 install --no-cache-dir ray==${RAY_VERSION} \
    && rm -rf /root/.cache

# Expose the default Ray ports
EXPOSE 6379 6380 8265

# Add a healthcheck
HEALTHCHECK --interval=30s --timeout=10s --retries=3 CMD curl -f http://localhost:8265 || exit 1

# Set the default command to start a Ray cluster
CMD ["ray", "start", "--head", "--port=6379", "--dashboard-host=0.0.0.0"]