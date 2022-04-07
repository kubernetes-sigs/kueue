# Installation

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster with version 1.21 or newer is running. Learn how to [install the Kubernetes tools](https://kubernetes.io/docs/tasks/tools/).
- The SuspendJob [feature gate](https://kubernetes.io/docs/reference/command-line-tools-reference/feature-gates/) is enabled. In Kubernetes 1.22 or newer, the feature gate is enabled by default.
- The kubectl command-line tool has communication with your cluster.

## Install Kueue

To deploy Kueue in your cluster, run the following commands:

```sh
git clone https://github.com/kubernetes-sigs/kueue.git
cd kueue
IMAGE_REGISTRY=registry.example.com/my-user make image-build image-push deploy
```

The controller will run in the `kueue-system` namespace.

## Uninstall Kueue

To uninstall Kueue, run the following command:

```sh
$ make undeploy 
```