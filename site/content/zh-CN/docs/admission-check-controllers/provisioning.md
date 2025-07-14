---
title: "Provisioning 准入检查控制器"
date: 2023-10-23
weight: 1
description: >
  一个提供 Kueue 与集群自动缩放器集成的准入检查控制器。
---

Provisioning 准入检查控制器(AdmissionCheck Controller)是一个专为将 Kueue 与
[Kubernetes 集群自动缩放器](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler)
集成而设计的准入检查控制器。其主要功能是为持有[配额预留](/docs/concepts/#quota-reservation)的工作负载创建
[ProvisioningRequests](https://github.com/kubernetes/autoscaler/blob/4872bddce2bcc5b4a5f6a3d569111c11b8a2baf4/cluster-autoscaler/provisioningrequest/apis/autoscaling.x-k8s.io/v1beta1/types.go#L41)，
并保持 [AdmissionCheckState](/docs/concepts/admission_check/#admissioncheckstate) 的同步。

该控制器是 Kueue 的一部分，默认启用。您可以通过编辑 `ProvisioningACC` 特性门控来禁用它。
有关特性门控配置的详细信息，请查看[安装](/docs/installation/#change-the-feature-gates-configuration)指南。

Provisioning 准入检查控制器支持
[Kubernetes 集群自动缩放器](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler)
1.29 及更高版本。但是，某些云提供商可能没有相应的实现。

请查看[集群自动缩放器文档](https://github.com/kubernetes/autoscaler/blob/master/cluster-autoscaler/FAQ.md#supported-provisioningclasses)
中支持的 Provisioning 类和先决条件列表。

## 使用方法 {#usage}

要使用 Provisioning 准入检查，需要创建一个 [AdmissionCheck](/docs/concepts/admission_check)，
将 `kueue.x-k8s.io/provisioning-request` 作为 `.spec.controllerName`，
并使用 `ProvisioningRequestConfig` 对象创建 ProvisioningRequest 配置。

接下来，您需要从 ClusterQueue 引用 AdmissionCheck，详见[准入检查使用](/docs/concepts/admission_check#usage)。

完整设置请参见[下文](#setup)。

## ProvisioningRequest 配置 {#provisioningrequest-configuration}

Kueue 为您的作业创建 ProvisioningRequests 有两种配置方式：

- **ProvisioningRequestConfig：** AdmissionCheck 中的此配置适用于通过此检查的所有作业。
  它允许您设置 `provisioningClassName`、`managedResources` 和 `parameters`
- **Job 注解：** 此配置允许您为特定作业设置 `parameters`。
  如果注解和 ProvisioningRequestConfig 都引用相同的参数，注解值优先。

### ProvisioningRequestConfig {#provisioningrequestconfig}

一个 `ProvisioningRequestConfig` 如下所示：

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ProvisioningRequestConfig
metadata:
  name: prov-test-config
spec:
  provisioningClassName: check-capacity.autoscaling.x-k8s.io
  managedResources:
  - nvidia.com/gpu
  retryStrategy:
    backoffLimitCount: 2
    backoffBaseSeconds: 60
    backoffMaxSeconds: 1800
  podSetMergePolicy: IdenticalWorkloadSchedulingRequirements
```

其中：

- **provisioningClassName** - 描述资源配置的不同模式。
  支持的 ProvisioningClasses 列在[集群自动缩放器文档](https://github.com/kubernetes/autoscaler/blob/master/cluster-autoscaler/FAQ.md#supported-provisioningclasses)中，
  也请查看您的云提供商文档以了解他们支持的其他 ProvisioningRequest 类。
- **managedResources** - 包含由自动缩放管理的资源列表。
- **retryStrategy.backoffLimitCount** - 指示在失败情况下 ProvisioningRequest 应重试多少次。
  默认为 3。
- **retryStrategy.backoffBaseSeconds** - 提供计算 ProvisioningRequest 重试前等待退避时间的基础。
  默认为 60。
- **retryStrategy.backoffMaxSeconds** - 指示重试 ProvisioningRequest 前的最大退避时间（以秒为单位）。
  默认为 1800。
- **podSetMergePolicy** - 允许将相似的 PodSets 合并为 ProvisioningRequest 使用的单个 PodTemplate。
- **podSetUpdates** - 允许根据成功的 ProvisioningRequest 使用 nodeSelectors 更新工作负载的 PodSets。
  这允许将 PodSets 的 pods 调度限制到新配置的节点。

#### PodSet 合并策略 {#podset-merge-policy}

{{% alert title="注意" color="primary" %}}
`podSetMergePolicy` 特性在 Kueue v0.12.0 版本或更新版本中可用。

它提供两个选项：

- `IdenticalPodTemplates` - 仅合并相同的 PodTemplates
- `IdenticalWorkloadSchedulingRequirements` - 合并具有相同字段的 PodTemplates，
  这些字段被认为是定义工作负载调度要求的。被视为工作负载调度要求的 PodTemplate 字段包括：
  - `spec.containers[*].resources.requests`
  - `spec.initContainers[*].resources.requests`
  - `spec.resources`
  - `spec.nodeSelector`
  - `spec.tolerations`
  - `spec.affinity`
  - `resourceClaims`

当未设置该字段时，即使相同，在创建 ProvisioningRequest 时也不会合并 PodTemplates。

例如，将字段设置为 `IdenticalPodTemplates` 或 `IdenticalWorkloadSchedulingRequirements`，
允许在使用 PyTorchJob 时创建具有单个 PodTemplate 的 ProvisioningRequest，
如此示例：[`sample-pytorchjob.yaml`](/docs/tasks/run/kubeflow/pytorchjobs/#sample-pytorchjob)。
{{% /alert %}}

#### 重试策略 {#retry-strategy}

如果 ProvisioningRequest 失败，它可能在退避期后重试。
退避时间（以秒为单位）使用以下公式计算，其中 `n` 是重试次数（从 1 开始）：

```latex
time = min(backoffBaseSeconds^n, backoffMaxSeconds)
```

当 ProvisioningRequest 失败时，为工作负载预留的配额会被释放，工作负载需要重新开始准入周期。

#### PodSet 更新 {#podset-updates}

为了将工作负载的 Pods 调度限制到新配置的节点，您可以使用 "podSetUpdates" API，
它允许注入节点选择器来针对这些节点。

例如：

```yaml
podSetUpdates:
  nodeSelector:
  - key: autoscaling.cloud-provider.com/provisioning-request
    valueFromProvisioningClassDetail: RequestKey
```

ProvisioningRequestConfig 中的此代码片段指示 Kueue 在配置后更新作业的 PodTemplate，
以针对具有标签 `autoscaling.cloud-provider.com/provisioning-request` 的新配置节点，
该标签的值来自 [ProvisiongClassDetails](https://github.com/kubernetes/autoscaler/blob/master/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1/types.go#L169)
映射中的 "RequestKey" 键。

请注意，这假设配置类（可能是云提供商特定的）支持在新配置的节点上设置唯一的节点标签。

#### 参考 {#reference}

有关更多详细信息，请查看
[API 定义](https://github.com/kubernetes-sigs/kueue/blob/main/apis/kueue/v1beta1/provisioningrequestconfig_types.go)。

### Job 注解 {#job-annotations}

传递 ProvisioningRequest 的[参数](https://github.com/kubernetes/autoscaler/blob/0130d33747bb329b790ccb6e8962eedb6ffdd0a8/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1beta1/types.go#L115)
的另一种方式是使用 Job 注解。每个带有 ***provreq.kueue.x-k8s.io/*** 前缀的注解都会直接传递给创建的 ProvisioningRequest。
例如，`provreq.kueue.x-k8s.io/ValidUntilSeconds: "60"` 将传递值为 `60` 的
`ValidUntilSeconds` 参数。
请参见下面的更多示例。

一旦 Kueue 为您提交的作业创建了 ProvisioningRequest，修改作业中注解的值将不会对 ProvisioningRequest 产生影响。

## 示例 {#examples}

### 设置 {#setup}

{{< include "examples/provisioning/provisioning-setup.yaml" "yaml" >}}

### 使用 ProvisioningRequest 的作业 {#job-using-a-provisioningrequest}

{{< include "examples/provisioning/sample-job.yaml" "yaml" >}}
