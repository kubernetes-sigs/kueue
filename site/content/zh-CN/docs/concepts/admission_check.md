---
title: "准入检查（Admission Check）"
date: 2024-06-13
weight: 6
description: >
  允许内部或外部组件影响工作负载准入的机制。
---

AdmissionCheck（准入检查）是一种机制，允许 Kueue 在准入 Workload（工作负载）之前考虑额外的标准。
在 Kueue 为 Workload 预留配额后，会并发运行在 ClusterQueue 中配置的所有准入检查。
只有当所有 AdmissionChecks 都为 Workload 提供了正面信号时，Kueue 才能准入该 Workload。

### API {#api}

AdmissionCheck 是一个非命名空间的 API 对象，用于定义准入检查的详细信息：

- `controllerName` - 标识处理 AdmissionCheck 的控制器，不一定是 Kubernetes 的 Pod 或 Deployment 名称。不能为空。
- `retryDelayMinutes`（已弃用）- 指定在检查失败（状态变为 False）后，保持工作负载挂起的时间。之后检查状态会变为 "Unknown"。默认值为 15 分钟。
- `parameters` - 标识带有额外参数的配置。

AdmissionCheck 对象示例如下：
```yaml
apiVersion: kueue.x-k8s.io/v1beta1
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

### 用法 {#usage}

定义后，可以在 [ClusterQueue 的 spec](/docs/concepts/cluster_queue) 中引用 AdmissionCheck。所有与该队列关联的 Workload 都需要由 AdmissionCheck 的控制器评估后才能被准入。
类似于 `ResourceFlavors`，如果找不到 `AdmissionCheck` 或其控制器未将其标记为 `Active`，则 ClusterQueue 会被标记为 Inactive。

在 ClusterQueue 的 spec 中有两种方式引用 AdmissionChecks：

- `.spec.admissionChecks` - 是将为提交到 ClusterQueue 的所有工作负载运行的 AdmissionChecks 列表。
- `.spec.admissionCheckStrategy` - 封装了 admissionCheckStrategyRules 列表，为您提供更多灵活性。它允许您为所有工作负载运行 AdmissionCheck，或将 AdmissionCheck 与特定的 ResourceFlavor 关联。要指定 AdmissionCheck 应运行的 ResourceFlavors，请使用 admissionCheckStrategyRule.onFlavors 字段；如果您希望为所有工作负载运行 AdmissionCheck，只需将该字段留空。

上述两种字段只能同时指定其中之一。

#### 示例 {#examples}

##### 使用 `.spec.admissionChecks` {#example-admission-checks}

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "cluster-queue"
spec:
<...>
  admissionChecks:
  - sample-prov
```

##### 使用 `.spec.admissionCheckStrategy` {#example-admission-check-strategy} 

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "cluster-queue"
spec:
<...>
  admissionChecksStrategy:
    admissionChecks:
    - name: "sample-prov"           # 要运行的 AdmissionCheck 名称
      onFlavors: ["default-flavor"] # 该 AdmissionCheck 仅对使用 default-flavor 的 Workload 运行
    - name: "sample-prov-2"         # 该 AdmissionCheck 会对所有 Workload 运行，无论使用何种 ResourceFlavor
```


### AdmissionCheckStates（准入检查状态） {#admissioncheckstates}

AdmissionCheckState 表示特定 Workload 的 AdmissionCheck 状态。
AdmissionCheckStates 列在 Workload 的 `.status.admissionCheckStates` 字段中。

AdmissionCheck 可以处于以下状态之一：
- `Pending`（待处理）- 检查尚未执行或尚未完成
- `Ready`（已通过）- 检查已通过
- `Retry`（重试）- 检查当前无法通过，将进行退避（可能允许其他检查尝试，释放配额）并重试。
- `Rejected`（已拒绝）- 检查在短期内不会通过，不值得重试。

Workload 的 AdmissionChecks 处于 `Pending` 状态时的状态示例如下：
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

Kueue 确保 Workload 的 AdmissionCheckStates 列表与其 ClusterQueue 的 AdmissionChecks 列表保持同步。
当用户添加新的 AdmissionCheck 时，Kueue 会将其以 `Pending` 状态添加到 Workload 的 AdmissionCheckStates 中。
如果 Workload 已被准入，添加新的 AdmissionCheck 不会驱逐该 Workload。

### 带 AdmissionChecks 的 Workload 准入 {#admission-check-admission}

一旦 Workload 的 `QuotaReservation` 条件为 `True`，且所有 AdmissionChecks 状态均为 `Ready`，该 Workload 将变为 `Admitted`（已准入）。

如果 Workload 的任何 AdmissionCheck 处于 `Retry` 状态：
  - 若已 `Admitted`，则 Workload 会被驱逐 - Workload 的 `workload.Status.Condition` 中会有 `Evicted` 条件，`Reason` 为 `AdmissionCheck`
  - 若有 `QuotaReservation`，则会被释放。
  - 会触发 `EvictedDueToAdmissionCheck` 事件

如果 Workload 的任何 AdmissionCheck 处于 `Rejected` 状态：
  - Workload 会被停用 - [`workload.Spec.Active`](docs/concepts/workload/#active) 被设为 `False`
  - 若已 `Admitted`，则 Workload 会被驱逐 - `workload.Status.Condition` 中有 `Evicted` 条件，`Reason` 为 `Deactivated`
  - 若有 `QuotaReservation`，则会被释放。
  - 会触发 `AdmissionCheckRejected` 事件

## 接下来？ {#what-next}

- 阅读 `AdmissionCheck` 的 [API 参考](/docs/reference/kueue.v1beta1/#kueue-x-k8s-io-v1beta1-AdmissionCheck)
- 了解更多内置的 [Provisioning Admission Check Controller](/docs/admission-check-controllers/provisioning)
