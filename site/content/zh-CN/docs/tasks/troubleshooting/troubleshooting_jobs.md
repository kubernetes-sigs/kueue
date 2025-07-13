---
title: "Job 故障排除"
date: 2024-03-21
weight: 1
description: >
  Job 状态的故障排除
---

本文档介绍如何排除挂起的
[Kubernetes Jobs](https://kubernetes.io/docs/concepts/workloads/controllers/job/)
的故障，
但大多数想法可以推广到其他[支持的作业类型](/docs/tasks/run)。

请参阅 [Kueue 概览](/docs/overview/#high-level-kueue-operation)
以可视化协作运行 Job 的组件。

在本文档中，假设你的 Job 名为 `my-job`，位于 `my-namespace` 命名空间中。

## 识别你的 Job 对应的 Workload {#identifying-the-workload-for-your-job}

对于每个 Job，Kueue 创建一个 [Workload](/docs/concepts/workload) 对象来保存
Job 准入的详细信息，无论它是否被准入。

要找到 Job 对应的 Workload，你可以使用以下任一步骤：

* 你可以通过运行以下命令从 Job 事件中获取 Workload 名称：

  ```bash
  kubectl describe job -n my-namespace my-job
  ```

  相关事件将如下所示：

  ```text
    Normal  CreatedWorkload   24s   batch/job-kueue-controller  Created Workload: my-namespace/job-my-job-19797
  ```

* Kueue 在标签 `kueue.x-k8s.io/job-uid` 中包含源 Job 的 UID。
  你可以使用以下命令获取 workload 名称：

  ```bash
  JOB_UID=$(kubectl get job -n my-namespace my-job -o jsonpath='{.metadata.uid}')
  kubectl get workloads -n my-namespace -l "kueue.x-k8s.io/job-uid=$JOB_UID"
  ```

  输出如下所示：

  ```text
  NAME               QUEUE         RESERVED IN   ADMITTED   AGE
  job-my-job-19797   user-queue    cluster-queue True       9m45s
  ```

* 你可以列出与你的作业相同命名空间中的所有 workload，并识别
  匹配格式 `<api-name>-<job-name>-<hash>` 的那个。
  你可以运行如下命令：

  ```bash
  kubectl get workloads -n my-namespace | grep job-my-job
  ```

  输出如下所示：

  ```text
  NAME               QUEUE         RESERVED IN   ADMITTED   AGE
  job-my-job-19797   user-queue    cluster-queue True       9m45s
  ```

一旦你确定了 Workload 的名称，你可以通过运行以下命令获取所有详细信息：

```bash
kubectl describe workload -n my-namespace job-my-job-19797
```

## 我的 Job 使用什么 ResourceFlavors？ {#what-resourceflavors-is-my-job-using}

一旦你[识别了你的 Job 对应的 Workload](/docs/tasks/troubleshooting/troubleshooting_jobs/#identifying-the-workload-for-your-job)，
运行以下命令来获取你的 Workload 的所有详细信息：

```sh
kubectl describe workload -n my-namespace job-my-job-19797
```

输出应该类似于以下内容：

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: Workload
...
status:
  admission:
    clusterQueue: cluster-queue
    podSetAssignments:
    - count: 3
      flavors:
        cpu: default-flavor
        memory: default-flavor
      name: main
      resourceUsage:
        cpu: "3"
        memory: 600Mi
  ...
```

现在你可以清楚地看到你的 Job 使用什么 `ResourceFlavors`。

## 我的 Job 是否被暂停？ {#is-my-job-suspended}

要知道你的 Job 是否被暂停，查看 `.spec.suspend` 字段的值，
运行以下命令：

```bash
kubectl get job -n my-namespace my-job -o jsonpath='{.spec.suspend}'
```

当作业被暂停时，Kubernetes 不会为该 Job 创建任何 [Pods](https://kubernetes.io/docs/concepts/workloads/pods/)。
如果 Job 已经有 Pod 在运行，Kubernetes 会终止并删除这些 Pod。

Job 在 Kueue 尚未准入它或当它被抢占以容纳另一个作业时会被暂停。
要了解为什么你的 Job 被暂停，请检查相应的 Workload 对象。

## 我的 Job 是否被准入？ {#is-my-job-admitted}

如果你的 Job 没有运行，请检查 Kueue 是否已准入该 Workload。

了解 Job 是否被准入、正在等待或尚未尝试准入的起点是查看 Workload 状态。

运行以下命令获取 Workload 的完整状态：

```bash
kubectl get workload -n my-namespace my-workload -o yaml
```

### 已准入的 Workload {#admitted-workload}

如果你的 Job 被准入，Workload 应该有类似以下的状态：

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: Workload
...
status:
  ...
  conditions:
  - lastTransitionTime: "2024-03-19T20:49:17Z"
    message: Quota reserved in ClusterQueue cluster-queue
    reason: QuotaReserved
    status: "True"
    type: QuotaReserved
  - lastTransitionTime: "2024-03-19T20:49:17Z"
    message: The workload is admitted
    reason: Admitted
    status: "True"
    type: Admitted
```

### 等待中的 Workload {#pending-workload}

如果 Kueue 尝试准入 Workload，但由于缺乏配额而失败，
Workload 应该有类似以下的状态：

```yaml
status:
  conditions:
  - lastTransitionTime: "2024-03-21T13:43:00Z"
    message: 'couldn''t assign flavors to pod set main: insufficient quota for cpu
      in flavor default-flavor in ClusterQueue'
    reason: Pending
    status: "False"
    type: QuotaReserved
  resourceRequests:
    name: main
    resources:
      cpu:    3
      Memory: 600Mi
```

### 我的 ClusterQueue 是否具有作业所需的资源请求？ {#does-my-clusterqueue-have-the-resource-requests-that-the-job-requires}

当你提交具有资源请求的作业时，例如：

```bash
kubectl get jobs job-0-9-size-6 -o json | jq -r .spec.template.spec.containers[0].resources
```

输出：

```json
{
  "limits": {
    "cpu": "2"
  },
  "requests": {
    "cpu": "2"
  }
}
```

如果你的 ClusterQueue 没有对 `requests` 的定义，Kueue 无法准入该作业。
对于上述作业，你应该在 `resourceGroups` 下定义 `cpu` 配额。
定义 `cpu` 配额的 ClusterQueue 如下所示：

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "cluster-queue"
spec:
  namespaceSelector: {}
  resourceGroups:
  - coveredResources: ["cpu"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "cpu"
        nominalQuota: 40
```

有关更多信息，请参阅[资源组](https://kueue.sigs.k8s.io/docs/concepts/cluster_queue/#resource-groups)。

### 等待准入检查 {#pending-admission-check}

当 ClusterQueue 配置了[准入检查](/docs/concepts/admission_check)时，例如
[ProvisioningRequest](/docs/admission-check-controllers/provisioning) 或 [MultiKueue](/docs/concepts/multikueue)，
Workload 可能会保持类似以下的状态，直到准入检查通过：

```yaml
status:
  admission:
    clusterQueue: dws-cluster-queue
    podSetAssignments:
      ...
  admissionChecks:
  - lastTransitionTime: "2024-05-03T20:01:59Z"
    message: ""
    name: dws-prov
    state: Pending
  conditions:
  - lastTransitionTime: "2024-05-03T20:01:59Z"
    message: Quota reserved in ClusterQueue dws-cluster-queue
    reason: QuotaReserved
    status: "True"
    type: QuotaReserved
```

### 未尝试的 Workload {#unattempted-workload}

当使用具有 `StrictFIFO`
[`queueingStrategy`](/docs/concepts/cluster_queue/#queueing-strategy) 的
[ClusterQueue](/docs/concepts/cluster_queue) 时，Kueue 只尝试准入每个 ClusterQueue 的头部。
因此，如果 Kueue 没有尝试准入 Workload，Workload 状态可能不包含任何条件。

### 配置错误的 LocalQueue 或 ClusterQueue {#misconfigured-localqueue-or-clusterqueue}

如果你的 Job 引用了不存在的 LocalQueue，或者它引用的 LocalQueue 或 ClusterQueue
配置错误，Workload 状态将如下所示：

```yaml
status:
  conditions:
  - lastTransitionTime: "2024-03-21T13:55:21Z"
    message: LocalQueue user-queue doesn't exist
    reason: Inadmissible
    status: "False"
    type: QuotaReserved
```

请参阅[队列故障排除](/docs/tasks/troubleshooting/troubleshooting_queues)了解为什么
ClusterQueue 或 LocalQueue 不活跃。

## 我的 Job 是否被抢占？ {#is-my-job-preempted}

如果你的 Job 没有运行，并且你的 ClusterQueue 启用了[抢占](/docs/concepts/cluster_queue/#preemption)，
你应该检查 Kueue 是否抢占了 Workload。

```yaml
status:
  conditions:
  - lastTransitionTime: "2024-03-21T15:49:56Z"
    message: 'couldn''t assign flavors to pod set main: insufficient unused quota
      for cpu in flavor default-flavor, 9 more needed'
    reason: Pending
    status: "False"
    type: QuotaReserved
  - lastTransitionTime: "2024-03-21T15:49:55Z"
    message: Preempted to accommodate a higher priority Workload
    reason: Preempted
    status: "True"
    type: Evicted
  - lastTransitionTime: "2024-03-21T15:49:56Z"
    message: The workload has no reservation
    reason: NoReservation
    status: "False"
    type: Admitted
```

`Evicted` 条件显示 Workload 被抢占，状态为 `status: "True"` 的
`QuotaReserved` 条件显示 Kueue 已经尝试再次准入它，在这种情况下不成功。

## 我的 Job 的 Pod 是否在运行？ {#are-the-pods-of-my-job-running}

当 Job 没有被暂停时，Kubernetes 为此 Job 创建 Pod。
要检查有多少这些 Pod 被调度和运行，请检查 Job 状态。

运行以下命令获取 Job 的完整状态：

```bash
kubectl get job -n my-namespace my-job -o yaml
```

输出将类似于以下内容：

```yaml
...
status:
  active: 5
  ready: 4
  succeeded: 1
  ...
```

`active` 字段显示 Job 存在多少 Pod，`ready` 字段显示其中有多少正在运行。

要列出 Job 的所有 Pod，运行以下命令：

```bash
kubectl get pods -n my-namespace -l batch.kubernetes.io/job-name=my-job
```

输出将类似于以下内容：

```text
NAME             READY   STATUS    RESTARTS   AGE
my-job-0-nxmpx   1/1     Running   0          3m20s
my-job-1-vgms2   1/1     Running   0          3m20s
my-job-2-74gw7   1/1     Running   0          3m20s
my-job-3-d4559   1/1     Running   0          3m20s
my-job-4-pg75n   0/1     Pending   0          3m20s
```

如果一个或多个 Pod 卡在 `Pending` 状态，通过运行以下命令检查为 Pod 发出的最新事件：

```bash
kubectl describe pod -n my-namespace my-job-0-pg75n
```

如果 Pod 无法调度，输出将类似于以下内容：

```text
Events:
  Type     Reason             Age                From                Message
  ----     ------             ----               ----                -------
  Warning  FailedScheduling   13s (x2 over 15s)  default-scheduler   0/9 nodes are available: 9 node(s) didn't match Pod's node affinity/selector. preemption: 0/9 nodes are available: 9 Preemption is not helpful for scheduling.
  Normal   NotTriggerScaleUp  13s                cluster-autoscaler  pod didn't trigger scale-up:
```
