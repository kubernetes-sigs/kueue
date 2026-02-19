---
title: "集群队列（ClusterQueue）"
date: 2023-03-14
weight: 3
description: >
  一个集群范围的资源对象，用于管理一组资源池，定义使用上限和公平共享规则。
---

ClusterQueue 是一个集群范围的对象，用于管理一组资源池，如 Pod、CPU、内存和硬件加速器。ClusterQueue 定义了：

- ClusterQueue 管理的[资源规格](/docs/concepts/resource_flavor)的配额，包括使用上限和消耗顺序。
- 集群中多个 ClusterQueue 之间的公平共享规则。

只有[批处理管理员](/docs/tasks#batch-administrator)才应创建 `ClusterQueue` 对象。

一个示例 ClusterQueue 如下所示：

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: "cluster-queue"
spec:
  namespaceSelector: {} # 匹配所有命名空间。
  resourceGroups:
  - coveredResources: ["cpu", "memory", "pods"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "cpu"
        nominalQuota: 9
      - name: "memory"
        nominalQuota: 36Gi
      - name: "pods"
        nominalQuota: 5
```

只有在以下条件全部满足时，该 ClusterQueue 才会接纳[工作负载](/docs/concepts/workload)：

- CPU 请求总和小于等于 9。
- 内存请求总和小于等于 36Gi。
- Pod 总数小于等于 5。

您可以将配额指定为[数量](https://kubernetes.io/docs/reference/kubernetes-api/common-definitions/quantity/)。

![Cohort](/images/cluster-queue.svg)

## 资源规格与资源 {#resource-flavors-and-resources}

在 ClusterQueue 中，你可以为多种**规格**定义配额，这些规格提供特定的[计算资源](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/#resource-types)
（如 CPU、内存、GPU、Pods 等）。

规格代表某种资源的不同变体（例如，不同型号的 GPU）。您可以通过 [ResourceFlavor 对象](/docs/concepts/resource_flavor)定义规格与节点组的映射关系。
在 ClusterQueue 中，您可以为每个规格所提供的资源分别设置配额。

在为 ClusterQueue 定义配额时，您可以设置以下值：
- `nominalQuota`：该资源在某一时刻可供 ClusterQueue 使用的数量。
- `borrowingLimit`：该 ClusterQueue 允许从同一[队列组](#cohort)中其他 ClusterQueue 未用名义配额中借用的最大配额数量。
- `lendingLimit`：当本 ClusterQueue 未使用其名义配额时，允许队列组内其他 ClusterQueue 借用的最大配额数量。

在称为[准入](/docs/concepts#admission)的流程中，Kueue 会为[工作负载 Pod 集合](/docs/concepts/workload#pod-sets)分配每个所需资源的规格。
Kueue 会优先分配 ClusterQueue `.spec.resourceGroups[*].flavors` 列表中第一个拥有足够未用 `nominalQuota` 的规格，无论是在本 ClusterQueue 还是其[队列组](#cohort)中。

{{% alert title="注意" color="primary" %}}
在 ClusterQueue 配额中使用 `pods` 资源名来限制可接纳的 Pod 数量。

资源名 `pods` 是[保留名](/docs/concepts/workload/#reserved-resource-names)，不能在 Pod 的 requests 字段中指定。
Kueue 会自动计算一个 Workload 需要的 Pod 数量。
{{% /alert %}}

### 资源组

当一个 ResourceFlavor 绑定到节点组、机器系列或虚拟机可用性策略时，通常要求所有与这些节点相关的资源（如 `cpu`、`memory` 和 GPU）在准入时分配到同一个规格。
要将两个或多个资源绑定到同一组规格，请将它们列在同一个资源组中。

如果希望为不同资源分配不同的规格，请将它们分别列在不同的资源组中。
这在某些资源不是直接与节点关联、可以通过网络动态挂载，或你希望独立追踪其配额时非常有用。

一个包含多个资源组的 ClusterQueue 示例如下：

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: "cluster-queue"
spec:
  namespaceSelector: {} # 匹配所有命名空间。
  resourceGroups:
  - coveredResources: ["cpu", "memory", "foo.com/gpu"]
    flavors:
    - name: "spot"
      resources:
      - name: "cpu"
        nominalQuota: 9
      - name: "memory"
        nominalQuota: 36Gi
      - name: "foo.com/gpu"
        nominalQuota: 50
    - name: "on-demand"
      resources:
      - name: "cpu"
        nominalQuota: 18
      - name: "memory"
        nominalQuota: 72Gi
      - name: "foo.com/gpu"
        nominalQuota: 100
  - coveredResources: ["bar.com/license"]
    flavors:
    - name: "pool1"
      resources:
      - name: "bar.com/license"
        nominalQuota: 10
    - name: "pool2"
      resources:
      - name: "bar.com/license"
        nominalQuota: 10
```

在上述示例中，`cpu`、`memory` 和 `foo.com/gpu` 属于同一个 resourceGroup，而 `bar.com/license` 属于另一个。

一个资源规格最多只能属于一个资源组。

## 命名空间选择器 {#namespace-selector}

您可以在 ClusterQueue 中限制哪些命名空间可以有工作负载被接纳，通过设置 [label selector](https://kubernetes.io/docs/reference/kubernetes-api/common-definitions/label-selector/#LabelSelector)
在 `.spec.namespaceSelector` 字段中。

要允许所有命名空间的工作负载，请将空选择器 `{}` 设置为
`spec.namespaceSelector` 字段。

有多种方法可以允许特定命名空间访问 Cluster Queue。使用 `matchLabels` 匹配工作负载到命名空间 `team-a` 的示例 `namespaceSelector` 如下：

```yaml
namespaceSelector:
  matchLabels:
    kubernetes.io/metadata.name: team-a
```

这里 `kubernetes.io/metadata.name: team-a` 指的是 Kubernetes 控制平面在所有命名空间上设置的不可变标签 `kubernetes.io/metadata.name`。标签的值是命名空间名称，在这种情况下是 `team-a`。

然而，`matchLabels` 可以采用任何与命名空间对象中存在的标签匹配的键。
例如，假设 `team-a` 和 `team-b` 在 cohort `team-a-b` 中，
并且两个命名空间上都存在用户定义的标签 `research-cohort: team-a-b`，如下所示：

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: team-a
  labels:
    research-cohort: team-a-b
```

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: team-b
  labels:
    research-cohort: team-a-b
```

允许两个命名空间向此 ClusterQueue 提交 Job 的 namespaceSelector 配置如下：

```yaml
namespaceSelector:
  matchLabels:
    research-cohort: team-a-b
```

另一种配置 `namespaceSelector` 的方法是使用 `matchExpressions`。请参阅
[Kubernetes 文档](https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/#resources-that-support-set-based-requirements) 
了解更多详细信息。

## 排队策略

您可以在 ClusterQueue 中使用
`.spec.queueingStrategy` 字段设置不同的排队策略。排队策略决定了工作负载在 ClusterQueue 中的顺序以及在失败的
[admission](/docs/concepts#admission) 尝试后如何重新排队。

以下是支持的排队策略：

- `StrictFIFO`：工作负载首先按 [优先级](/docs/concepts/workload#priority)排序，
  然后按创建时间 `.metadata.creationTimestamp` 排序。无法被接纳的旧工作负载会阻止新工作负载，即使新工作负载适合可用配额。
- `BestEffortFIFO`：工作负载按与 `StrictFIFO` 相同的方式排序。然而，
  无法被接纳的旧工作负载不会阻止新工作负载，只要新工作负载适合可用配额。

默认排队策略是 `BestEffortFIFO`。

## 队列组（Cohort） {#cohort}

ClusterQueues 可以分组为 **队列组**。属于
同一队列组的 ClusterQueues 可以从每个其他 ClusterQueue 借用未使用的配额。

要向队列组添加 ClusterQueue，请在
`.spec.cohort` 字段中指定 cohort 的名称。所有具有匹配 `spec.cohort` 的 ClusterQueues
都是 cohort 的一部分。如果 `spec.cohort` 字段为空，则 ClusterQueue
不属于任何 cohort，因此它不能从任何其他
ClusterQueue 借用配额。

### 规格与借用语义 {#flavor-and-borrowing-semantics}

当 ClusterQueue 是 cohort 的一部分时，Kueue 满足以下准入语义：

- 当分配规格时，Kueue 通过 ClusterQueue 的
  (`.spec.resourceGroups[*].flavors`). 对于每个规格，Kueue 尝试
  根据 ClusterQueue 为规格定义的配额和 cohort 中的未用配额来适应 Workload 的 pod set。
  如果 Workload 不适合，Kueue 评估列表中的下一个规格。
- Workload 的 pod set 资源适合在 ClusterQueue 中定义的规格，如果 Workload 的请求资源总和：
  1. 小于或等于规格在
     ClusterQueue 中的未用 `nominalQuota`；或
  2. 小于或等于规格在
     cohort 中的所有 ClusterQueues 的未用 `nominalQuota` 之和，并且
  3. 小于或等于规格在 ClusterQueue 中的未用 `nominalQuota + borrowingLimit`。
  在 Kueue 中，当 (2) 和 (3) 满足但未满足 (1) 时，这称为
  **borrowing quota**。
- ClusterQueue 只能借用其定义的规格配额。
- 对于 Workload 中的每个 pod set 资源，ClusterQueue 只能借用一个规格配额。

{{% alert title="注意" color="primary" %}}
在 cohort 中，Kueue 优先安排将适合 under `nominalQuota` 的工作负载。
默认情况下，如果多个工作负载需要 `borrowing`，Kueue 将尝试安排优先级更高的工作负载
[priority](/docs/concepts/workload#priority) 首先。
如果 feature gate `PrioritySortingWithinCohort=false` 设置，Kueue 将尝试安排最早的 `.metadata.creationTimestamp` 的工作负载。
{{% /alert %}}

你可以设置一个 [`flavorFungibility`](/docs/concepts/cluster_queue#flavorfungibility) 来影响一些规格选择和借用的语义。

### 借用示例 {#borrowing-example}

假设你创建了以下两个 ClusterQueues：

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: "team-a-cq"
spec:
  namespaceSelector: {} # 匹配所有命名空间。
  cohortName: "team-ab"
  resourceGroups:
  - coveredResources: ["cpu", "memory"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "cpu"
        nominalQuota: 9
      - name: "memory"
        nominalQuota: 36Gi
```

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: "team-b-cq"
spec:
  namespaceSelector: {} # 匹配所有命名空间。
  cohortName: "team-ab"
  resourceGroups:
  - coveredResources: ["cpu", "memory"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "cpu"
        nominalQuota: 12
      - name: "memory"
        nominalQuota: 48Gi
```

ClusterQueue `team-a-cq` 可以根据以下情况接纳工作负载：

- 如果 ClusterQueue `team-b-cq` 没有被接纳的工作负载，那么 ClusterQueue
  `team-a-cq` 可以接纳资源总和为 `12+9=21` CPUs 和
  `48+36=84Gi` 内存的工作负载。
- 如果 ClusterQueue `team-b-cq` 有待处理的工作负载，并且 ClusterQueue
  `team-a-cq` 的所有 `nominalQuota` 配额已用完，Kueue 将首先在
  ClusterQueue `team-b-cq` 中接纳工作负载，然后再接纳任何新工作负载
  `team-a-cq`。
  因此，Kueue 确保 `nominalQuota` 配额用于 `team-b-cq`。

### 借用限制 {#borrowinglimit}

要限制 ClusterQueue 可以从其他地方借用的资源量，你可以设置
`.spec.resourcesGroup[*].flavors[*].resource[*].borrowingLimit`
[quantity](https://kubernetes.io/docs/reference/kubernetes-api/common-definitions/quantity/) 字段。

例如，假设你创建了以下两个 ClusterQueues：

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: "team-a-cq"
spec:
  namespaceSelector: {} # 匹配所有命名空间。
  cohortName: "team-ab"
  resourceGroups:
  - coveredResources: ["cpu", "memory"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "cpu"
        nominalQuota: 9
        borrowingLimit: 1
```

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: "team-b-cq"
spec:
  namespaceSelector: {} # 匹配所有命名空间。
  cohortName: "team-ab"
  resourceGroups:
  - coveredResources: ["cpu", "memory"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "cpu"
        nominalQuota: 12
```

在这种情况下，因为我们在 ClusterQueue `team-a-cq` 中设置了 borrowingLimit，如果
ClusterQueue `team-b-cq` 没有被接纳的工作负载，那么 ClusterQueue `team-a-cq`
可以接纳资源总和为 `9+1=10` CPUs 的工作负载。

如果，对于给定的规格/资源，`borrowingLimit` 字段为空或 null，
ClusterQueue 可以借用所有名义配额从 cohort 中的所有 ClusterQueues。因此，对于上面列出的 yamls，`team-b-cq` 可以使用
最多 `12+9` CPUs。

### LendingLimit {#lendinglimit}

要限制 ClusterQueue 可以在 cohort 中借出的资源量，你可以设置
`.spec.resourcesGroup[*].flavors[*].resource[*].lendingLimit`
[quantity](https://kubernetes.io/docs/reference/kubernetes-api/common-definitions/quantity/) 字段。

{{< feature-state state="stable" for_version="v0.18" >}}

例如，假设你创建了以下两个 ClusterQueues：

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: "team-a-cq"
spec:
  namespaceSelector: {} # 匹配所有命名空间。
  cohortName: "team-ab"
  resourceGroups:
  - coveredResources: ["cpu"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "cpu"
        nominalQuota: 9
```

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: "team-b-cq"
spec:
  namespaceSelector: {} # 匹配所有命名空间。
  cohortName: "team-ab"
  resourceGroups:
  - coveredResources: ["cpu"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "cpu"
        nominalQuota: 12
        lendingLimit: 1
```

在这里，你在 ClusterQueue `team-b-cq` 中设置了 lendingLimit=1。这意味着
如果 ClusterQueue `team-b-cq` 中所有被接纳的工作负载的总
配额使用量低于 `nominalQuota`（小于或等于 `12-1=11` CPUs），
然后 ClusterQueue `team-a-cq` 可以接纳资源
总和为 `9+1=10` CPUs 的工作负载。

如果 `lendingLimit` 字段未指定，ClusterQueue 可以借出
所有资源。在这种情况下，`team-b-cq` 可以使用最多 `9+12` CPUs。

## 抢占 {#preemption}

当 ClusterQueue 或其 cohort 中没有足够的配额时，新进入的工作负载可以触发以前被接纳的工作负载的预留，基于
ClusterQueue 的策略。

一个配置 ClusterQueue 以启用预留的示例如下：

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: "team-a-cq"
spec:
  preemption:
    reclaimWithinCohort: Any
    borrowWithinCohort:
      policy: LowerPriority
      maxPriorityThreshold: 100
    withinClusterQueue: LowerPriority
```

上述字段如下：

- `reclaimWithinCohort` 确定是否可以预留
  cohort 中使用更多配额的 Workloads。可能的值是：
  - `Never`（默认）：不要预留 cohort 中的 Workloads。
  - `LowerPriority`：如果待处理的工作负载适合其 ClusterQueue 的配额，则仅预留 cohort 中优先级较低的 Workloads。
  - `Any`：如果待处理的工作负载适合其 ClusterQueue 的配额，则预留 cohort 中的任何 Workloads，无论优先级如何。

- `borrowWithinCohort` 确定是否可以预留
  Workloads 从其他 ClusterQueues 如果工作负载需要借用。
  只能配置 Classical Preemption，并且 __not__ with Fair Sharing。
  此字段需要指定 `policy` 子字段，可能的值：
  - `Never`（默认）：如果借用是必需的，不要预留 cohort 中的 Workloads。
  - `LowerPriority`：如果待处理的工作负载需要借用，则仅预留
    cohort 中优先级较低的 Workloads。
  此预留策略仅在 `reclaimWithinCohort` 启用时支持（与 `Never` 不同）。
  此外，仅在配置的优先级阈值内可以预留该场景中的工作负载。

- `withinClusterQueue` 确定是否可以预留
  待处理的工作负载，如果它不适合其 ClusterQueue 的配额，则可以预留
  ClusterQueue 中的活动 Workloads。可能的值是：
  - `Never`（默认）：不要预留 ClusterQueue 中的 Workloads。
  - `LowerPriority`：仅预留 ClusterQueue 中优先级较低的 Workloads。
  - `LowerOrNewerEqualPriority`：仅预留 ClusterQueue 中优先级较低或等于待处理工作负载的 Workloads。

请注意，新进入的工作负载可以预留 ClusterQueue 和 cohort 中的 Workloads。

阅读 [Preemption](/docs/concepts/preemption) 以了解 Kueue 实现以预留尽可能少的工作负载的启发式方法。

## FlavorFungibility {#flavorfungibility}

当 ResourceFlavor 中没有足够的名义配额资源时，新进入的工作负载可以借用
配额或预留 ClusterQueue 或 cohort 中的运行工作负载。

Kueue 按顺序评估 ClusterQueue 中的规格。你可以影响是否优先
预留或借用规格，在尝试适应 Workload 到下一个规格之前，设置 `flavorFungibility` 字段。

一个配置 ClusterQueue 以配置此行为的示例如下：

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: "team-a-cq"
spec:
  flavorFungibility:
    whenCanBorrow: TryNextFlavor
    whenCanPreempt: MayStopSearch
```

上述字段如下：

- `whenCanBorrow` 确定是否应该停止寻找更好的分配，如果工作负载可以通过借用当前 ResourceFlavor 获得足够的资源。可能的值是：
  - `MayStopSearch`（默认）：ClusterQueue 停止寻找更好的分配。
  - `TryNextFlavor`：ClusterQueue 尝试下一个 ResourceFlavor 以查看工作负载是否可以获得更好的分配。
  - `Borrow`（已弃用）。
- `whenCanPreempt` 确定是否应该尝试预留当前 ResourceFlavor 中的工作负载，然后再尝试下一个。可能的值是：
  - `MayStopSearch`：ClusterQueue 停止尝试预留当前 ResourceFlavor 并从下一个开始，如果预留失败。
  - `TryNextFlavor`（默认）：ClusterQueue 尝试下一个 ResourceFlavor 以查看工作负载是否适合 ResourceFlavor。
  - `Preempt`（已弃用）。

默认情况下，新进入的工作负载停止尝试下一个风味，如果工作负载可以获得足够的借用资源。
并且 Kueue 仅在 Kueue 确定剩余 ResourceFlavors 无法适应工作负载时触发预留。

请注意，每当时机允许且配置策略允许时，Kueue 避免预留，如果它可以借用 Workload 以适应。

## 停止策略 {#stoppolicy}

StopPolicy 允许集群管理员通过在 [spec](/docs/reference/kueue.v1beta1/#kueue-x-k8s-io-v1beta1-ClusterQueueSpec) 中设置其值来临时停止 ClusterQueue 中工作负载的接纳，如下所示：

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: "team-a-cq"
spec:
  stopPolicy: Hold
```

上述示例将停止 ClusterQueue 中新的工作负载接纳，同时允许已经接纳的工作负载完成。
`HoldAndDrain` 将具有类似的效果，但除此之外，它还会触发已接纳工作负载的驱逐。

如果设置为 `None` 或 `spec.stopPolicy` 被删除，ClusterQueue 将恢复正常接纳行为。

## AdmissionChecks {#admissionchecks}

AdmissionChecks 是一个机制，允许 Kueue 在接纳工作负载之前考虑其他标准。

例如，使用 admission checks 的 ClusterQueue 配置，请参阅 [Admission Checks](/docs/concepts/admission_check#usage)。

## 下一步是什么？ {#what-next}

- 创建 [local queues](/docs/concepts/local_queue)
- 如果你还没有，请创建 [resource flavors](/docs/concepts/resource_flavor)
- 学习如何 [administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas)
- 阅读 [API reference](/docs/reference/kueue.v1beta1/#kueue-x-k8s-io-v1beta1-ClusterQueue) for `ClusterQueue`
