---
title: "删除 ClusterQueue 故障排除"
date: 2024-08-28
weight: 5
---

当 ClusterQueue 有活跃的 Workload 时，删除它可能需要额外的步骤。

本指南提供了安全删除 ClusterQueue 的清晰方法，解释了删除过程并提供了处理特定场景的替代方法。

在本示例中，假设你的 ClusterQueue 名为 `my-cq`。

## 理解 `kubectl delete clusterqueue` {#understanding-kubectl-delete-clusterqueue}

当你运行以下命令时：

```sh
kubectl delete clusterqueue my-cq
```

你正在启动删除 ClusterQueue 对象的操作。
但是，如果仍有 Workload 在使用该 ClusterQueue，命令可能会挂起或需要一些时间才能完成。

## 为什么会发生这种情况？ {#why-does-this-happen}

Kueue 为每个 Job 创建一个 [Workload](/docs/concepts/workload/) 对象来跟踪其准入状态。
如果 Workload 被准入，它会与特定的 ClusterQueue 关联。
Kueue 使用名为 `kueue.x-k8s.io/resource-in-use` 的
[finalizer](https://kubernetes.io/docs/concepts/overview/working-with-objects/finalizers/)
来防止在资源仍在使用时删除 ClusterQueue。
因此，具有此 finalizer 的 ClusterQueue 在释放所有资源之前无法被删除。

## 如何识别与 ClusterQueue 相关的 Workload？ {#how-to-identify-the-workloads-related-to-the-clusterqueue}

要查找链接到特定 ClusterQueue 的 Workload，你可以使用以下步骤：

### 使用 `kueuectl` {#using-kueuectl}

{{% alert title="注意" color="primary" %}}
如果你没有安装 `kueuectl`，请按照[安装指南](/docs/reference/kubectl-kueue/installation/)进行操作。
{{% /alert %}}

```shell
kueuectl list workload --clusterqueue my-cq -A
```

示例输出：

```shell
NAMESPACE   NAME                     JOB TYPE   JOB NAME       LOCALQUEUE   CLUSTERQUEUE    STATUS     POSITION IN QUEUE   AGE
default     my-job-job-4gk8s-7b737   job        my-job-4gk8s   my-lq        my-cq           ADMITTED                       27m
default     my-job-job-826hp-caa72   job        my-job-826hp   my-lq        my-cq           ADMITTED                       1h
```

### 使用 `kubectl` 和 `grep` {#using-kubectl-and-grep}

或者，你可以使用 `kubectl` 配合 `grep`：

```shell
kueuectl get workload -A | grep my-cq
```

示例输出：

```shell
default     my-job-4gk8s-7b737   my-lq   my-cq   True                  30m
default     my-job-826hp-caa72   my-lq   my-cq   True                  30m
```

{{% alert title="注意" color="primary" %}}
grep 命令只基于指定的模式过滤行。
如果 Workload 名称或 LocalQueue 名称包含与 ClusterQueue 相同的名称，
它可能会返回额外的 Workload。
{{% /alert %}}

## 如何停止 ClusterQueue？ {#how-to-stop-the-clusterqueue}

{{% alert title="注意" color="primary" %}}
如果没有为指定的 ClusterQueue 找到 Workload，你可以跳过停止步骤，直接删除 ClusterQueue。
{{% /alert %}}

### 使用 `kueuectl` 停止 {#using-kueuectl-to-stop-the-clusterqueue}

要停止 ClusterQueue 中的所有作业并防止新作业被它准入，运行以下命令：

```shell
kueuectl stop clusterqueue my-cq
```

### 使用 `kubectl edit` {#using-kubectl-edit}

或者，你可以通过编辑其配置来停止 ClusterQueue：

```shell
kubectl edit clusterqueue my-cq
```

在编辑器中，将 `stopPolicy` 值更改为 `HoldAndDrain`：

```yaml
spec:
   stopPolicy: HoldAndDrain
```

保存更改。这将停止与 ClusterQueue 关联的所有 workload。

## 如何删除 ClusterQueue？ {#how-to-delete-the-clusterqueue}

一旦 Workload 被停止，你可以删除 ClusterQueue：

```shell
kueuectl delete clusterqueue my-cq
```
