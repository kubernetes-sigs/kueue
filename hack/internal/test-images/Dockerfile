FROM ubuntu:22.04

# Set environment variables to avoid interactive prompts during package installation
ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get clean && rm -rf /var/lib/apt/lists/* \
    && apt-get update --fix-missing \
    && apt-get install -y --no-install-recommends \
        python3 \
        python3-pip \
        curl \
        wget \
    && apt-get clean && rm -rf /var/lib/apt/lists/*

# Install Ray
RUN pip3 install --no-cache-dir ray

# Expose the default Ray ports
EXPOSE 6379 6380 8265

# Set the entrypoint to start a Ray cluster
ENTRYPOINT ["ray", "start", "--head", "--port=6379", "--dashboard-host=0.0.0.0"]