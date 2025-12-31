---
title: 弹性工作负载（Elastic Workloads）
date: 2025-12-19
weight: 4
description: >
  支持动态伸缩的工作负载类型。
---

{{< feature-state state="alpha" for_version="v0.13" >}}

## 弹性工作负载（Elastic Workloads / Workload Slices）

弹性工作负载（Elastic Workloads）在 Kueue 的核心`Workload`抽象之上进行了扩展，使**已准入（admitted）的作业**能够进行**动态伸缩**，而无需挂起或重新排队作业。
该能力通过 **Workload Slice（工作负载切片）** 实现。Workload Slice用于跟踪作业在扩容和缩容过程中的**部分资源分配**，从而支持作业规模的动态调整。

该特性能够根据集群容量的变化进行更加灵活和高效地调度作用，尤其适用于工作负载波动较大或资源受限的运行环境。

## 动态伸缩（Dynamic Scaling）

传统情况下，Kueue 中的一个`Workload`表示一个**原子性的准入单元**。
一旦被准入，它就对应一组固定数量的 Pod 实例，并消耗确定数量的资源配额。如果作业需要扩容或缩容，则必须将原有`Workload`挂起、删除或整体替换。

在动态伸缩场景下：
-   **缩容（Scale Down）** 相对简单，不需要额外的集群容量，也不需要创建新的 Workload Slice。
-   **扩容（Scale Up）** 则更为复杂，它需要请求**额外的资源容量**，并由 Kueue 通过创建并准入一个新的 **Workload Slice** 来完成。

**注意：**
虽然**缩容**在概念上与 [动态回收（Dynamic Reclaim）](https://kueue.sigs.k8s.io/docs/concepts/workload/#dynamic-reclaim) 类似，
但两者是**相互独立、正交的概念**，弹性工作负载的缩容既不依赖也不影响 Dynamic Reclaim 的功能。

## 使用场景（Use Cases）

-   为 [高度可并行（Embarrassingly Parallel）](https://en.wikipedia.org/wiki/Embarrassingly_parallel)的作业动态调整吞吐量。
-   使用支持弹性伸缩的 AI/ML 框架（例如 Distributed Torch Elastic）。

## 生命周期（Lifecycle）

1.  **初始准入（Initial Admission）**：作业提交后，系统会创建第一个`Workload`并完成准入。
2.  **扩容（Scaling Up）**：当作业请求更高的并行度时，会创建一个新的 Workload Slice，其Pod数量为**扩容后的规模**。该切片一旦被准入，原有的 Workload 会被标记为 `Finished`，由新的切片取而代之。
3.  **缩容（Scaling Down）**：当作业降低并行度时，会直接更新 Pod 数量，记录到现有的 Workload中，不会创建新的 Workload Slice。
4.  **抢占（Preemption）**：行为与现有的 Workload 抢占机制保持一致。
5.  **完成（Completion）**：行为与现有的 Workload 完成流程保持一致。

## 示例（Example）

## Kubernetes Job

{{< include "examples/jobs/sample-scalable-job.yaml" "yaml" >}}

上述示例将生成一个准入的 Workload，并启动 3 个运行中的 Pod。
只要作业仍处于 **Active** 状态（即尚未完成），其并行度就可以被动态地增加或减少。

## RayJob

参见：[运行 RayJob](/docs/tasks/run/rayjobs)

## 特性开关（Feature Gate）

弹性工作负载（Elastic Workloads / Workload Slices）通过特性开关控制：

``` yaml
ElasticJobsViaWorkloadSlices: true
```

此外，还必须在**每个作业**上通过注释显式启用弹性作业行为：

``` yaml
metadata:
  annotations:
    kueue.x-k8s.io/elastic-job: "true"
```

## 限制（Limitations）

* 当前仅支持以下工作负载类型：
    * `batch/v1.Job`
    * `ray.io/v1.RayJob`
    * `ray.io/v1.RayCluster`

* 弹性工作负载不支持包含**部分准入（partial admission）**的作业。

   * 对包含部分准入的作业尝试进行伸缩操作时，将触发类似如下的准入校验错误：

      ```text
      Error from server (Forbidden): error when applying patch:
      error when patching "job.yaml": admission webhook "vjob.kb.io" denied the request: spec.parallelism: Forbidden: cannot change when partial admission is enabled and the job is not suspended
      ```

* 对已准入作业进行扩容时，新创建的 Workload Slice 必须复用最初分配的 flavor，即使其他可用的 flavor 仍有剩余容量。

* 不支持 Multikueue。

*  不支持拓扑感知调度（Topology-Aware Scheduling，TAS）。
