---
title: "状态中的待处理工作负载"
date: 2024-09-23
weight: 30
description: >
  获取 ClusterQueue 和 LocalQueue 状态中的待处理工作负载。
---

本页面向你展示如何监控待处理的工作负载。

本页面的目标读者为[批处理管理员](/zh-cn/docs/tasks#batch-administrator)。

从 v0.5.0 版本开始，Kueue 提供了批处理管理员监控待处理作业流水线的特性，
并帮助用户估计他们的作业何时开始。

## 开始之前  {#before-you-begin}

确保满足以下条件：

- 一个 Kubernetes 集群正在运行。
- kubectl 命令行工具与你的集群通信。
- [安装了 Kueue](/zh-CN/docs/installation) v0.5.0 或更新版本。

## 启用 QueueVisibility 特性门控  {#enabling-feature-queuevisibility}

QueueVisibility 是一个默认禁用的 `Alpha` 特性门控，
请查阅[安装](/zh-CN/docs/installation/)部分中的
[更改特性门控配置](/zh-cn/docs/installation/#change-the-feature-gates-configuration)以获取详细信息。

## 监控待处理工作负载  {#monitor-pending-workloads}

{{< feature-state state="deprecated" for_version="v0.9" >}}

{{% alert title="警告" color="warning" %}}
此特性已被弃用，并将在 v1beta2 中删除。
请使用[按需待处理工作负载](/zh-cn/docs/tasks/manage/monitor_pending_workloads/pending_workloads_on_demand/)代替。
{{% /alert %}}

要安装简单的集群队列设置，请运行以下命令：

```shell
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/kueue/main/site/static/examples/admin/single-clusterqueue-setup.yaml
```

例如，让我们创建一个循环中的 10 个任务：

```shell
for i in {1..10}; do kubectl create -f https://raw.githubusercontent.com/kubernetes-sigs/kueue/main/site/static/examples/jobs/sample-job.yaml; done
```

要查看待处理的工作负载状态，运行以下命令：

```shell
kubectl describe clusterqueues.kueue.x-k8s.io
```

输出类似于以下内容：

```shell
Status:
  ...
  Pending Workloads:  10
  Pending Workloads Status:
    Cluster Queue Pending Workload:
      Name:            job-sample-job-gswhv-afff9
      Namespace:       default
      Name:            job-sample-job-v8dwc-ab5b7
      Namespace:       default
      Name:            job-sample-job-lrxbj-a9a91
      Namespace:       default
      Name:            job-sample-job-dj6nb-e6ef8
      Namespace:       default
      Name:            job-sample-job-6hdgw-bb26e
      Namespace:       default
      Name:            job-sample-job-2d268-693a7
      Namespace:       default
      Name:            job-sample-job-bfkd7-5e739
      Namespace:       default
      Name:            job-sample-job-8sbgz-506c6
      Namespace:       default
      Name:            job-sample-job-k2bmq-44616
      Namespace:       default
      Name:            job-sample-job-c724j-50fd2
      Namespace:       default
    Last Change Time:  2023-09-28T09:22:12Z
```

要配置队列可见性，
请按照如何[安装具有自定义管理器配置的 Kueue](/zh-cn/docs/installation/#install-a-custom-configured-released-version)
的说明进行操作。

`queueVisibility.clusterQueues.maxCount` 参数表示在 ClusterQueue
状态中公开的挂起工作负载的最大数量。
默认情况下，Kueue 会将此参数设置为 10。
当值设置为 0 时，则禁用 ClusterQueue 可见性更新。

```yaml
    queueVisibility:
      clusterQueues: 
        maxCount: 0
```

`queueVisibility.updateIntervalSeconds` 参数允许控制 Kueue 启动后快照更新的周期。
默认为 5 秒。
也可以在 Kueue 配置中更改此参数：

```yaml
    queueVisibility:
      updateIntervalSeconds: 5s
```
