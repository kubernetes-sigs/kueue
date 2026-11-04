# defaulted ARGs need to be declared first
ARG BUILDER_IMAGE=public.ecr.aws/docker/library/golang:1.27
ARG BASE_IMAGE=gcr.io/distroless/static:nonroot
# compilation stage for the manager binary
FROM --platform=${BUILDPLATFORM} ${BUILDER_IMAGE} AS builder
WORKDIR /workspace
# Fetch dependencies first, with retries for transient download failures.
COPY go.mod go.sum ./
COPY hack/testing/retry.sh /usr/local/bin/retry.sh
RUN retry.sh --attempts 7 --delay 2 --exponential --stream -- go mod download
COPY . .
ARG GIT_TAG GIT_COMMIT TARGETARCH CGO_ENABLED=0
RUN --mount=type=cache,target=/root/.cache/go-build \
    make build GIT_TAG="${GIT_TAG}" GIT_COMMIT="${GIT_COMMIT}" GO_BUILD_ENV="GOARCH=${TARGETARCH} CGO_ENABLED=${CGO_ENABLED}"

# final image, implicitly --platform=${TARGETPLATFORM}
FROM ${BASE_IMAGE}
WORKDIR /
USER 65532:65532
ENTRYPOINT ["/manager"]
COPY --from=builder /workspace/bin/manager /manager
