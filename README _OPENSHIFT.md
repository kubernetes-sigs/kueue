This document is to detail our process for managing the Kueue fork.

# Openshift specific Makefiles

All openshift related make targets are contained in Makefile-test-ocp.mk.

This is to allow our CI to pass without modifying operand make targets.

# Building the Kueue OCP Image

```
DOCKER_BUILDX_CMD=podman IMAGE_REGISTRY=quay.io/some.user USE_OCP=true make -f Makefile.ocp image-build-generic
DOCKER_BUILDX_CMD=podman IMAGE_REGISTRY=quay.io/some.user USE_OCP=true make -f Makefile.ocp image-push-generic
```

# Integration tests

Prow has limited access to the internet so `go mod download` is not allowed.
This blocked onboarding the integration tests
for Kueue.

Therefore, we have to generate the dependent-crds` on each release and commit that directory.

One should run `make dep-crds` and then commit the dep-crds folder.

This will run the integration tests with the latest changes for each supported integration.
