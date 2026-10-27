FROM golang:1.27-alpine@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS builder
ARG PLUGIN_VERSION
RUN go install sigs.k8s.io/cluster-inventory-api/plugins/secretreader/cmd/plugin@${PLUGIN_VERSION}

# Final image
FROM alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
USER 65534:65534
# Place the binary at /bin to mirror the official secretreader image layout, so the
# init-container fallback and the image-volume mount expose it at the same path.
COPY --from=builder /go/bin/plugin /bin/secretreader-plugin
