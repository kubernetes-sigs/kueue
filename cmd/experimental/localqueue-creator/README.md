# Local Queue Creator

The `localqueue-creator` is an experimental controller that automatically creates a `LocalQueue` in namespaces that match a `ClusterQueue`'s `namespaceSelector`. This simplifies the setup for users who want to automatically provision `LocalQueue`s without manual intervention.

## Purpose

This component demonstrates how to extend Kueue's functionality with custom controllers that operate on Kueue resources. It is not part of the main Kueue binary and is intended to be built and deployed independently.

## Build

To build the `localqueue-creator` binary:

```bash
make build
```

This will create the executable `bin/manager` in the `cmd/experimental/localqueue-creator/bin` directory.

To build the container image:

```bash
make image-build
```

The image will be tagged as `us-central1-docker.pkg.dev/k8s-staging-images/kueue/localqueue-creator:$(GIT_TAG)`.

## Deploy

The `localqueue-creator` can be deployed to a Kubernetes cluster using the Kustomize manifests located in the `run-in-cluster/` directory.

1.  **Ensure Kueue is installed:** The `localqueue-creator` relies on Kueue CRDs and assumes a Kueue installation is present in the cluster.
2.  **Apply manifests (to an existing cluster):**

    ```bash
    cd run-in-cluster
    kubectl apply -k .
    ```

### Deploying to Kind with a Local Image

To deploy the `localqueue-creator` using a locally built image into a Kind cluster:

1.  **Build and Load Image:** Ensure you have built the image for your local environment:

    ```bash
    make kind-image-build
    ```

    This will build the image and load it into your local Docker daemon. You then need to load it into your Kind cluster. First, identify the image tag:

    ```bash
    GIT_TAG=$(git describe --tags --dirty --always)
    IMAGE_TAG="us-central1-docker.pkg.dev/k8s-staging-images/kueue/localqueue-creator:$GIT_TAG"
    kind load docker-image "$IMAGE_TAG" --name <your-kind-cluster-name>
    ```

2.  **Apply manifests:** Navigate to the `run-in-cluster` directory and apply the manifests.

    ```bash
    cd run-in-cluster
    kubectl apply -k .
    ```

## Configuration

The `localqueue-creator` supports the following command-line flags:

*   `--local-queue-name`: The name of the `LocalQueue` to create by default in selected namespaces. Defaults to `"default"`.
*   `--zap-log-level`: Sets the logging verbosity level for the Zap logger (e.g., `info`, `debug`, `error`).

To set these flags, modify the `args` list in the `Deployment` manifest (`run-in-cluster/localqueue-creator.yaml`) or use a Kustomize patch. For example:

```yaml
      containers:
      - args:
        - "--zap-log-level=2"
        - "--local-queue-name=my-custom-queue"
```
