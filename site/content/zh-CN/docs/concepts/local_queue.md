---
title: "本地队列（Local Queue）"
date: 2022-03-14
weight: 4
description: >
  命名空间资源，用于将属于单个租户的紧密相关的工作负载进行分组。
---

`LocalQueue` 是一个命名空间对象，用于将属于单个命名空间的紧密相关的工作负载进行分组。
一个命名空间通常分配给组织的一个租户（团队或用户）。
`LocalQueue` 指向一个 [`ClusterQueue`](/zh-CN/docs/concepts/cluster_queue)，
并从中分配资源以运行其工作负载。

`LocalQueue` 定义如下所示：

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: LocalQueue
metadata:
  namespace: team-a 
  name: team-a-queue
spec:
  clusterQueue: cluster-queue 
```

用户将作业（Job）提交到 `LocalQueue`，而不是直接提交到 `ClusterQueue`。
租户可以通过列出其命名空间中的本地队列来发现他们可以向哪些队列提交作业。
该命令类似于以下内容：

```sh
kubectl get -n my-namespace localqueues
# 或者，使用别名 `queue` 或 `queues`
kubectl get -n my-namespace queues
```

`queue` 和 `queues` 是 `localqueue` 的别名。

## 下一步是什么？

- 通过本地队列启动[工作负载](/zh-CN/docs/concepts/workload)
- 阅读 `LocalQueue` 的 [API 参考](/zh-CN/docs/reference/kueue.v1beta1/#kueue-x-k8s-io-v1beta1-LocalQueue)
