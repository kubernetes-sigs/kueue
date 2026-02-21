# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
	GOBIN=$(shell go env GOPATH)/bin
else
	GOBIN=$(shell go env GOBIN)
endif

GO_CMD ?= go
ROOT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))/../../..

GIT_TAG ?= $(shell git describe --tags --dirty --always)
HOST_IMAGE_PLATFORM ?= linux/$(shell go env GOARCH)
PLATFORMS ?= $(HOST_IMAGE_PLATFORM)
DOCKER_BUILDX_CMD ?= docker buildx
IMAGE_BUILD_CMD ?= $(DOCKER_BUILDX_CMD) build

IMAGE_REGISTRY ?= us-central1-docker.pkg.dev/k8s-staging-images/kueue
IMAGE_NAME := priority-boost-controller
IMAGE_REPO := $(IMAGE_REGISTRY)/$(IMAGE_NAME)
IMAGE_TAG := $(IMAGE_REPO):$(GIT_TAG)

BASE_IMAGE ?= gcr.io/distroless/static:nonroot
BASE_BUILDER_IMAGE ?= golang:1.25.0
BUILDER_IMAGE ?= $(BASE_BUILDER_IMAGE)
CGO_ENABLED ?= 0

.PHONY: build
build:
	$(GO_BUILD_ENV) $(GO_CMD) build -ldflags="$(LD_FLAGS)" -o bin/manager main.go

.PHONY: test
test:
	GOWORK=off $(GO_CMD) test ./...

.PHONY: image-build
image-build:
	$(IMAGE_BUILD_CMD) \
		-t $(IMAGE_TAG) \
		--platform=$(PLATFORMS) \
		--build-arg BASE_IMAGE=$(BASE_IMAGE) \
		--build-arg BUILDER_IMAGE=$(BUILDER_IMAGE) \
		--build-arg CGO_ENABLED=$(CGO_ENABLED) \
		$(PUSH) \
		$(IMAGE_BUILD_EXTRA_OPTS) \
		.

.PHONY: image-push
image-push: PUSH=--push
image-push: image-build

.PHONY: kind-image-build
kind-image-build: PLATFORMS=linux/amd64
kind-image-build: PUSH=--load
kind-image-build: image-build
