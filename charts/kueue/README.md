# Kueue's helm chart

## Table of Contents

<!-- toc -->
- [Installation](#installation)
  - [Prerequisites](#prerequisites)
  - [Installing the chart](#installing-the-chart)
    - [Install chart using Helm v3.0+](#install-chart-using-helm-v30)
    - [Verify that controller pods are running properly.](#verify-that-controller-pods-are-running-properly)
  - [Configuration](#configuration)
<!-- /toc -->

### Installation

Quick start instructions for the setup and configuration of kueue using Helm.

#### Prerequisites

- [Helm](https://helm.sh/docs/intro/quickstart/#install-helm)

#### Installing the chart

##### Install chart using Helm v3.0+

```bash
$ git clone git@github.com:kubernetes-sigs/kueue.git
$ cd kueue/charts
$ helm install kueue kueue/ --create-namespace --namespace kueue-system
```

##### Verify that controller pods are running properly.

```bash
$ kubectl get deploy -n kueue-system
NAME                           READY   UP-TO-DATE   AVAILABLE   AGE
kueue-controller-manager       1/1     1            1           7s
```

### Configuration

The following table lists the configurable parameters of the kueue chart and their default values.

| Parameter                                   | Description                             |Default                                      |
|---------------------------------------------|-----------------------------------------|---------------------------------------------|
| `nameOverride`                              | override the resource name              | ``                                          |
| `fullnameOverride`                          | override the resource name              | ``                                          |
| `enablePrometheus`                          | enable Prometheus                       | `false`                                     |
| `enableCertManager`                         | enable CertManager                      | `false`                                     |
| `controllerManager.kubeRbacProxy.image`     | controllerManager.kubeRbacProxy's image | `gcr.io/kubebuilder/kube-rbac-proxy:v0.8.0` |
| `controllerManager.manager.image`           | controllerManager.manager's image       | `gcr.io/k8s-staging-kueue/kueue:main`       |
| `controllerManager.manager.resources`       | controllerManager.manager's resources   | abbr.                                       |
| `controllerManager.replicas`                | ControllerManager's replicaCount        | `1`                                         |
| `controllerManager.imagePullSecrets`        | ControllerManager's imagePullSecrets    | `[]`                                        |
| `kubernetesClusterDomain`                   | kubernetesCluster's Domain              | `cluster.local`                             |
| `managerConfig.controllerManagerConfigYaml` | controllerManagerConfigYaml             | abbr.                                       |
| `metricsService`                            | metricsService's ports                  | abbr.                                       |
| `webhookService`                            | webhookService's ports                  | abbr.                                       |