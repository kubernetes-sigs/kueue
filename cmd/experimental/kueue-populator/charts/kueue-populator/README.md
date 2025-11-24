# Kueue Prepopulator

This Helm chart installs the Kueue Prepopulator, a component designed to automatically create default LocalQueue resources in namespaces, and sets up initial Kueue resources like a default ClusterQueue and ResourceFlavor. It includes the official Kueue chart as a dependency.

## Purpose

-   Deploys the `kueue-populator` controller manager.
-   Installs Kueue (via subchart dependency).
-   Creates a default `ResourceFlavor` named `tas-gpu-default`.
-   Creates a default `ClusterQueue` (name configurable).
-   The populator then creates a default `LocalQueue` (name configurable) in namespaces matching the selector, pointing to the default ClusterQueue.

## Prerequisites

-   [Helm](https://helm.sh/docs/intro/quickstart/#install-helm)
-   Kubernetes cluster
-   (Optional) [Cert-manager](https://cert-manager.io/docs/installation/)
-   Docker or a compatible container builder.
-   A container registry to push the image to.

## Building the Image

You need to build and push the image to your own registry.
 
From the `cmd/experimental/kueue-populator` directory:
 
```bash
# Build the image
make image-build IMAGE_REGISTRY=<YOUR_REGISTRY>
 
# Push the image
make image-push IMAGE_REGISTRY=<YOUR_REGISTRY>
```
 
This will build and push an image with the tag `<YOUR_REGISTRY>/kueue-populator:<GIT_TAG>`.
 
If you want to use a specific tag, you can override `GIT_TAG`:
 
```bash
make image-build image-push IMAGE_REGISTRY=<YOUR_REGISTRY> GIT_TAG=latest
```

## Installation

To install the chart, you MUST override the image repository and tag with the image you built and pushed.

The following commands assume you are in the `cmd/experimental/kueue-populator` directory.

Example using `--set`:

```bash
helm install kueue-populator ./charts/kueue-populator --namespace kueue-system --create-namespace \
  --set kueuePopulator.image.repository=<YOUR_REGISTRY>/kueue-populator \
  --set kueuePopulator.image.tag=latest
```

Example using a custom `my-values.yaml`:

```yaml
# my-values.yaml
kueuePopulator:
  image:
    repository: <YOUR_REGISTRY>/kueue-populator
    tag: latest
```

```bash
helm install kueue-populator ./charts/kueue-populator --namespace kueue-system --create-namespace -f my-values.yaml
```

## Configuration

### Kueue Prepopulator Configuration

The following table lists the configurable parameters under the `kueuePopulator` key in `values.yaml`:

| Key                                                | Type     | Default           | Description                                                                                                |
| -------------------------------------------------- | -------- | ----------------- | ---------------------------------------------------------------------------------------------------------- |
| `image.repository`                                 | string   | `null`            | **Required.** Image repository for the populator (e.g., `<YOUR_REGISTRY>/kueue-populator`)            |
| `image.tag`                                        | string   | `null`            | **Required.** Image tag for the populator (e.g., `latest`)                                                |
| `image.pullPolicy`                                 | string   | `IfNotPresent`    | Image pull policy                                                                                          |
| `config.localQueue.name`                           | string   | `default`         | Name of the default LocalQueue to create in namespaces                                                     |
| `config.clusterQueue.name`                         | string   | `cluster-queue`   | Name of the default ClusterQueue to create and reference in LocalQueues                                    |
| `config.clusterQueue.resources`                    | list     | (see values.yaml) | Resources to configure in the default ResourceFlavor and ClusterQueue                                      |
| `config.topology.levels`                           | list     | `[]`              | Optional list of node labels for Topology Aware Scheduling levels. Enables Topology creation.            |
| `config.resourceFlavor.nodeLabels`                 | object   | `{}`              | Node labels to associate with the default ResourceFlavor.                                                  |
| `config.managedJobsNamespaceSelector`              | object   | (see values.yaml) | Label selector to filter namespaces where the default LocalQueue will be created. Excludes system namespaces. |

### Kueue Subchart Configuration

This chart includes the official `kueue` chart as a dependency. You can configure it under the `kueue` key in `values.yaml`. Key overrides included in this chart:

-   `kueue.enabled: true`: Enables the subchart installation.
-   `kueue.controllerManager.featureGates`: Enables `TopologyAwareScheduling`.
-   `kueue.managerConfig.controllerManagerConfigYaml`: Provides minimal necessary overrides for `apiVersion` and `managedJobsNamespaceSelector` to ensure compatibility and safe hook execution.

See the [Kueue chart README](https://github.com/kubernetes-sigs/kueue/blob/main/charts/kueue/README.md) for all possible Kueue configuration options.

## Testing
 
### Unit Tests & Linting
 
You can run unit tests and linting locally without a cluster using the Makefile targets:
 
```bash
# Run Helm lint
make helm-lint
 
# Verify Helm template rendering
make helm-verify
 
# Run Helm unit tests (requires helm-unittest plugin)
make helm-unit-test
```
 
### Integration Tests
 
This chart includes tests to verify the installation. Assuming you have installed the chart as `kueue-populator` in the `kueue-system` namespace, you can run the tests using:
 
```bash
helm test kueue-populator --namespace kueue-system
```
 
Or using the Makefile target (requires active cluster):
 
```bash
make helm-test
```
 
This will launch a few test pods that check for the health of the deployments and the existence of the expected resources.

## Uninstallation

To uninstall the chart:

```bash
helm uninstall kueue-populator --namespace kueue-system
```
