---
title: "工作负载（Workload）"
date: 2022-02-14
weight: 5
description: 一个将会运行至完成的应用程序。它是 Kueue 中准入的单位。有时也被称为作业（job）。
---

_workload_（工作负载）是一个将会运行至完成的应用程序。它可以由一个或多个 Pod 组成，这些 Pod 可以松散或紧密耦合，作为一个整体完成某项任务。工作负载是 Kueue 中[准入](/docs/concepts#admission)的单位。

典型的工作负载可以用 [Kubernetes `batch/v1.Job`](https://kubernetes.io/docs/concepts/workloads/controllers/job/) 来表示。因此，我们有时会用“作业（job）”来指代任何工作负载，而当我们特指 Kubernetes API 时，则用 Job。

然而，Kueue 并不直接操作 Job 对象。相反，Kueue 管理代表任意工作负载资源需求的 Workload 对象。Kueue 会为每个 Job 对象自动创建一个 Workload，并同步决策和状态。

一个 Workload 的清单如下所示：

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: Workload
metadata:
  name: sample-job
  namespace: team-a
spec:
  active: true
  queueName: team-a-queue
  podSets:
  - count: 3
    name: main
    template:
      spec:
        containers:
        - name: container
          image: registry.k8s.io/e2e-test-images/agnhost:latest
          args: ["pause"]
          imagePullPolicy: Always
          resources:
            requests:
              cpu: "1"
              memory: 200Mi
        restartPolicy: Never
```
## Active

你可以通过设置 [Active](/docs/reference/kueue.v1beta1#kueue-x-k8s-io-v1beta1-WorkloadSpec) 字段来停止或恢复正在运行的工作负载。active 字段决定了工作负载是否可以被准入到队列中，或在已被准入后是否可以继续运行。
将 `.spec.Active` 从 true 改为 false 会导致正在运行的工作负载被驱逐，并且不会被重新入队。

## 队列名称

要指定你的 Workload 应该被入队到哪个 [LocalQueue](/docs/concepts/local_queue)，请在 `.spec.queueName` 字段中设置 LocalQueue 的名称。

## Pod 集合

一个工作负载可能由多个具有不同 pod 规范的 Pod 组成。

`.spec.podSets` 列表中的每一项代表一组同质的 Pod，并包含以下字段：

- `spec` 使用 [`v1/core.PodSpec`](https://kubernetes.io/docs/reference/kubernetes-api/workload-resources/pod-v1/#PodSpec) 描述 pod。
- `count` 是使用相同 `spec` 的 pod 数量。
- `name` 是 pod 集合的人类可读标识符。你可以使用 Pod 在 Workload 中的角色，如 `driver`、`worker`、`parameter-server` 等。

### 资源请求

Kueue 使用 `podSets` 的资源请求来计算工作负载使用的配额，并决定是否以及何时准入工作负载。

Kueue 计算工作负载的总资源用量为每个 `podSet` 资源请求的总和。`podSet` 的资源用量等于 pod 规范的资源请求乘以 `count`。

#### 请求值调整

根据集群设置，Kueue 会基于以下情况调整工作负载的资源用量：

- 如果集群在 [Limit Ranges](https://kubernetes.io/docs/concepts/policy/limit-range/) 中定义了默认值，则在 `spec` 未提供时会使用默认值。
- 创建的 pod 受 [Runtime Class Overhead](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-overhead/) 影响。
- 如果 spec 只定义了资源限制（limit），则会将 limit 值视为请求（request）。

#### 请求值校验

当集群定义了 Limit Ranges 时，上述调整后的值会根据范围进行校验。
如果范围校验失败，Kueue 会将工作负载标记为 `Inadmissible`（不可准入）。

#### 保留资源名称

除了常规的资源命名限制外，Pod 规范中不能使用 `pods` 资源名称，因为它被 Kueue 内部保留使用。你可以在 [ClusterQueue](/docs/concepts/cluster_queue#resources) 中使用 `pods` 资源名称来设置最大 pod 数量的配额。

## 优先级

工作负载有优先级，会影响 [ClusterQueue 准入顺序](/docs/concepts/cluster_queue#queueing-strategy)。
设置 Workload 优先级有两种方式：

- **Pod 优先级**：你可以在 `.spec.priority` 字段中看到 Workload 的优先级。
对于 `batch/v1.Job`，Kueue 会根据 Job 的 pod 模板的 [pod priority](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-priority-preemption/) 设置 Workload 的优先级。

- **WorkloadPriority**：有时开发者希望控制工作负载的优先级而不影响 pod 的优先级。
通过使用 [`WorkloadPriority`](/docs/concepts/workload_priority_class)，你可以独立管理工作负载的排队和抢占优先级，而不影响 pod 的优先级。

## 自定义工作负载

如前所述，Kueue 内置支持使用 Job API 创建的工作负载。但任何自定义工作负载 API 都可以通过为其创建相应的 Workload 对象来集成 Kueue。

## 动态回收（Dynamic Reclaim）

这是一种机制，允许当前已准入的工作负载释放其不再需要的部分配额预留（Quota Reservation）。

作业集成通过设置 `reclaimablePods` 状态字段来传递此信息，枚举每个 podset 不再需要配额预留的 pod 数量。

```yaml

status:
  reclaimablePods:
  - name: podset1
    count: 2
  - name: podset2
    count: 2

```
`count` 只能在工作负载持有配额预留期间增加。

## 作业资源分配的全有或全无语义

该机制允许 Job 在未就绪时被驱逐并重新入队。
详情请参阅 [All-or-nothing with ready Pods](/docs/tasks/manage/setup_wait_for_pods_ready/)。

### 指数退避重入队

一旦因 `PodsReadyTimeout` 原因发生驱逐，Workload 会以退避方式重新入队。
Workload 状态允许你了解以下信息：

- `.status.requeueState.count` 表示因 PodsReadyTimeout 驱逐而已退避重入队的次数
- `.status.requeueState.requeueAt` 表示 Workload 下次将被重新入队的时间

```yaml
status:
  requeueState:
    count: 5
    requeueAt: 2024-02-11T04:51:03Z
```

当因“全有或全无”机制被停用的 Workload 被重新激活时，requeueState（`.status.requeueState`）会被重置为 null。

## 从 Job 复制标签到 Workload
你可以配置 Kueue，在创建 Workload 时，将 Job 或 Pod 对象中的标签复制到新建的 Workload。这对于工作负载的识别和调试很有用。
你可以通过在配置 API（integrations 下）设置 `labelKeysToCopy` 字段来指定要复制哪些标签。默认情况下，Kueue 不会将任何 Job 或 Pod 标签复制到 Workload。

## 最长执行时间

你可以通过指定期望的最大运行秒数来配置 Workload 的最长执行时间：

```yaml
spec:
  maximumExecutionTimeSeconds: n
```

如果工作负载在 `Admitted` 状态下运行超过 `n` 秒（包括之前“准入/驱逐”周期中处于 Admitted 状态的时间），它会被自动停用。
一旦被停用，之前所有“准入/驱逐”周期中累计的活跃时间会被重置为 0。

如果未指定 `maximumExecutionTimeSeconds`，则工作负载没有执行时间限制。

你可以通过在任何受支持的 Kueue Job 关联的 Workload 上，设置 `kueue.x-k8s.io/max-exec-time-seconds` 标签来配置其 `maximumExecutionTimeSeconds`。



## 下一步

- 了解 [workload priority class](/docs/concepts/workload_priority_class)
- 学习如何 [运行作业](/docs/tasks/run/jobs)
- 阅读 [API 参考](/docs/reference/kueue.v1beta1/#kueue-x-k8s-io-v1beta1-Workload) 以了解 `Workload`
