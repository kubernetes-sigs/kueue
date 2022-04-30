# Installation

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster with version 1.21 or newer is running. Learn how to [install the Kubernetes tools](https://kubernetes.io/docs/tasks/tools/).
- The SuspendJob [feature gate](https://kubernetes.io/docs/reference/command-line-tools-reference/feature-gates/) is enabled. In Kubernetes 1.22 or newer, the feature gate is enabled by default.
- The kubectl command-line tool has communication with your cluster.
- The [cert-manager](https://github.com/cert-manager/cert-manager) is installed in your cluster. Learn how to [install cert-manager](https://cert-manager.io/docs/installation/).

## Install a released version

To install a released version of Kueue in your cluster, run the following command:

```shell
VERSION=v0.1.0
kubectl apply -f https://github.com/kubernetes-sigs/kueue/releases/download/$VERSION/manifests.yaml
```

## Install a custom-configured released version

To install a custom-configured released version of Kueue in your cluster, execute the following steps: 

1. Download the release's `manifests.yaml` file using the following command:

```shell
VERSION=v0.1.0
wget https://github.com/kubernetes-sigs/kueue/releases/download/$VERSION/manifests.yaml
```
2. Using your favourite editor, open `manifests.yaml` and edit the `kueue-manager-config` 
ConfigMap manifest, specifically edit the `controller_manager_config.yaml` data entry which represents
the default Kueue Configuration 
struct ([v1alpha1@v0.1.0](https://pkg.go.dev/sigs.k8s.io/kueue@v0.1.0/apis/config/v1alpha1#Configuration)).
The contents of the ConfigMap are similar to the following:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: kueue-manager-config
  namespace: kueue-system
data:
  controller_manager_config.yaml: |
    apiVersion: config.kueue.x-k8s.io/v1alpha1
    kind: Configuration
    health:
      healthProbeBindAddress: :8081
    metrics:
      bindAddress: :8080
    webhook:
      port: 9443
    manageJobsWithoutQueueName: true
```

3. To apply the customized manifests to the cluster, run the following command:

```shell
kubectl apply -f manifests.yaml
```

## Install the latest development version

To install the latest development version of Kueue in your cluster, run the
following command:

```shell
kubectl apply -k github.com/kubernetes-sigs/kueue/config/default?ref=main
```

The controller runs in the `kueue-system` namespace.

### Uninstall

To uninstall Kueue, run the following command:

```shell
kubectl delete -k github.com/kubernetes-sigs/kueue/config/default
```

## Build and install from source

To build Kueue from source and install Kueue in your cluster, run the following
commands:

```sh
git clone https://github.com/kubernetes-sigs/kueue.git
cd kueue
IMAGE_REGISTRY=registry.example.com/my-user make image-build image-push deploy
```

### Uninstall

To uninstall Kueue, run the following command:

```sh
make undeploy
```
