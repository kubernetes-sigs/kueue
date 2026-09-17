FROM golang:1.27-alpine@sha256:cf6fca6641884b8433441b2b0652976f975e1d0fdd26d177eaaf8596087f3125 AS builder
ARG PLUGIN_VERSION
RUN go install sigs.k8s.io/cluster-inventory-api/plugins/secretreader/cmd/plugin@${PLUGIN_VERSION}

# Final image
FROM alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
USER 65534:65534
# Place the binary at /bin to mirror the official secretreader image layout, so the
# init-container fallback and the image-volume mount expose it at the same path.
COPY --from=builder /go/bin/plugin /bin/secretreader-plugin
