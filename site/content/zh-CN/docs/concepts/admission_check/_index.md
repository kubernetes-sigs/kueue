---
title: "准入检查"
linkTitle: "准入检查"
date: 2024-06-13
weight: 6
description: >
  允许内部或外部组件影响工作负载准入的机制。
no_list: true
---

AdmissionChecks 是一种机制，允许 Kueue 在接纳 Workload 之前考虑额外的标准。
在 Kueue 为 Workload 预留配额后，Kueue 并发运行 ClusterQueue 中配置的所有准入检查。
只有当所有 AdmissionCheck 都为 Workload 提供了正面信号时，Kueue 才能接纳该 Workload。

### API

AdmissionCheck 是一个非命名空间的 API 对象，用于定义有关准入检查的详细信息：

- `controllerName` - 标识处理 AdmissionCheck 的控制器，不一定是 Kubernetes Pod 或 Deployment 名称。不能为空。
- `retryDelayMinutes` （已弃用）- 指定在检查失败（转换为 False）后保持 Workload
  暂停的时间长度。之后检查状态变为 "Unknown"。默认是 15 分钟。
- `parameters` - 标识带有检查附加参数的配置。

一个 AdmissionCheck 对象如下所示：

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: AdmissionCheck
metadata:
  name: prov-test
spec:
  controllerName: kueue.x-k8s.io/provisioning-request
  parameters:
    apiGroup: kueue.x-k8s.io
    kind: ProvisioningRequestConfig
    name: prov-test-config
```

### 使用方法

一旦定义，AdmissionCheck 可以在[ClusterQueue 的规约](/docs/concepts/cluster_queue)中引用。
与队列关联的所有 Workload 在被接纳前都需要由 AdmissionCheck 的控制器进行评估。
类似于 `ResourceFlavors`，如果未找到 `AdmissionCheck` 或其控制器未将其标记为
`Active`，ClusterQueue 将被标记为 Inactive。

在 ClusterQueue 的 spec 中引用 AdmissionChecks 有两种方式：

- `.spec.admissionChecks` —— 是将为所有提交到 ClusterQueue 的 Workloads 运行的 AdmissionCheck 列表
- `.spec.admissionCheckStrategy` —— 包含 `admissionCheckStrategyRules` 列表，
  这为你提供了更大的灵活性。它允许你既为所有 Workloads 运行 AdmissionCheck，
  也可以将 AdmissionCheck 与特定 ResourceFlavor 关联。
  要指定 AdmissionCheck 应运行的 ResourceFlavor，请使用
  `admissionCheckStrategyRule.onFlavors` 字段，如果你想为所有 Workload 运行 AdmissionCheck，只需留空该字段即可。

上述字段中只能指定一个。

#### 示例

##### 使用 `.spec.admissionChecks`

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: "cluster-queue"
spec:
<...>
  admissionChecks:
  - sample-prov
```

##### 使用 `.spec.admissionCheckStrategy`

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: "cluster-queue"
spec:
<...>
  admissionChecksStrategy:
    admissionChecks:
    - name: "sample-prov"           # Name of the AdmissionCheck to be run
      onFlavors: ["default-flavor"] # This AdmissionCheck will only run for Workloads that use default-flavor
    - name: "sample-prov-2"         # This AdmissionCheck will run for all Workloads regardless of a used ResourceFlavor
```

### AdmissionCheck 状态

AdmissionCheckState 是特定 Workload 的 AdmissionCheck 状态的表示。
AdmissionCheckState 列于 Workload 的 `.status.admissionCheckStates` 字段中。

AdmissionCheck 可以处于以下状态之一：
- `Pending` - 检查尚未执行/未完成
- `Ready` - 检查已通过
- `Retry` - 当前无法通过检查，它将回退（可能允许其他尝试，释放配额）并重试。
- `Rejected` - 近期内检查不会通过。不值得重试。

具有 `Pending` AdmissionCheck 的 Workload 的状态类似于以下情况：

```yaml
status:
  admission:
    <...>
  admissionChecks:
  - lastTransitionTime: "2023-10-20T06:40:14Z"
    message: ""
    name: sample-prov
    podSetUpdates:
    - annotations:
        cluster-autoscaler.kubernetes.io/consume-provisioning-request: job-prov-job-9815b-sample-prov
      name: main
    state: Pending
  <...>
```

Kueue 确保 Workload 的 AdmissionCheckStates 列表与 Workload 的 ClusterQueue 列表保持同步。
当用户添加一个新的 AdmissionCheck 时，Kueue 将其以 `Pending` 状态添加到 Workload 的 AdmissionCheckState 中。
如果 Workload 已被接纳，添加新的 AdmissionCheck 不会驱逐该 Workload。

### 带有 AdmissionChecks 的 Workload 的接纳

一旦 Workload 的 `QuotaReservation` 条件设置为 `True`，并且所有 AdmissionCheck
都处于 `Ready` 状态，Workload 将变为 `Admitted`。

如果 Workload 的任意 AdmissionCheck 处于 `Retry` 状态：
- 如果已 `Admitted`，Workload 被驱逐 - Workload 在 `workload.Status.Condition`
  中将有一个 `Evicted` 条件，原因标记为 `AdmissionCheck`
- 如果 Workload 具有 `QuotaReservation`，它将会被释放。
- 发出 `EvictedDueToAdmissionCheck` 事件

如果 Workload 的任意 AdmissionCheck 处于 `Rejected` 状态：
- Workload 被停用 - [`workload.Spec.Active`](/docs/concepts/workload/#active) 设置为 `False`
- 如果已 `Admitted`，Workload 被驱逐 - Workload 在 `workload.Status.Condition` 中具有一个 `Evicted` 条件，原因标记为 `Deactivated`
- 如果 Workload 具有 `QuotaReservation`，它将会被释放。
- 发出 `AdmissionCheckRejected` 事件

## 接下来是什么？

- 阅读 [API 参考](/docs/reference/kueue.v1beta1/#kueue-x-k8s-io-v1beta1-AdmissionCheck)了解 `AdmissionCheck`
- 从内置的[准入检查控制器](/docs/concepts/admission_check/provisioning_request)学习更多
