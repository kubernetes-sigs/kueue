IMAGE_TAG ?= latest
IMAGE ?= example.com/kueue-podtaintstolerations:$(IMAGE_TAG)


.PHONY: docker-build
docker-build:
	docker buildx build . -t $(IMAGE_TAG)

.PHONY: test-e2e
test-e2e: docker-build
	E2E_KIND_VERSION="kindest/node:v1.24.15" \
	KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) \
	CREATE_KIND_CLUSTER=$(CREATE_KIND_CLUSTER) \
	ARTIFACTS="$(ARTIFACTS)" \
	IMAGE=$(IMAGE) \
	./hack/e2e-test.sh

