FROM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS builder
ARG PLUGIN_VERSION
RUN go install sigs.k8s.io/cluster-inventory-api/plugins/secretreader/cmd/plugin@${PLUGIN_VERSION}

# Final image
FROM alpine:3.24.2@sha256:31b6477333eb8257db9e5d7c3a7264fd0467928756f0bbcc27d35bea5d28cdbd
USER 65534:65534
# Place the binary at /bin to mirror the official secretreader image layout, so the
# init-container fallback and the image-volume mount expose it at the same path.
COPY --from=builder /go/bin/plugin /bin/secretreader-plugin
