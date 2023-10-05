CONTROLLER_IMAGE_TAG ?= latest
CONTROLLER_IMAGE ?= example.com/kueue-podtaintstolerations:$(CONTROLLER_IMAGE_TAG)

.PHONY: docker-build
docker-build:
	docker buildx build . -t $(CONTROLLER_IMAGE)

.PHONY: test-dependencies
test-dependencies:
	mkdir -p bin/test-e2e
	cd ../../.. && $(MAKE) kind kustomize ginkgo

.PHONY: test-e2e
test-e2e: docker-build
	E2E_KIND_VERSION="kindest/node:v1.24.15" \
	KIND_CLUSTER_NAME='test-e2e' \
	CREATE_KIND_CLUSTER='true' \
	ARTIFACTS="./bin/test-e2e" \
	CONTROLLER_IMAGE=$(CONTROLLER_IMAGE) \
	./hack/e2e-test.sh

