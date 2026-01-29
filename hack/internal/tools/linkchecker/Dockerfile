# Copyright 2026 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Build stage: compile the linkchecker Go binary inside Docker.
ARG GO_VERSION
FROM golang:${GO_VERSION}-bookworm AS builder

WORKDIR /workspace
COPY . .

RUN cd hack/internal/tools && \
    go build -o /usr/local/bin/kueue-verify-website-links ./linkchecker

# Runtime stage: provide all dependencies and run the compiled binary.
FROM debian:bookworm-slim

ARG NODE_VERSION=20.18.0
ARG HUGO_VERSION
ARG LINKCHECKER_VERSION=10.5.0

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    git \
    make \
    python3 \
    python3-pip \
    python3-venv \
    xz-utils \
  && rm -rf /var/lib/apt/lists/*

RUN test -n "${NODE_VERSION}" && test -n "${HUGO_VERSION}"

# Node.js LTS (from nodejs.org)
RUN ARCH="$(dpkg --print-architecture)" && \
    curl -fsSL "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${ARCH}.tar.xz" \
    | tar -xJ --strip-components=1 -C /usr/local && \
    node --version && npm --version

# Hugo extended
RUN ARCH="$(dpkg --print-architecture)" && \
    curl -fsSL \
      "https://github.com/gohugoio/hugo/releases/download/v${HUGO_VERSION}/hugo_extended_${HUGO_VERSION}_linux-${ARCH}.tar.gz" \
    | tar -xz -C /usr/local/bin hugo && \
    hugo version

# linkchecker (Python)
RUN pip3 install --break-system-packages "linkchecker==${LINKCHECKER_VERSION}" && \
    linkchecker --version

COPY --from=builder /usr/local/bin/kueue-verify-website-links /usr/local/bin/kueue-verify-website-links
COPY --from=builder /usr/local/go /usr/local/go

ENV PATH="/usr/local/go/bin:${PATH}"

WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/kueue-verify-website-links"]
