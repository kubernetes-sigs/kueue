---
title: "开发 AdmissionCheck 控制器"
date: 2024-09-03
weight: 9
description: >
  开发 AdmissionCheck 控制器（ACC）。
---

准入检查控制器，以下简称 **ACC**，
是管理与其关联的准入检查（由 `spec.controllerName` 对应值指定）
及针对配置了这些准入检查的 ClusterQueues 排队的工作负载的组件。

阅读[准入检查](/zh-CN/docs/concepts/admission_check/)以从用户角度了解更多机制。

## 子组件

ACC 可以内置在 Kueue 中或运行在不同的 Kubernetes 控制器管理器中，
并且应当实现对准入检查和工作负载的协调器。

### AdmissionCheck 协调器

监控集群中的准入检查，并维护与其关联（通过 `spec.controllerName`）的准入检查的 `Active` 状态。

可选地，它可以监视自定义[参数](/docs/reference/kueue.v1beta1/#kueue-x-k8s-io-v1beta1-AdmissionCheckParametersReference)对象。

[资源调配准入检查控制器](/zh-cn/docs/admission-check-controllers/provisioning/)在
`pkg/controller/admissionchecks/provisioning/admissioncheck_reconciler.go` 中实现了此功能。

### 工作负载协调器

它负责根据 ACC 的自定义逻辑维护各个工作负载的 [AdmissionCheckStates](/zh-CN/docs/concepts/admission_check/#admissioncheckstates)。
它可以允许工作负载的准入、重新排队或使其失败。

[资源调配准入检查控制器](/zh-CN/docs/admission-check-controllers/provisioning/)在 `pkg/controller/admissionchecks/provisioning/controller.go`
中实现了此功能。

### 参数对象类型

可选地，你可以定义一个集群级别的对象类型来保存特定于你的实现的准入检查参数。
用户可以在 [spec.parameters](/zh-CN/docs/reference/kueue.v1beta1/#kueue-x-k8s-io-v1beta1-AdmissionCheckParametersReference)
中的准入检查定义里引用此类对象类型的实例。

例如，[资源调配准入检查控制器](/zh-CN/docs/admission-check-controllers/provisioning/)为此使用了
[ProvisioningRequestConfig](/zh-CN/docs/reference/kueue.v1beta1/#kueue-x-k8s-io-v1beta1-ProvisioningRequestConfig)。

## 工具代码

`pkg/util/admissioncheck` 提供了可与 AdmissionCheck 的参数及其他功能交互的帮助代码。

## 示例

目前，Kueue 中实现了两个 ACC：

- [资源调配准入检查控制器](/zh-CN/docs/admission-check-controllers/provisioning/) 在
  `pkg/controller/admissionchecks/provisioning` 中实现，
  并实现了 Kueue 与 [Kubernetes 集群自动伸缩器](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler)的集成，
  包括 [ProvisioningRequests](https://github.com/kubernetes/autoscaler/blob/4872bddce2bcc5b4a5f6a3d569111c11b8a2baf4/cluster-autoscaler/provisioningrequest/apis/autoscaling.x-k8s.io/v1beta1/types.go#L41)。
- [MultiKueue](/zh-CN/docs/concepts/multikueue/)，作为对多集群支持的实现，
  是作为一个准入检查控制器在 `/pkg/controller/admissionchecks/multikueue` 中实现的。
