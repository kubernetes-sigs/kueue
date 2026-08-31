FROM golang:1.27-alpine@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS builder
ARG PLUGIN_VERSION
RUN go install sigs.k8s.io/cluster-inventory-api/plugins/secretreader/cmd/plugin@${PLUGIN_VERSION}

# Final image
FROM alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
USER 65534:65534
# Place the binary at /bin to mirror the official secretreader image layout, so the
# init-container fallback and the image-volume mount expose it at the same path.
COPY --from=builder /go/bin/plugin /bin/secretreader-plugin
