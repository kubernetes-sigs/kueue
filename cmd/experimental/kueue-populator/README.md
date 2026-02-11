# Kueue Populator

The `kueue-populator` is an experimental controller that automatically creates a `LocalQueue` in namespaces that match a `ClusterQueue`'s `namespaceSelector`. This simplifies the setup for users who want to automatically provision `LocalQueue`s without manual intervention.

## Purpose

This component demonstrates how to extend Kueue's functionality with custom controllers that operate on Kueue resources. It is not part of the main Kueue binary and is intended to be built and deployed independently.

## Build

To build the `kueue-populator` binary:

```bash
make build
```

This will create the executable `bin/manager` in the current directory (`cmd/experimental/kueue-populator/bin`).

To build the container image:

```bash
make image-build
```

The image will be tagged as `us-central1-docker.pkg.dev/k8s-staging-images/kueue/kueue-populator:$(GIT_TAG)`.

## Deploy

The `kueue-populator` can be deployed to a Kubernetes cluster using the Kustomize manifests located in the `config/` directory.

1.  **Ensure Kueue is installed:** The `kueue-populator` relies on Kueue CRDs and assumes a Kueue installation is present in the cluster.
2.  **Apply manifests (to an existing cluster):**

    ```bash
    kubectl apply -k config
    ```

### Installation via Helm

You can also install the `kueue-populator` using the provided Helm chart.

```bash
helm install kueue-populator oci://registry.k8s.io/kueue/charts/kueue-populator \
  --version 0.16.1 \
  --namespace kueue-system \
  --create-namespace \
  --wait
```

For more details on configuration and advanced usage, see the [Helm Chart README](charts/kueue-populator/README.md).

### Deploying to Kind with a Local Image

To deploy the `kueue-populator` using a locally built image into a Kind cluster:

1.  **Build and Load Image:** Ensure you have built the image for your local environment:

    ```bash
    make kind-image-build
    ```

    This will build the image and load it into your local Docker daemon and Kind cluster.

2.  **Apply manifests:** Apply the manifests from the `config` directory.

    ```bash
        kubectl apply -k config
    ```
    
## Configuration
The `kueue-populator` supports the following command-line flags:

*   `--local-queue-name`: The name of the `LocalQueue` to create by default in selected namespaces. Defaults to `"default"`.
*   `--zap-log-level`: Sets the logging verbosity level for the Zap logger (e.g., `info`, `debug`, `error`).

To set these flags, modify the `args` list in the `Deployment` manifest (`config/manager/manager.yaml`) or use a Kustomize patch. For example:

```yaml
      containers:
      - args:
        - "--zap-log-level=2"
        - "--local-queue-name=my-custom-queue"
```

## Testing

The `kueue-populator` project includes unit, integration, and end-to-end (e2e) tests to ensure its functionality and reliability.

### Unit Tests

To run the unit tests:

```bash
make test
```

### Integration Tests

To run the integration tests:

```bash
make test-integration
```

### End-to-End Tests

To run the end-to-end tests, you need a Kubernetes cluster and a `kind` image.

1.  **Build Kind Image:**

    ```bash
    make kind-image-build
    ```

2.  **Run E2E Tests:**

    ```bash
    make test-e2e
    ```
