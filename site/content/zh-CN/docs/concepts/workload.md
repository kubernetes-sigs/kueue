---
title: "工作负载(Workload)"
date: 2022-02-14
weight: 5
description: >
  将运行至完成的应用程序。它是 Kueue 中的准入单位。有时也称为作业。
---

_工作负载(Workload)_ 是将运行至完成的应用程序。它可以由一个或多个 Pod 组成，
这些 Pod 松散或紧密耦合，作为一个整体完成任务。工作负载是 Kueue 中的
[准入(admission)](/docs/concepts#admission)单位。

典型的工作负载可以用
[Kubernetes `batch/v1.Job`](https://kubernetes.io/docs/concepts/workloads/controllers/job/)
来表示。因此，我们有时使用_作业(job)_一词来指代任何工作负载，而 Job 则专门指
Kubernetes API。

但是，Kueue 不直接操作 Job 对象。相反，Kueue 管理代表任意工作负载资源需求的
Workload 对象。Kueue 自动为每个 Job 对象创建一个 Workload，并同步决策和状态。

Workload 的清单如下所示：

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

## 活跃状态 {#active}

您可以通过设置 [Active](/docs/reference/kueue.v1beta1#kueue-x-k8s-io-v1beta1-WorkloadSpec)
字段来停止或恢复正在运行的工作负载。active 字段决定工作负载是否可以被准入到队列中
或继续运行（如果已经被准入）。
将 `.spec.Active` 从 true 更改为 false 将导致正在运行的工作负载被驱逐且不会
重新排队。

## 队列名称 {#queue-name}

要指示您希望将工作负载排入哪个[本地队列(LocalQueue)](/docs/concepts/local_queue)，
请在 `.spec.queueName` 字段中设置本地队列的名称。

## Pod 集合 {#pod-sets}

工作负载可能由具有不同 Pod 规格的多个 Pod 组成。

`.spec.podSets` 列表的每个项目代表一组同质 Pod，并具有以下字段：

- `spec` 使用 [`v1/core.PodSpec`](https://kubernetes.io/docs/reference/kubernetes-api/workload-resources/pod-v1/#PodSpec)
  描述 Pod。
- `count` 是使用相同 `spec` 的 Pod 数量。
- `name` 是 Pod 集合的人类可读标识符。您可以使用 Pod 在工作负载中的角色，
  如 `driver`、`worker`、`parameter-server` 等。

### 资源请求 {#resource-requests}

Kueue 使用 `podSets` 资源请求来计算工作负载使用的配额，并决定是否以及何时准入
工作负载。

Kueue 将工作负载的总资源使用量计算为每个 `podSet` 资源请求的总和。`podSet` 的
资源使用量等于 Pod 规格的资源请求乘以 `count`。

#### 请求值调整 {#request-value-adjustment}

根据集群设置，Kueue 将基于以下因素调整工作负载的资源使用量：

- 集群在[限制范围(Limit Ranges)](https://kubernetes.io/docs/concepts/policy/limit-range/)
  中定义默认值，如果 `spec` 中未提供，将使用默认值。
- 创建的 Pod 受到[运行时类开销(Runtime Class Overhead)](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-overhead/)
  的影响。
- 规格仅定义资源限制，这种情况下限制值将被视为请求。

#### 请求值验证 {#request-value-validation}

在集群定义限制范围的情况下，上述调整后的值将根据范围进行验证。
如果范围验证失败，Kueue 会将工作负载标记为 `Inadmissible`。

#### 保留的资源名称 {#reserved-resource-names}

除了通常的资源命名限制外，您不能在 Pod 规格中使用 `pods` 资源名称，因为它是为
Kueue 内部使用而保留的。您可以在[集群队列(ClusterQueue)](/docs/concepts/cluster_queue#resources)
中使用 `pods` 资源名称来设置 Pod 最大数量的配额。

## 优先级 {#priority}

工作负载具有影响[集群队列准入顺序](/docs/concepts/cluster_queue#queueing-strategy)的
优先级。有两种设置工作负载优先级的方法：

- **Pod 优先级**：您可以在 `.spec.priority` 字段中查看工作负载的优先级。
  对于 `batch/v1.Job`，Kueue 根据 Job 的 Pod 模板的
  [Pod 优先级](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-priority-preemption/)
  设置工作负载的优先级。

- **WorkloadPriority**：有时开发人员希望控制工作负载的优先级而不影响 Pod 的优先级。
  通过使用 [`WorkloadPriority`](/docs/concepts/workload_priority_class)，
  您可以独立管理工作负载的排队和抢占优先级，与 Pod 的优先级分离。

## 自定义工作负载 {#custom-workload}

如前所述，Kueue 内置支持使用 Job API 创建的工作负载。但是任何自定义工作负载 API
都可以通过为其创建相应的 Workload 对象来与 Kueue 集成。

## 动态回收 {#dynamic-reclaim}

这是一种允许当前已准入工作负载释放不再需要的部分配额预留的机制。

作业集成通过设置 `reclaimablePods` 状态字段来传达此信息，枚举每个 Pod 集合中
不再需要配额预留的 Pod 数量。

```yaml

status:
  reclaimablePods:
  - name: podset1
    count: 2
  - name: podset2
    count: 2

```

只有当工作负载持有配额预留时，`count` 才能增加。

## 作业资源分配的全有或全无语义 {#all-or-nothing-semantics-for-job-resource-assignment}

此机制允许在作业未准备就绪时被驱逐并重新排队。
请参考[全有或全无与就绪 Pod](/docs/tasks/manage/setup_wait_for_pods_ready/)
了解更多详情。

### 指数退避重新排队 {#exponential-backoff-requeueing}

一旦发生 `PodsReadyTimeout` 原因的驱逐，工作负载将以退避方式重新排队。
工作负载状态允许您了解以下信息：

- `.status.requeueState.count` 指示工作负载因 PodsReadyTimeout 原因驱逐而已
  退避重新排队的次数
- `.status.requeueState.requeueAt` 指示工作负载下次重新排队的时间

```yaml
status:
  requeueState:
    count: 5
    requeueAt: 2024-02-11T04:51:03Z
```

当由于全有或全无与就绪 Pod 而被停用的工作负载重新激活时，
requeueState (`.status.requeueState`) 将重置为 null。

## 从作业复制标签到工作负载 {#replicate-labels-from-jobs-into-workloads}

您可以配置 Kueue 在工作负载创建时，从底层 Job 或 Pod 对象将标签复制到新的
工作负载中。这对于工作负载识别和调试很有用。
您可以通过在配置 API 中设置 `labelKeysToCopy` 字段（在 `integrations` 下）
来指定应该复制哪些标签。默认情况下，Kueue 不会将任何 Job 或 Pod 标签复制到
工作负载中。

## 最大执行时间 {#maximum-execution-time}

您可以通过指定工作负载预期运行的最大秒数来配置其最大执行时间：

```yaml
spec:
  maximumExecutionTimeSeconds: n
```

如果工作负载在 `Admitted` 状态下花费超过 `n` 秒，包括在之前的"准入/驱逐"循环中
作为 `Admitted` 状态花费的时间，它会自动被停用。
一旦停用，在之前的"准入/驱逐"循环中作为活跃状态累积的时间将设置为 0。

如果未指定 `maximumExecutionTimeSeconds`，则工作负载没有执行时间限制。

您可以通过在作业的 `kueue.x-k8s.io/max-exec-time-seconds` 标签中指定所需值
来配置任何支持的 Kueue 作业关联的工作负载的 `maximumExecutionTimeSeconds`。

## 下一步 {#whats-next}

- 了解[工作负载优先级类](/docs/concepts/workload_priority_class)。
- 了解如何[运行作业](/docs/tasks/run/jobs)
- 阅读 `Workload` 的 [API 参考](/docs/reference/kueue.v1beta1/#kueue-x-k8s-io-v1beta1-Workload)
