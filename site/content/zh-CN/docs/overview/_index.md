---
title: "概览"
linkTitle: "概览"
weight: 1
description: >
  为什么选择 Kueue?
---

Kueue 是一个 Kubernetes 原生系统，用于管理配额以及作业如何使用配额。
Kueue 决定作业何时应该等待，何时应该被接纳开始（即可以创建 Pod），
以及何时应该被抢占（即应该删除活跃的 Pod）。

## 为什么选择 Kueue {#why-use-kueue}

你可以在一个普通的 Kubernetes 集群之上安装 Kueue。Kueue 不会替换任何现有的 Kubernetes 组件。Kueue 与云环境兼容，在这些环境中：

*   计算资源是弹性的，可以扩展和缩减。
*   计算资源是异构的（在架构、可用性、价格等方面）。

Kueue API 允许你表达：

* 租户间公平共享的配额和策略。
* 资源可替代性：如果一种资源 Flavor 已被完全利用，Kueue 可以使用不同的 Flavor 接纳作业。

Kueue 的一个核心设计原则是避免重复 Kubernetes 组件和成熟的第三方控制器中的功能。
自动扩缩、Pod 到节点的调度和作业生命周期管理分别是 cluster-autoscaler、kube-scheduler
和 kube-controller-manager 的职责。高级准入控制可以委托给像 Gatekeeper 这样的控制器。

## 功能概览 {#features-overview}

- **作业管理：** 支持基于[优先级](/docs/concepts/workload/#priority)的作业排队，
  并提供不同的[策略](/docs/concepts/cluster_queue/#queueing-strategy)：`StrictFIFO` 和 `BestEffortFIFO`。
- **高级资源管理：** 包括：[资源 Flavor 可替代性(resource flavor fungibility)](/docs/concepts/cluster_queue/#flavorfungibility)、
  [公平共享(Fair Sharing)](/docs/concepts/preemption/#fair-sharing)、
  [队列组(Cohorts)](/docs/concepts/cohort)和[抢占(preemption)](/docs/concepts/cluster_queue/#preemption)，
  并为不同租户提供多种策略。
- **集成：** 内置支持流行的作业，例如[BatchJob](/docs/tasks/run/jobs/)、
  [Kubeflow 训练作业](/docs/tasks/run/kubeflow/)、[RayJob](/docs/tasks/run/rayjobs/)、
  [RayCluster](/docs/tasks/run/rayclusters/)、[JobSet](/docs/tasks/run/jobsets/)、
  [AppWrappers](/docs/tasks/run/appwrappers/)、[普通 Pod 和 Pod 组](/docs/tasks/run/plain_pods/)。
- **系统洞察：** 内置 [Prometheus 指标](/docs/reference/metrics/)帮助监控系统状态，并提供按需可见性端点用于[监控待处理工作负载](/docs/tasks/manage/monitor_pending_workloads/pending_workloads_on_demand/)。
- **准入检查(AdmissionChecks)：** 一种供内部或外部组件影响工作负载是否可以被[接纳](/docs/concepts/admission_check/)的机制。
- **高级自动扩缩支持：** 通过准入检查与 cluster-autoscaler 的 [provisioningRequest](/docs/admission-check-controllers/provisioning/#job-using-a-provisioningrequest) 集成。
- **All-or-nothing 与就绪 Pod：** 一种基于超时的 [All-or-nothing 调度](/docs/tasks/manage/setup_wait_for_pods_ready/)实现。
- **部分接纳(Partial admission)和动态回收(dynamic reclaim)：** 基于可用配额以[减少并行度](/docs/tasks/run/jobs/#partial-admission)运行作业，
  并在 Pod 完成后[释放](/docs/concepts/workload/#dynamic-reclaim)配额的机制。
- **混合训练和推理：** 同时管理批处理工作负载和服务工作负载（例如 [Deployments](/docs/tasks/run/deployment/) 或
  [StatefulSets](/docs/tasks/run/statefulset/)）。
- **多集群作业分发：** 称为[MultiKueue](/docs/concepts/multikueue/)，允许搜索容量并从主集群卸载工作。
- **拓扑感知调度：** 允许通过[感知数据中心拓扑的调度](/docs/concepts/topology_aware_scheduling/)来优化 Pod 间的通信吞吐量。

## 作业集成功能 {#job-integrated-features}

| 功能 | Batch&nbsp;Job | JobSet | PaddleJob | PytorchJob | TFJob | XGBoostJob | MPIJob | JAXJob | Pod | RayCluster | RayJob | AppWrapper | Deployment | StatefulSet | LeaderWorkerSet |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| [动态回收](/docs/concepts/workload/#dynamic-reclaim) | + | + | | | | | | | + | | | | | | |
| [MultiKueue](/docs/concepts/multikueue/) | + | + | + | + | + | + | + | + | | + | + | + | | | |
| [MultiKueueBatchJobWithManagedBy](/docs/concepts/multikueue/#multikueuebatchjobwithmanagedby-enabled) | + | | | | | | | | | | | | | | |
| [部分接纳](/docs/tasks/run/jobs/#partial-admission) | + | | | | | | | | | | | | | | |
| [工作负载优先级类别](/docs/concepts/workload_priority_class/) | + | + | + | + | + | + | + | + | + | + | + | + | + | + | + |
| [Flavor 可替代性](/docs/concepts/cluster_queue/#flavorfungibility) | + | + | + | + | + | + | + | + | + | + | + | + | + | + | + |
| [ProvisioningACC](/docs/admission-check-controllers/provisioning/) | + | + | + | + | + | + | + | + | + | + | + | + | + | + | + |
| [队列可见性](/docs/tasks/manage/monitor_pending_workloads/pending_workloads_in_status/) | + | + | + | + | + | + | + | + | + | + | + | + | + | + | + |
| [按需可见性](/docs/tasks/manage/monitor_pending_workloads/pending_workloads_on_demand/) | + | + | + | + | + | + | + | + | + | + | + | + | + | + | + |
| [队列组内优先级排序](/docs/concepts/cluster_queue/#flavors-and-borrowing-semantics) | + | + | + | + | + | + | + | + | + | + | + | + | + | + | + |
| [借用限制](/docs/concepts/cluster_queue/#lendinglimit) | + | + | + | + | + | + | + | + | + | + | + | + | + | + | + |
| [All-or-nothing 与就绪 Pod](/docs/concepts/workload/#all-or-nothing-semantics-for-job-resource-assignment) | + | + | + | + | + | + | + | + | + | + | + | + | + | + | + |
| [公平共享](/docs/concepts/preemption/#fair-sharing) | + | + | + | + | + | + | + | + | + | + | + | + | + | + | + |
| [拓扑感知调度](/docs/concepts/topology_aware_scheduling) | + | + | + | + | + | + | + | + | + | + | + | + | + | + | + |

## Kueue 高级操作 {#high-level-kueue-operation}

![Kueue 高级操作](/images/theory-of-operation.svg)

要了解有关 Kueue 概念的更多信息，请参阅[概念](/docs/concepts)部分。

要了解不同的 Kueue 用户角色以及如何使用 Kueue，请参阅[任务](/docs/tasks)部分。
