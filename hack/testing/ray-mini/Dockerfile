FROM python:3.15-slim@sha256:65a65fc7b30b06cb6df4930fd4650697cb23f4c2698ab81fc87e4b031bc36928

# Set environment variable to avoid interactive prompts during package installation
ENV DEBIAN_FRONTEND=noninteractive

ARG RAY_VERSION=2.55.1
ARG RAY_UID=1000
ARG RAY_GID=100

RUN apt-get update && apt-get install -y --no-install-recommends \
        wget \
        bash \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

SHELL ["/bin/bash", "-c"]

# TODO(ray-project/ray#65507): drop jinja2 once ray[serve] declares it.
RUN pip3 install --no-cache-dir ray[default,serve]==${RAY_VERSION} jinja2 \
    && rm -rf ~/.cache

RUN useradd -ms /bin/bash -d /home/ray ray --uid $RAY_UID --gid $RAY_GID || echo "Group already exists" \
 && usermod -aG sudo ray || echo "User already exists" \
 && echo 'ray ALL=NOPASSWD: ALL' >> /etc/sudoers

USER $RAY_UID
ENV HOME=/home/ray

# Make sure the ports are exposed
EXPOSE 52365
EXPOSE 8265
