---
title: "Installation"
linkTitle: "Installation"
weight: 2
description: >
  Installing Kueue to a Kubernetes Cluster
---

<!-- toc -->
- [Before you begin](#before-you-begin)
- [Install a released version](#install-a-released-version)
  - [Install by kubectl](#install-by-kubectl)
  - [Install by Helm](#install-by-helm)
  - [Add metrics scraping for prometheus-operator](#add-metrics-scraping-for-prometheus-operator)
  - [Add API Priority and Fairness configuration for the visibility API](#add-api-priority-and-fairness-configuration-for-the-visibility-api)
  - [Uninstall](#uninstall)
- [Install a custom-configured released version](#install-a-custom-configured-released-version)
- [Install the latest development version](#install-the-latest-development-version)
  - [Uninstall](#uninstall-1)
- [Build and install from source](#build-and-install-from-source)
  - [Add metrics scraping for prometheus-operator](#add-metrics-scraping-for-prometheus-operator-1)
  - [Uninstall](#uninstall-2)
- [Install via Helm](#install-via-helm)
- [Change the feature gates configuration](#change-the-feature-gates-configuration)
  - [Feature gates for alpha and beta features](#feature-gates-for-alpha-and-beta-features)
  - [Feature gates for graduated or deprecated features](#feature-gates-for-graduated-or-deprecated-features)
- [What's next](#whats-next)

<!-- /toc -->

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster with version 1.29 or newer is running. Learn how to [install the Kubernetes tools](https://kubernetes.io/docs/tasks/tools/).
- The kubectl command-line tool has communication with your cluster.

Kueue publishes [metrics](/docs/reference/metrics) to monitor its operators.
You can scrape these metrics with Prometheus.
Use [kube-prometheus](https://github.com/prometheus-operator/kube-prometheus)
if you don't have your own monitoring system.

The webhook server in kueue uses an internal cert management for provisioning certificates. If you want to use
  a third-party one, e.g. [cert-manager](https://github.com/cert-manager/cert-manager), follow the [cert manage guide](/docs/tasks/manage/installation).

[feature_gate]: https://kubernetes.io/docs/reference/command-line-tools-reference/feature-gates/

## Install a released version

### Install by kubectl

To install a released version of Kueue in your cluster by kubectl, run the following command:

```shell
kubectl apply --server-side -f https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "version" >}}/manifests.yaml
```

To wait for Kueue to be fully available, run:

```shell
kubectl wait deploy/kueue-controller-manager -nkueue-system --for=condition=available --timeout=5m
```

### Install by Helm

To install a released version of Kueue in your cluster by [Helm](https://helm.sh/), run the following command:

```shell
helm install kueue oci://registry.k8s.io/kueue/charts/kueue \
  --version={{< param "chart_version" >}} \
  --namespace  kueue-system \
  --create-namespace \
  --wait --timeout 300s
```

You can also use the following command:

```shell
helm install kueue https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "version" >}}/kueue-chart-{{< param "version" >}}.tgz \
  --namespace kueue-system \
  --create-namespace \
  --wait --timeout 300s
```

### Add metrics scraping for prometheus-operator

To allow [prometheus-operator](https://github.com/prometheus-operator/prometheus-operator)
to scrape metrics from kueue components, run the following command:

{{% alert title="Note" color="primary" %}}
This feature depends on [servicemonitor CRD](https://github.com/prometheus-operator/kube-prometheus/blob/main/manifests/setup/0servicemonitorCustomResourceDefinition.yaml), please ensure that CRD is installed first.

We can follow [Prometheus Operator Installing guide](https://prometheus-operator.dev/docs/getting-started/installation/) to install it.
{{% /alert %}}

```shell
kubectl apply --server-side -f https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "version" >}}/prometheus.yaml
```

### Add API Priority and Fairness configuration for the visibility API

See [Configure API Priority and Fairness](/docs/tasks/manage/monitor_pending_workloads/pending_workloads_on_demand/#configure-api-priority-and-fairness) for more details.

### Uninstall

To uninstall a released version of Kueue from your cluster by kubectl, run the following command:

```shell
kubectl delete -f https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "version" >}}/manifests.yaml
```

To uninstall a released version of Kueue from your cluster by Helm, run the following command:

```shell
helm uninstall kueue --namespace kueue-system
```

## Install a custom-configured released version

To install a custom-configured released version of Kueue in your cluster, execute the following steps:

1. Download the release's `manifests.yaml` file:

```shell
wget https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "version" >}}/manifests.yaml
```

2. With an editor of your preference, open `manifests.yaml`.
3. In the `kueue-manager-config` ConfigMap manifest, edit the
`controller_manager_config.yaml` data entry. The entry represents
the default [KueueConfiguration](/docs/reference/kueue-config.v1beta1).
The contents of the ConfigMap are similar to the following:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: kueue-manager-config
  namespace: kueue-system
data:
  controller_manager_config.yaml: |
    apiVersion: config.kueue.x-k8s.io/v1beta1
    kind: Configuration
    namespace: kueue-system
    health:
      healthProbeBindAddress: :8081
    metrics:
      bindAddress: :8443
      # enableClusterQueueResources: true
    webhook:
      port: 9443
    manageJobsWithoutQueueName: true
    internalCertManagement:
      enable: true
      webhookServiceName: kueue-webhook-service
      webhookSecretName: kueue-webhook-server-cert
    waitForPodsReady:
      enable: true
      timeout: 10m
    integrations:
      frameworks:
      - "batch/job"
```

__The `integrations.externalFrameworks` field is available in Kueue v0.7.0 and later.__

{{% alert title="Note" color="primary" %}}
See [All-or-nothing with ready Pods](/docs/tasks/manage/setup_wait_for_pods_ready) to learn
more about using `waitForPodsReady` for Kueue.
{{% /alert %}}

{{% alert title="Note" color="primary" %}}
Certain Kubernetes distributions might use batch/jobs to perform maintenance operations.
For these distributions, setting `manageJobsWithoutQueueName` to `true` without disabling the
`batch/job` integration may prevent system-created jobs from executing.
{{% /alert %}}

4. Apply the customized manifests to the cluster:

```shell
kubectl apply --server-side -f manifests.yaml
```

## Install the latest development version

To install the latest development version of Kueue in your cluster, run the
following command:

```shell
kubectl apply --server-side -k "github.com/kubernetes-sigs/kueue/config/default?ref=main"
```

The controller runs in the `kueue-system` namespace.

### Uninstall

To uninstall Kueue, run the following command:

```shell
kubectl delete -k "github.com/kubernetes-sigs/kueue/config/default?ref=main"
```

## Build and install from source

To build Kueue from source and install Kueue in your cluster, run the following
commands:

```sh
git clone https://github.com/kubernetes-sigs/kueue.git
cd kueue
IMAGE_REGISTRY=registry.example.com/my-user make image-local-push deploy
```

### Add metrics scraping for prometheus-operator

To allow [prometheus-operator](https://github.com/prometheus-operator/prometheus-operator)
to scrape metrics from kueue components, run the following command:

```shell
make prometheus
```

### Uninstall

To uninstall Kueue, run the following command:

```sh
make undeploy
```

## Install via Helm

To install and configure Kueue with [Helm](https://helm.sh/), follow the [instructions](https://github.com/kubernetes-sigs/kueue/blob/main/charts/kueue/README.md).

## Change the feature gates configuration

Kueue uses a similar mechanism to configure features as described in [Kubernetes Feature Gates](https://kubernetes.io/docs/reference/command-line-tools-reference/feature-gates).

In order to change the default of a feature, you need to edit the `kueue-controller-manager` deployment within the kueue installation namespace and change the `manager` container arguments to include

```
--feature-gates=...,<FeatureName>=<true|false>
```

For example, to enable `PartialAdmission`, you should change the manager deployment as follows:

```diff
kind: Deployment
...
spec:
  ...
  template:
    ...
    spec:
      containers:
      - name: manager
        args:
        - --config=/controller_manager_config.yaml
        - --zap-log-level=2
+       - --feature-gates=PartialAdmission=true
```

### Feature gates for alpha and beta features

| Feature                                       | Default | Stage | Since | Until |
|-----------------------------------------------|---------|-------|-------|-------|
| `FlavorFungibility`                           | `true`  | Beta  | 0.5   |       |
| `MultiKueue`                                  | `false` | Alpha | 0.6   | 0.8   |
| `MultiKueue`                                  | `true`  | Beta  | 0.9   |       |
| `MultiKueueBatchJobWithManagedBy`             | `false` | Alpha | 0.8   |       |
| `PartialAdmission`                            | `false` | Alpha | 0.4   | 0.4   |
| `PartialAdmission`                            | `true`  | Beta  | 0.5   |       |
| `ProvisioningACC`                             | `false` | Alpha | 0.5   | 0.6   |
| `ProvisioningACC`                             | `true`  | Beta  | 0.7   | 0.14  |
| `QueueVisibility`                             | `false` | Alpha | 0.5   | 0.9   |
| `VisibilityOnDemand`                          | `false` | Alpha | 0.6   | 0.8   |
| `VisibilityOnDemand`                          | `true`  | Beta  | 0.9   |       |
| `PrioritySortingWithinCohort`                 | `true`  | Beta  | 0.6   |       |
| `LendingLimit`                                | `false` | Alpha | 0.6   | 0.8   |
| `LendingLimit`                                | `true`  | Beta  | 0.9   |       |
| `TopologyAwareScheduling`                     | `false` | Alpha | 0.9   |       |
| `ConfigurableResourceTransformations`         | `false` | Alpha | 0.9   | 0.9   |
| `ConfigurableResourceTransformations`         | `true`  | Beta  | 0.10  |       |
| `ManagedJobsNamespaceSelector`                | `true`  | Beta  | 0.10  | 0.13  |
| `LocalQueueDefaulting`                        | `false` | Alpha | 0.10  | 0.11  |
| `LocalQueueDefaulting`                        | `true`  | Beta  | 0.12  |       |
| `LocalQueueMetrics`                           | `false` | Alpha | 0.10  |       |
| `HierarchicalCohort`                          | `true`  | Beta  | 0.11  |       |
| `ObjectRetentionPolicies`                     | `false` | Alpha | 0.12  | 0.12  |
| `ObjectRetentionPolicies`                     | `true`  | Beta  | 0.13  |       |
| `TASFailedNodeReplacement`                    | `false` | Alpha | 0.12  |       |
| `AdmissionFairSharing`                        | `false` | Alpha | 0.12  |       |
| `TASFailedNodeReplacementFailFast`            | `false` | Alpha | 0.12  |       |
| `TASReplaceNodeOnPodTermination`              | `false` | Alpha | 0.13  |       |
| `ElasticJobsViaWorkloadSlices`                | `false` | Alpha | 0.13  |       |
| `ManagedJobsNamespaceSelectorAlwaysRespected` | `false` | Alpha | 0.13  |       |
| `FlavorFungibilityImplicitPreferenceDefault`  | `false` | Alpha | 0.13  |       |

### Feature gates for graduated or deprecated features

| Feature                           | Default | Stage      | Since | Until |
| --------------------------------- | ------- | ---------- | ----- | ----- |
| `ManagedJobsNamespaceSelector`    | `true`  | GA         | 0.13  |       |
| `ProvisioningACC`                 | `true`  | GA         | 0.14  |       |
| `QueueVisibility`                 | `false` | Alpha      | 0.4   | 0.9   |
| `QueueVisibility`                 | `false` | Deprecated | 0.9   |       |
| `TASProfileMostFreeCapacity`      | `false` | Deprecated | 0.11  | 0.13  |
| `TASProfileLeastFreeCapacity`     | `false` | Deprecated | 0.11  |       |
| `TASProfileMixed`                 | `false` | Deprecated | 0.11  |       |

## What's next

- Read the [API reference](/docs/reference/kueue-config.v1beta1/#Configuration) for  `Configuration`
