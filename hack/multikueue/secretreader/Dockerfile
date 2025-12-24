FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git
WORKDIR /src

# Clone the repository into /src
RUN git clone https://github.com/kubernetes-sigs/cluster-inventory-api.git .

# Build the binary
RUN go build -o /src/secretreader-plugin ./cmd/secretreader-plugin

# Final image
FROM alpine:latest
WORKDIR /app
USER 65532:65532
COPY --from=builder /src/secretreader-plugin /app/secretreader-plugin