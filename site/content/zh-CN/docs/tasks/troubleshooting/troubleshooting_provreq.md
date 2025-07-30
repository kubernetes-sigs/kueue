---
title: "Kueue 中的 Provisioning Request 故障排除"
date: 2024-05-20
weight: 3
description: >
  Kueue 中 Provisioning Request 状态的故障排除
---

本文档帮助你排除 ProvisioningRequest 的故障，这是由
[ClusterAutoscaler](https://github.com/kubernetes/autoscaler/blob/4872bddce2bcc5b4a5f6a3d569111c11b8a2baf4/cluster-autoscaler/provisioningrequest/apis/autoscaling.x-k8s.io/v1beta1/types.go#L41)
定义的 API。

Kueue 通过 [Provisioning Admission Check Controller](/docs/admission-check-controllers/provisioning/)
创建 ProvisioningRequest，并将其视为[准入检查](/docs/concepts/admission_check/)。
为了让 Kueue 准入 Workload，为其创建的 ProvisioningRequest 需要成功。

## 开始之前 {#before-you-begin}

在开始故障排除之前，请确保你的集群满足以下要求：

- 你的集群已启用 ClusterAutoscaler，且 ClusterAutoscaler 支持 ProvisioningRequest API。
  检查你的云提供商文档以确定支持 ProvisioningRequest 的最低版本。
  如果你使用 GKE，你的集群应该运行版本 `1.28.3-gke.1098000` 或更新版本。
- 你使用支持 ProvisioningRequest 的节点类型。这可能因云提供商而异。
- Kueue 的版本是 `v0.5.3` 或更新版本。
- 你已在[特性门控配置](/docs/installation/#change-the-feature-gates-configuration)中启用了 `ProvisioningACC`。
  对于 Kueue `v0.7.0` 或更新版本，此特性门控默认启用。

## 识别你的作业对应的 Provisioning Request {#identifying-the-provisioning-request-for-your-job}

请参阅 [Job 故障排除指南](/docs/tasks/troubleshooting/troubleshooting_jobs/#identifying-the-workload-for-your-job)，
了解如何识别你的作业对应的 Workload。

你可以运行以下命令在 Workload 状态的 `admissionChecks` 字段中查看
Provisioning Request（和其他准入检查）的简要状态。

```bash
kubectl describe workload WORKLOAD_NAME
```

Kueue 使用有助于识别与你的 workload 对应的请求的命名模式创建 ProvisioningRequest。

```text
[YOUR WORKLOAD NAME]-[ADMISSION CHECK NAME]-[RETRY NUMBER]
```

例如：

```bash
sample-job-2zcsb-57864-sample-admissioncheck-1
```

当为你的作业配置节点时，Kueue 还会在 Workload 状态的 `.admissionChecks[*].podSetUpdate[*]` 字段中
添加注解 `cluster-autoscaler.kubernetes.io/consume-provisioning-request`。
此注解的值是 Provisioning Request 的名称。

`kubectl describe workload` 命令的输出应该类似于以下内容：

```bash
[...]
Status:
  Admission Checks:
    Last Transition Time:  2024-05-22T10:47:46Z
    Message:               Provisioning Request was successfully provisioned.
    Name:                  sample-admissioncheck
    Pod Set Updates:
      Annotations:
        cluster-autoscaler.kubernetes.io/consume-provisioning-request:  sample-job-2zcsb-57864-sample-admissioncheck-1
        cluster-autoscaler.kubernetes.io/provisioning-class-name:       queued-provisioning.gke.io
      Name:                                                             main
    State:                                                              Ready
```

## 我的 Provisioning Request 当前状态是什么？ {#what-is-the-current-state-of-my-provisioning-request}

你的作业未运行的一个可能原因是 ProvisioningRequest 正在等待配置。
要确定是否是这种情况，你可以通过运行以下命令查看 Provisioning Request 的状态：

```bash
kubectl get provisioningrequest PROVISIONING_REQUEST_NAME
```

如果是这种情况，输出应该类似于以下内容：

```bash
NAME                                                 ACCEPTED   PROVISIONED   FAILED   AGE
sample-job-2zcsb-57864-sample-admissioncheck-1       True       False         False    20s
```

你也可以通过运行以下命令查看 ProvisioningRequest 的更详细状态：

```bash
kubectl describe provisioningrequest PROVISIONING_REQUEST_NAME
```

如果你的 ProvisioningRequest 未能配置节点，错误输出可能类似于以下内容：

```bash
[...]
Status:
  Conditions:
    Last Transition Time:  2024-05-22T13:04:54Z
    Message:               Provisioning Request wasn't accepted.
    Observed Generation:   1
    Reason:                NotAccepted
    Status:                False
    Type:                  Accepted
    Last Transition Time:  2024-05-22T13:04:54Z
    Message:               Provisioning Request wasn't provisioned.
    Observed Generation:   1
    Reason:                NotProvisioned
    Status:                False
    Type:                  Provisioned
    Last Transition Time:  2024-05-22T13:06:49Z
    Message:               max cluster limit reached, nodepools out of resources: default-nodepool (cpu, memory)
    Observed Generation:   1
    Reason:                OutOfResources
    Status:                True
    Type:                  Failed
```

请注意，`Failed` 条件的 `Reason` 和 `Message` 值可能与你的输出不同，
这取决于阻止配置的原因。

Provisioning Request 状态在 `.conditions[*].status` 字段中描述。
空字段意味着 ProvisioningRequest 仍在被 ClusterAutoscaler 处理。
否则，它属于以下状态之一：

- `Accepted` - 表示 ProvisioningRequest 被 ClusterAutoscaler 接受，
  因此 ClusterAutoscaler 将尝试为其配置节点。
- `Provisioned` - 表示所有请求的资源都已创建并在集群中可用。
  当 VM 创建成功完成时，ClusterAutoscaler 将设置此条件。
- `Failed` - 表示无法获取资源来满足此 ProvisioningRequest。
  条件 Reason 和 Message 将包含有关失败原因的更多详细信息。
- `BookingExpired` - 表示 ProvisioningRequest 之前有 Provisioned 条件，
  但容量预留时间已过期。
- `CapacityRevoked` - 表示请求的资源不再有效。

状态转换如下：

![Provisioning Request's states](/images/prov-req-states.svg)

## 为什么没有创建 Provisioning Request？ {#why-a-provisioning-request-is-not-created}

如果 Kueue 没有为你的作业创建 Provisioning Request，请尝试检查以下要求：

### a. 确保 Kueue 的控制器管理器启用了 `ProvisioningACC` 特性门控 {#a-ensure-the-kueues-controller-manager-enables-the-provisioningacc-feature-gate}

运行以下命令检查你的 Kueue 控制器管理器是否启用了 `ProvisioningACC` 特性门控：

```bash
kubectl describe pod -n kueue-system kueue-controller-manager-
```

Kueue 容器的参数应该类似于以下内容：

```bash
    ...
    Args:
      --config=/controller_manager_config.yaml
      --zap-log-level=2
      --feature-gates=ProvisioningACC=true
```

请注意，对于 Kueue `v0.7.0` 或更新版本，该特性默认启用，因此你可能会看到不同的输出。

### b. 确保你的 Workload 已预留配额 {#b-ensure-your-workload-has-reserved-quota}

要检查你的 Workload 是否在 ClusterQueue 中预留了配额，请运行以下命令检查你的 Workload 状态：

```bash
kubectl describe workload WORKLOAD_NAME
```

输出应该类似于以下内容：

```bash
[...]
Status:
  Conditions:
    Last Transition Time:  2024-05-22T10:26:40Z
    Message:               Quota reserved in ClusterQueue cluster-queue
    Observed Generation:   1
    Reason:                QuotaReserved
    Status:                True
    Type:                  QuotaReserved
```

如果你得到的输出类似于以下内容：

```bash
  Conditions:
    Last Transition Time:  2024-05-22T08:48:47Z
    Message:               couldn't assign flavors to pod set main: insufficient unused quota for memory in flavor default-flavor, 4396Mi more needed
    Observed Generation:   1
    Reason:                Pending
    Status:                False
    Type:                  QuotaReserved
```

这意味着你的 ClusterQueue 中没有足够的可用配额。

你的 Workload 没有预留配额的其他原因可能与 LocalQueue/ClusterQueue 配置错误有关，例如：

```bash
Status:
  Conditions:
    Last Transition Time:  2024-05-22T08:57:09Z
    Message:               ClusterQueue cluster-queue doesn't exist
    Observed Generation:   1
    Reason:                Inadmissible
    Status:                False
    Type:                  QuotaReserved
```

你可以检查 ClusterQueue 和 LocalQueue 是否准备好准入你的 Workload。
有关更多详细信息，请参阅[队列故障排除](/docs/tasks/troubleshooting/troubleshooting_queues/)。

### c. 确保准入检查处于活跃状态 {#c-ensure-the-admission-check-is-active}

要检查你的作业使用的准入检查是否处于活跃状态，请运行以下命令：

```bash
kubectl describe admissionchecks ADMISSIONCHECK_NAME
```

其中 `ADMISSIONCHECK_NAME` 是在你的 ClusterQueue 规范中配置的名称。
有关更多详细信息，请参阅[准入检查文档](/docs/concepts/admission_check/)。

准入检查的状态应该类似于：

```bash
...
Status:
  Conditions:
    Last Transition Time:  2024-03-08T11:44:53Z
    Message:               The admission check is active
    Reason:                Active
    Status:                True
    Type:                  Active
```

如果上述步骤都无法解决你的问题，请通过
[Slack `wg-batch` 频道](https://kubernetes.slack.com/archives/C032ZE66A2X)联系我们。
