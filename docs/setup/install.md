# Installation

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster with version 1.21 or newer is running. Learn how to [install the Kubernetes tools](https://kubernetes.io/docs/tasks/tools/).
- The SuspendJob [feature gate](https://kubernetes.io/docs/reference/command-line-tools-reference/feature-gates/) is enabled. In Kubernetes 1.22 or newer, the feature gate is enabled by default.
- The kubectl command-line tool has communication with your cluster.

## Install the latest development version

To install the latest development version of Kueue in your cluster, run the
following command:

```shell
kubectl apply -k github.com/kubernetes-sigs/kueue/config/default
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
$ make undeploy 
```