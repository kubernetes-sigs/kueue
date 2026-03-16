#!/usr/bin/env sh
set -e
export DOCKER_BUILDX_CMD=docker
export PLATFORMS=linux/amd64
export CLI_PLATFORMS=linux/amd64
export VIZ_PLATFORMS=linux/amd64
export STAGING_IMAGE_REGISTRY=icr.io/drl-nextgen/vv/
export GIT_TAG=vv

make image-build

# docker push ${IMAGE_TAG}
