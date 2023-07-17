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
- (Optional) [Cert-manager](https://cert-manager.io/docs/installation/)

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

| Parameter                                              | Description                                            | Default                                     |
|--------------------------------------------------------|--------------------------------------------------------|---------------------------------------------|
| `nameOverride`                                         | override the resource name                             | ``                                          |
| `fullnameOverride`                                     | override the resource name                             | ``                                          |
| `enablePrometheus`                                     | enable Prometheus                                      | `false`                                     |
| `enableCertManager`                                    | enable CertManager                                     | `false`                                     |
| `controllerManager.kubeRbacProxy.image`                | controllerManager.kubeRbacProxy's image                | `gcr.io/kubebuilder/kube-rbac-proxy:v0.8.0` |
| `controllerManager.manager.image`                      | controllerManager.manager's image                      | `gcr.io/k8s-staging-kueue/kueue:main`       |
| `controllerManager.manager.resources`                  | controllerManager.manager's resources                  | abbr.                                       |
| `controllerManager.replicas`                           | ControllerManager's replicaCount                       | `1`                                         |
| `controllerManager.imagePullSecrets`                   | ControllerManager's imagePullSecrets                   | `[]`                                        |
| `controllerManager.readinessProbe.initialDelaySeconds` | ControllerManager's readinessProbe initialDelaySeconds | `5`                                         |
| `controllerManager.readinessProbe.periodSeconds`       | ControllerManager's readinessProbe periodSeconds       | `10`                                        |
| `controllerManager.readinessProbe.timeoutSeconds`      | ControllerManager's readinessProbe timeoutSeconds      | `1`                                         |
| `controllerManager.readinessProbe.failureThreshold`    | ControllerManager's readinessProbe failureThreshold    | `3`                                         |
| `controllerManager.readinessProbe.successThreshold`    | ControllerManager's readinessProbe successThreshold    | `1`                                         |
| `controllerManager.livenessProbe.initialDelaySeconds`  | ControllerManager's livenessProbe initialDelaySeconds  | `15`                                        |
| `controllerManager.livenessProbe.periodSeconds`        | ControllerManager's livenessProbe periodSeconds        | `20`                                        |
| `controllerManager.livenessProbe.timeoutSeconds`       | ControllerManager's livenessProbe timeoutSeconds       | `1`                                         |
| `controllerManager.livenessProbe.failureThreshold`     | ControllerManager's livenessProbe failureThreshold     | `3`                                         |
| `controllerManager.livenessProbe.successThreshold`     | ControllerManager's livenessProbe successThreshold     | `1`                                         |
| `kubernetesClusterDomain`                              | kubernetesCluster's Domain                             | `cluster.local`                             |
| `managerConfig.controllerManagerConfigYaml`            | controllerManagerConfigYaml                            | abbr.                                       |
| `metricsService`                                       | metricsService's ports                                 | abbr.                                       |
| `webhookService`                                       | webhookService's ports                                 | abbr.                                       |
