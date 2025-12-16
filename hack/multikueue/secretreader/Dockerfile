FROM golang:1.25-alpine AS builder

ARG CLUSTERPROFILE_SHA=2e3fabb46733

RUN apk add --no-cache git
WORKDIR /src

# Clone the repository into /src
RUN git clone https://github.com/kubernetes-sigs/cluster-inventory-api.git .

# Checkout the desired commit
RUN git checkout ${CLUSTERPROFILE_SHA}

# Build the binary
RUN go build -o /src/secretreader-plugin ./cmd/secretreader-plugin

# Final image (the same alpine version as in golang:1.25-alpine)
FROM alpine:3.23.2
WORKDIR /app
USER 65532:65532
COPY --from=builder /src/secretreader-plugin /app/secretreader-plugin
