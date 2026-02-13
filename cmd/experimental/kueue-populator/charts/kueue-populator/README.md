# Kueue Populator

This Helm chart installs the Kueue Populator, a component designed to automatically create default LocalQueue resources in namespaces, and sets up initial Kueue resources like a default ClusterQueue and ResourceFlavor. It includes the official Kueue chart as a dependency.

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
-   (Optional) Docker or a compatible container builder.
-   (Optional) A container registry to push the image to.

## Dependencies

This chart depends on the [Kueue](https://github.com/kubernetes-sigs/kueue/tree/main/charts/kueue) chart.
By default, the Kueue dependency is **disabled** (`kueue.enabled=false`).

-   To install Kueue along with the populator, set `kueue.enabled=true`.
-   The dependency is configured to enable `TopologyAwareScheduling` feature gate in Kueue.

## Installation

### Installing from OCI Registry

You can install the chart directly from the OCI registry:

```bash
helm install kueue-populator oci://registry.k8s.io/kueue/charts/kueue-populator \
  --version 0.16.1 \
  --namespace kueue-system \
  --create-namespace \
  --wait
```

> The `--wait` flag is required to ensure that the Kueue controller and webhooks are fully ready before the chart attempts to create Kueue resources (like ClusterQueue) via post-install hooks. Without it, the installation may fail.

### Installing from Source

If you want to make changes to the populator and deploy your own version, you need to build and push the image to your own registry.

#### Building the Image

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

#### Installing with a Custom Image

If you built your own image, you **must** override the image repository and tag.

The following commands assume you are in the `cmd/experimental/kueue-populator` directory.

Example using `--set`:

```bash
helm install kueue-populator ./charts/kueue-populator --namespace kueue-system --create-namespace --wait \
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
helm install kueue-populator ./charts/kueue-populator --namespace kueue-system --create-namespace --wait -f my-values.yaml
```

### Installing with Topology and ResourceFlavor

To enable Topology Aware Scheduling and configure the default ResourceFlavor with node labels, it is recommended to use a values file:

```yaml
# populator-config.yaml
kueuePopulator:
  config:
    topology:
      levels:
        - nodeLabel: cloud.google.com/gke-nodepool
    resourceFlavor:
      nodeLabels:
        cloud.google.com/gke-nodepool: "default-pool"
```

```bash
helm install kueue-populator oci://registry.k8s.io/kueue/charts/kueue-populator \
  --version 0.16.1 \
  --namespace kueue-system \
  --create-namespace \
  --wait \
  -f populator-config.yaml
```

For simple configuration you may also use the minimalistic command:

> When using `--set` for keys containing dots (e.g., `cloud.google.com/gke-nodepool`), you must escape the dots with a backslash.

```bash
helm install kueue-populator oci://registry.k8s.io/kueue/charts/kueue-populator \
  --version 0.16.1 \
  --namespace kueue-system \
  --create-namespace \
  --wait \
  --set kueuePopulator.config.topology.levels[0].nodeLabel="cloud\.google\.com/gke-nodepool" \
  --set kueuePopulator.config.resourceFlavor.nodeLabels."cloud\.google\.com/gke-nodepool"=default-pool
```

## Configuration

### Kueue Populator Configuration

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

-   `kueue.enabled: false`: Disables the subchart installation by default. Set to `true` to install Kueue.
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
