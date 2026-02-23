---
title: "弹性工作负载"
date: 2025-04-16
weight: 4
description: >
  支持动态扩展的工作负载类型。
---

{{< feature-state state="alpha" for_version="v0.13" >}}

## 弹性工作负载（工作负载切片）

弹性工作负载扩展了 Kueue 中核心的 `Workload` 抽象，以支持已接受 Job 的**动态扩缩**，
无需暂停或重新排队。这是通过使用**工作负载切片**实现的，工作负载切片跟踪父 Job 扩缩操作的部分分配。

此特性使得能够更响应和高效地调度适应集群容量变化的 Job，特别是在工作负载波动或资源受限的环境中。

## 动态扩缩

传统上，Kueue 中的 `Workload` 表示单一原子性的准入单元。
一旦被接纳，它反映了一组固定的 Pod 副本并消耗定义的配额。
如果 Job 需要扩缩，`Workload` 必须被暂停、移除或完全替换。

尽管缩小工作负载相对简单且不需要额外的容量或新的工作负载切片，
但扩大工作负载则更为复杂。它需要**额外的容量**，
这些容量必须明确请求并通过一个新的 `Workload Slice` 由 Kueue 接纳。

**注意：** 尽管缩小在概念上类似于[动态回收](https://kueue.sigs.k8s.io/zh-cn/docs/concepts/workload/#dynamic-reclaim)，
但它是一个正交的概念，既不与也不依赖于动态回收功能。

## 使用场景

* 动态调整[易并行](https://en.wikipedia.org/wiki/Embarrassingly_parallel)
  Job 的吞吐量。
* 使用支持弹性的 AI/ML 框架（分布式 Torch Elastic）。

## 生命周期

1. **初始准入**：提交 Job 并创建和接纳其第一个 `Workload`。
2. **扩大**：如果 Job 请求更多的并行度，则创建具有**增加的**副本数的新切片。
   一旦被接纳，新切片通过将旧的工作负载标记为`Finished`来替换原始工作负载。
3. **缩小**：如果 Job 减少其并行度，则直接在现有工作负载中记录更新后的 Pod 数量。
4. **抢占**：遵循现有的工作负载抢占机制。
5. **完成**：遵循现有的工作负载完成行为。

## 示例

## Kubernetes Job

{{< include "examples/jobs/sample-scalable-job.yaml" "yaml" >}}

上面的例子将导致一个被接纳的工作负载和 3 个运行中的 Pod。
只要 Job 仍处于“活跃”状态（即未完成），就可以调整并行度（增加或减少）。

## RayJob

参见[运行 RayJob](/zh-cn/docs/tasks/run/rayjobs)

## 特性门控

通过工作负载切片实现的弹性工作负载由以下特性门控控制：

```yaml
ElasticJobsViaWorkloadSlices: true
```

此外，必须通过注解为每个 Job 显式启用弹性 Job 行为：

```yaml
metadata:
  annotations:
    kueue.x-k8s.io/elastic-job: "true"
```

## 不足之处

* 当前仅适用于以下工作负载：
    * `batch/v1.Job`
    * `ray.io/v1.RayJob`
    * `ray.io/v1.RayCluster`
* 对于启用了部分准入的 Job，不支持弹性工作负载。

    * 尝试扩缩启用了部分准入的 Job 将导致准入验证错误，类似于以下内容：

      ```text
      Error from server (Forbidden): error when applying patch:
      error when patching "job.yaml": admission webhook "vjob.kb.io" denied the request: spec.parallelism: Forbidden: cannot change when partial admission is enabled and the job is not suspended
      ```
* 扩大之前已接受的 Job 时，新工作负载必须重用最初分配的规格，即使有其他符合条件且有可用容量的规格。
* 不支持多队列。
* 不支持拓扑感知调度（TAS）。
