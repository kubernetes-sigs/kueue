#Copyright 2024 The Kubernetes Authors.
#
#Licensed under the Apache License, Version 2.0 (the "License");
#you may not use this file except in compliance with the License.
#You may obtain a copy of the License at
#
#    http://www.apache.org/licenses/LICENSE-2.0
#
#Unless required by applicable law or agreed to in writing, software
#distributed under the License is distributed on an "AS IS" BASIS,
#WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
#See the License for the specific language governing permissions and
#limitations under the License.

ARG BUILDER_IMAGE=golang:1.23
ARG BASE_IMAGE=scratch

FROM --platform=${BUILDPLATFORM} ${BUILDER_IMAGE} AS builder

ARG CGO_ENABLED=0
ARG TARGETOS=linus
ARG TARGETARCH=amd64

WORKDIR /workspace

COPY . .

RUN GO_BUILD_ENV='CGO_ENABLED=${CGO_ENABLED} GOOS=${TARGETOS} GOARCH=${TARGETARCH}' go build -o slurm-init main.go

FROM --platform=${BUILDPLATFORM} ${BASE_IMAGE}
WORKDIR /
COPY --from=builder /workspace/slurm-init .

ENTRYPOINT ["/slurm-init"]