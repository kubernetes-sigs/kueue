---
title: "配置 Admission Check Controller"
date: 2023-10-23
weight: 1
description: >
  一个为 kueue 提供与集群自动扩缩容器集成的 admission check controller。
---

Provisioning AdmissionCheck Controller 是一个 AdmissionCheck Controller，旨在将 Kueue 与 [Kubernetes cluster-autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler) 集成。其主要功能是为持有 [Quota Reservation](/docs/concepts/#quota-reservation) 的工作负载创建 [ProvisioningRequests](https://github.com/kubernetes/autoscaler/blob/4872bddce2bcc5b4a5f6a3d569111c11b8a2baf4/cluster-autoscaler/provisioningrequest/apis/autoscaling.x-k8s.io/v1beta1/types.go#L41)，并保持 [AdmissionCheckState](/docs/concepts/admission_check/#admissioncheckstate) 的同步。

该控制器是 Kueue 的一部分，默认启用。你可以通过编辑 `ProvisioningACC` 特性门控来禁用它。有关特性门控配置的详细信息，请查阅 [安装指南](/docs/installation/#change-the-feature-gates-configuration)。

Provisioning Admission Check Controller 支持 [Kubernetes cluster-autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler) 1.29 及更高版本。但某些云服务提供商可能尚未实现对其的支持。

在 [ClusterAutoscaler 文档](https://github.com/kubernetes/autoscaler/blob/master/cluster-autoscaler/FAQ.md#supported-provisioningclasses) 中查看受支持的 Provisioning Classes 及其前提条件。

## 用法

要使用 Provisioning AdmissionCheck，请创建一个 [AdmissionCheck](/docs/concepts/admission_check)
，并将 `.spec.controllerName` 设置为 `kueue.x-k8s.io/provisioning-request`，并使用 `ProvisioningRequestConfig` 对象创建 ProvisioningRequest 配置。

接下来，你需要按照 [Admission Check 用法](/docs/concepts/admission_check#usage) 中的说明，从 ClusterQueue 引用 AdmissionCheck。

完整设置请参见[下文](#setup)。

## ProvisioningRequest 配置

Kueue 为你的作业创建 ProvisioningRequests 有两种配置方式。

- **ProvisioningRequestConfig：** AdmissionCheck 中的此配置适用于所有通过此检查的作业。
  你可以设置 `provisioningClassName`、`managedResources` 和 `parameters`
- **作业注解**：此配置允许你为特定作业设置 `parameters`。如果注解和 ProvisioningRequestConfig 都设置了同一个参数，则注解的值优先生效。

### ProvisioningRequestConfig
`ProvisioningRequestConfig` 示例：

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

含义如下：
- **provisioningClassName** - 描述资源调配的不同模式。受支持的 ProvisioningClasses 列表见 [ClusterAutoscaler 文档](https://github.com/kubernetes/autoscaler/blob/master/cluster-autoscaler/FAQ.md#supported-provisioningclasses)，也可查阅云服务商文档了解其支持的其他 ProvisioningRequest 类别。
- **managedResources** - 包含由自动扩缩容器管理的资源列表。
- **retryStrategy.backoffLimitCount** - 指定 ProvisioningRequest 失败时的重试次数。默认值为 3。
- **retryStrategy.backoffBaseSeconds** - 提供计算重试等待时间的基数。默认值为 60。
- **retryStrategy.backoffMaxSeconds** - 指定重试前的最大等待时间（秒）。默认值为 1800。
- **podSetMergePolicy** - 允许将相似的 PodSet 合并为 ProvisioningRequest 使用的单个 PodTemplate。
- **podSetUpdates** - 允许根据成功的 ProvisioningRequest，使用 nodeSelector 更新 Workload 的 PodSets。
  这样可以将 PodSets 的 Pod 调度限制在新扩容的节点上。

#### PodSet 合并策略

{{% alert title="注意" color="primary" %}}
`podSetMergePolicy` 功能在 Kueue v0.12.0 及更高版本可用。

有两种选项：
- `IdenticalPodTemplates` - 仅合并完全相同的 PodTemplate
- `IdenticalWorkloadSchedulingRequirements` - 合并具有相同工作负载调度需求字段的 PodTemplate。被视为工作负载调度需求的 PodTemplate 字段包括：
  - `spec.containers[*].resources.requests`
  - `spec.initContainers[*].resources.requests`
  - `spec.resources`
  - `spec.nodeSelector`
  - `spec.tolerations`
  - `spec.affinity`
  - `resourceClaims`

当该字段未设置时，即使 PodTemplate 完全相同，也不会在创建 ProvisioningRequest 时合并。

例如，将该字段设置为 `IdenticalPodTemplates` 或 `IdenticalWorkloadSchedulingRequirements`，
可以在使用 PyTorchJob 时创建仅包含一个 PodTemplate 的 ProvisioningRequest，示例见：[`sample-pytorchjob.yaml`](/docs/tasks/run/kubeflow/pytorchjobs/#sample-pytorchjob)。
{{% /alert %}}

#### 重试策略

如果 ProvisioningRequest 失败，可以在退避一段时间后重试。
退避时间（秒）按以下公式计算，其中 `n` 为重试次数（从 1 开始）：

```latex
time = min(backoffBaseSeconds^n, backoffMaxSeconds)
```

当 ProvisioningRequest 失败时，为 Workload 保留的配额会被释放，Workload 需要重新开始准入流程。

#### PodSet 更新

为了将工作负载的 Pod 调度限制在新扩容的节点上，可以使用 "podSetUpdates" API，通过注入 nodeSelector 来实现。

例如：

```yaml
podSetUpdates:
  nodeSelector:
  - key: autoscaling.cloud-provider.com/provisioning-request
    valueFromProvisioningClassDetail: RequestKey
```

此 ProvisioningRequestConfig 片段指示 Kueue 在扩容后更新 Job 的 PodTemplate，
使其调度到带有标签 `autoscaling.cloud-provider.com/provisioning-request` 的新节点，
标签值来自 [ProvisiongClassDetails](https://github.com/kubernetes/autoscaler/blob/master/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1/types.go#L169) map 中 "RequestKey" 键对应的值。

注意，这要求所用的 provisioning class（可能与云服务商相关）支持在新扩容节点上设置唯一的节点标签。

#### 参考

更多详情请查阅 [API 定义](https://github.com/kubernetes-sigs/kueue/blob/main/apis/kueue/v1beta1/provisioningrequestconfig_types.go)。

### 作业注解

另一种传递 ProvisioningRequest [参数](https://github.com/kubernetes/autoscaler/blob/0130d33747bb329b790ccb6e8962eedb6ffdd0a8/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1beta1/types.go#L115) 的方式是使用作业注解。所有以 ***provreq.kueue.x-k8s.io/*** 为前缀的注解都会直接传递给创建的 ProvisioningRequest。例如 `provreq.kueue.x-k8s.io/ValidUntilSeconds: "60"` 会将 `ValidUntilSeconds` 参数值设为 `60`。更多示例见下文。

一旦 Kueue 为你提交的作业创建了 ProvisioningRequest，修改作业中的注解值不会影响已创建的 ProvisioningRequest。

## 示例

### 设置

{{< include "examples/provisioning/provisioning-setup.yaml" "yaml" >}}

### 使用 ProvisioningRequest 的作业

{{< include "examples/provisioning/sample-job.yaml" "yaml" >}}
