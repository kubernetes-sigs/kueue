---
title: "安装"
linkTitle: "安装"
weight: 2
description: >
  将 Kueue 安装到 Kubernetes 集群
---

<!-- toc -->
- [开始之前](#before-you-begin)
- [安装正式版本](#install-a-released-version)
  - [通过 kubectl 安装](#install-by-kubectl)
  - [通过 Helm 安装](#install-by-helm)
  - [为 Prometheus-Operator 添加指标采集](#add-metrics-scraping-for-prometheus-operator)
  - [为可见性 API 添加 API 优先级和公平性配置](#add-api-priority-and-fairness-configuration-for-the-visibility-api)
  - [卸载](#uninstall)
- [安装自定义配置的正式版本](#install-a-custom-configured-released-version)
- [安装最新开发版本](#install-the-latest-development-version)
  - [卸载](#uninstall-1)
- [从源代码构建和安装](#build-and-install-from-source)
  - [为 Prometheus-Operator 添加指标采集](#add-metrics-scraping-for-prometheus-operator-1)
  - [卸载](#uninstall-2)
- [通过 Helm 安装](#install-via-helm)
- [更改特性配置](#change-the-feature-gates-configuration)
  - [Alpha 和 Beta 级别特性的特性门控](#feature-gates-for-alpha-and-beta-features)
  - [已毕业或已弃用特性的特性门控](#feature-gates-for-graduated-or-deprecated-features)
- [接下来是什么](#whats-next)

<!-- /toc -->

## 开始之前 {#before-you-begin}

确保满足以下条件：

- 运行版本为 1.29 或更新的 Kubernetes 集群。了解如何[安装 Kubernetes 工具](https://kubernetes.io/docs/tasks/tools/)。
- kubectl 命令行工具已与你的集群通信。

Kueue 发布[指标](/docs/reference/metrics)以监控其控制器组件。
你可以使用 Prometheus 采集这些指标。
如果你没有自己的监控系统，请使用 [kube-prometheus](https://github.com/prometheus-operator/kube-prometheus)。

Kueue 中的 Webhook 服务使用内部证书管理来配置证书。如果你想使用第三方证书，例如
[cert-manager](https://github.com/cert-manager/cert-manager)，请遵循[证书管理指南](/docs/tasks/manage/installation)。

[feature_gate]: https://kubernetes.io/docs/reference/command-line-tools-reference/feature-gates/

## 安装正式版本 {#install-a-released-version}

### 通过 kubectl 安装 {#install-by-kubectl}

要通过 kubectl 在集群中安装已发布的 Kueue 版本，请运行以下命令：

```shell
kubectl apply --server-side -f https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "version" >}}/manifests.yaml
```

要等待 Kueue 完全可用，请运行：

```shell
kubectl wait deploy/kueue-controller-manager -nkueue-system --for=condition=available --timeout=5m
```

### 通过 Helm 安装 {#install-by-helm}

要通过 [Helm](https://helm.sh/) 在集群中安装已发布的 Kueue 版本，请运行以下命令：

```shell
helm install kueue oci://registry.k8s.io/kueue/charts/kueue \
  --version={{< param "chart_version" >}} \
  --namespace  kueue-system \
  --create-namespace \
  --wait --timeout 300s
```

你还可以使用以下命令：

```shell
helm install kueue https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "version" >}}/kueue-chart-{{< param "version" >}}.tgz \
  --namespace kueue-system \
  --create-namespace \
  --wait --timeout 300s
```

### 为 Prometheus-Operator 添加指标采集 {#add-metrics-scraping-for-prometheus-operator}

要允许 [prometheus-operator](https://github.com/prometheus-operator/prometheus-operator)
从 Kueue 组件中采集指标，请运行以下命令：

{{% alert title="Note" color="primary" %}}
此功能依赖于 [servicemonitor CRD](https://github.com/prometheus-operator/kube-prometheus/blob/main/manifests/setup/0servicemonitorCustomResourceDefinition.yaml)，请确保首先安装了 CRD。

我们可以遵循 [Prometheus Operator 安装指南](https://prometheus-operator.dev/docs/getting-started/installation/)来安装它。
{{% /alert %}}

```shell
kubectl apply --server-side -f https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "version" >}}/prometheus.yaml
```

### 为可见性 API 添加 API 优先级和公平性配置 {#add-api-priority-and-fairness-configuration-for-the-visibility-api}

有关更多详细信息，请参阅[配置 API 优先级和公平性](/docs/tasks/manage/monitor_pending_workloads/pending_workloads_on_demand/#configure-api-priority-and-fairness)。

### 卸载 {#uninstall}

要通过 kubectl 从集群中卸载已发布的 Kueue 版本，请运行以下命令：

```shell
kubectl delete -f https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "version" >}}/manifests.yaml
```

要通过 Helm 从集群中卸载已发布的 Kueue 版本，请运行以下命令：

```shell
helm uninstall kueue --namespace kueue-system
```

## 安装自定义配置的正式版本 {#install-a-custom-configured-released-version}

要安装自定义配置的已发布版本的 Kueue，请执行以下步骤：

1. 下载发布的 `manifests.yaml` 文件：

```shell
wget https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "version" >}}/manifests.yaml
```

2. 使用你喜欢的编辑器打开 `manifests.yaml`。
3. 在 `kueue-manager-config` ConfigMap 清单中，编辑
  `controller_manager_config.yaml` 数据条目。该条目代表默认的
  [KueueConfiguration](/docs/reference/kueue-config.v1beta1)。
  ConfigMap 的内容类似于以下内容：

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

__`integrations.externalFrameworks` 字段在 Kueue v0.7.0 及更高版本中可用。__

{{% alert title="Note" color="primary" %}}
请参阅 [All-or-nothing 与就绪 Pod](/docs/tasks/manage/setup_wait_for_pods_ready) 以了解
有关为 Kueue 使用 `waitForPodsReady` 的更多信息。
{{% /alert %}}

{{% alert title="Note" color="primary" %}}
某些 Kubernetes 发行版可能会使用 batch/jobs 执行维护操作。
对于这些发行版，将 `manageJobsWithoutQueueName` 设置为 `true` 而不禁用
`batch/job` 集成可能会阻止系统创建的作业执行。
{{% /alert %}}

4. 将自定义清单应用于集群：

```shell
kubectl apply --server-side -f manifests.yaml
```

## 安装最新开发版本 {#install-the-latest-development-version}

要安装最新开发版本的 Kueue，请运行以下命令：

```shell
kubectl apply --server-side -k "github.com/kubernetes-sigs/kueue/config/default?ref=main"
```

控制器在 `kueue-system` 命名空间中运行。

### 卸载 {#uninstall-1}

要卸载 Kueue，请运行以下命令：

```shell
kubectl delete -k "github.com/kubernetes-sigs/kueue/config/default?ref=main"
```

## 从源代码构建和安装 {#build-and-install-from-source}

要从源代码构建 Kueue 并将其安装在你的集群中，请运行以下命令：

```sh
git clone https://github.com/kubernetes-sigs/kueue.git
cd kueue
IMAGE_REGISTRY=registry.example.com/my-user make image-local-push deploy
```

### 为 Prometheus-Operator 添加指标采集 {#add-metrics-scraping-for-prometheus-operator-1}

要允许 [prometheus-operator](https://github.com/prometheus-operator/prometheus-operator)
从 Kueue 组件中采集指标，请运行以下命令：

```shell
make prometheus
```

### 卸载 {#uninstall-2}

要卸载 Kueue，请运行以下命令：

```sh
make undeploy
```

## 通过 Helm 安装 {#install-via-helm}

要使用 [Helm](https://helm.sh/) 安装和配置 Kueue，请遵循[说明](https://github.com/kubernetes-sigs/kueue/blob/main/charts/kueue/README.md)。

## 更改特性门控配置 {#change-the-feature-gates-configuration}

Kueue 使用类似于
[Kubernetes 特性门控](https://kubernetes.io/docs/reference/command-line-tools-reference/feature-gates)
中描述的机制来配置功能。

为了更改功能的默认值，你需要编辑 kueue 安装命名空间中的 `kueue-controller-manager` 部署，
并更改 `manager` 容器参数以包括

```
--feature-gates=...,<FeatureName>=<true|false>
```

例如，要启用 `PartialAdmission`，你应该按如下方式更改管理器部署：

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

### Alpha 和 Beta 级别特性的特性门控 {#feature-gates-for-alpha-and-beta-features}

| 功能                                            | 默认值     | 阶段    | 起始版本 | 截止版本 |
|-----------------------------------------------|---------|-------|------|------|
| `FlavorFungibility`                           | `true`  | Beta  | 0.5  |      |
| `MultiKueue`                                  | `false` | Alpha | 0.6  | 0.8  |
| `MultiKueue`                                  | `true`  | Beta  | 0.9  |      |
| `MultiKueueBatchJobWithManagedBy`             | `false` | Alpha | 0.8  |      |
| `PartialAdmission`                            | `false` | Alpha | 0.4  | 0.4  |
| `PartialAdmission`                            | `true`  | Beta  | 0.5  |      |
| `ProvisioningACC`                             | `false` | Alpha | 0.5  | 0.6  |
| `ProvisioningACC`                             | `true`  | Beta  | 0.7  |      |
| `QueueVisibility`                             | `false` | Alpha | 0.5  | 0.9  |
| `VisibilityOnDemand`                          | `false` | Alpha | 0.6  | 0.8  |
| `VisibilityOnDemand`                          | `true`  | Beta  | 0.9  |      |
| `PrioritySortingWithinCohort`                 | `true`  | Beta  | 0.6  |      |
| `LendingLimit`                                | `false` | Alpha | 0.6  | 0.8  |
| `LendingLimit`                                | `true`  | Beta  | 0.9  |      |
| `TopologyAwareScheduling`                     | `false` | Alpha | 0.9  |      |
| `ConfigurableResourceTransformations`         | `false` | Alpha | 0.9  | 0.9  |
| `ConfigurableResourceTransformations`         | `true`  | Beta  | 0.10 |      |
| `ManagedJobsNamespaceSelector`                | `true`  | Beta  | 0.10 | 0.13 |
| `LocalQueueDefaulting`                        | `false` | Alpha | 0.10 | 0.11 |
| `LocalQueueDefaulting`                        | `true`  | Beta  | 0.12 |      |
| `LocalQueueMetrics`                           | `false` | Alpha | 0.10 |      |
| `HierarchicalCohort`                          | `true`  | Beta  | 0.11 |      |
| `ObjectRetentionPolicies`                     | `false` | Alpha | 0.12 | 0.12 |
| `ObjectRetentionPolicies`                     | `true`  | Beta  | 0.13 |      |
| `TASFailedNodeReplacement`                    | `false` | Alpha | 0.12 |      |
| `AdmissionFairSharing`                        | `false` | Alpha | 0.12 |      |
| `TASFailedNodeReplacementFailFast`            | `false` | Alpha | 0.12 |      |
| `TASReplaceNodeOnPodTermination`              | `false` | Alpha | 0.13 |      |
| `ElasticJobsViaWorkloadSlices`                | `false` | Alpha | 0.13 |      |
| `ManagedJobsNamespaceSelectorAlwaysRespected` | `false` | Alpha | 0.13 |      |
| `FlavorFungibilityImplicitPreferenceDefault`  | `false` | Alpha | 0.13 |      |

### 已毕业或已弃用特性的特性门控 {#feature-gates-for-graduated-or-deprecated-features}

| 功能 | 默认值 | 阶段 | 起始版本 | 截止版本 |
| --------------------------------- | ------- | ---------- | ----- | ----- |
| `ManagedJobsNamespaceSelector`    | `true`  | GA          | 0.13 |       |
| `QueueVisibility`                 | `false` | Alpha      | 0.4   | 0.9   |
| `QueueVisibility`                 | `false` | Deprecated | 0.9   |       |
| `WorkloadResourceRequestsSummary` | `false` | Alpha      | 0.9   | 0.10  |
| `WorkloadResourceRequestsSummary` | `true`  | Beta       | 0.10  | 0.11  |
| `WorkloadResourceRequestsSummary` | `true`  | GA         | 0.11  |       |
| `TASProfileMostFreeCapacity`      | `false` | Deprecated | 0.11  | 0.13  |
| `TASProfileLeastFreeCapacity`     | `false` | Deprecated | 0.11  |       |
| `TASProfileMixed`                 | `false` | Deprecated | 0.11  |       |

## 接下来是什么 {#whats-next}

- 阅读 [`Configuration` 的 API 参考](/docs/reference/kueue-config.v1beta1/#Configuration)
