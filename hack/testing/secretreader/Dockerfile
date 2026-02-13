FROM golang:1.26-alpine AS builder
ARG PLUGIN_VERSION
RUN go install sigs.k8s.io/cluster-inventory-api/cmd/secretreader-plugin@${PLUGIN_VERSION}

# Final image
FROM alpine:3.23.3
USER 65534:65534
COPY --from=builder /go/bin/secretreader-plugin /secretreader-plugin
