---
title: "运行 SparkApplication"
date: 2025-11-17
weight: 7
description: >
  运行 Kueue 调度的 SparkApplication
---

{{< feature-state state="alpha" for_version="v0.17" >}}

此页面展示了在运行 [Spark Operator](https://github.com/kubeflow/spark-operator)
SparkApplication 时，如何利用 Kueue 的调度和资源管理能力。

本指南适用于对 Kueue 有基本了解的[批处理用户](/docs/tasks#batch-user)。
欲了解更多信息，请参阅 [Kueue 概述](/docs/overview)。

{{% alert title="Note" color="primary" %}}
`SparkApplicationIntegration` 目前仍处于 Alpha 测试阶段，默认情况下处于禁用状态。

要启用它，需要激活 `SparkApplicationIntegration` 特性门控，并将 `sparkoperator.k8s.io/sparkapplication` 添加为允许的工作负载。

{{% /alert %}}

## 在你开始之前

在 Kueue 中启用 SparkApplication 集成。你可以[修改已安装版本的 Kueue 配置](/docs/installation#install-a-custom-configured-released-version)，以将 `sparkoperator.k8s.io/sparkapplication` 作为允许的工作负载。

启用 `SparkApplicationIntegration` 特性门控。有关特性门控配置的详细信息，请参阅[安装](/docs/installation/#change-the-feature-gates-configuration)指南。

检查[管理集群配额](/docs/tasks/manage/administer_cluster_quotas)，以获取初始集群设置的详细信息。

查阅 [Spark Operator 安装指南](https://www.kubeflow.org/docs/components/spark-operator/getting-started/#installation)。

{{% alert title="Note" color="primary" %}}
要使用 SparkApplication 集成，必须安装
[Spark Operator](https://github.com/kubeflow/spark-operator)
[v2.4.0](https://github.com/kubeflow/spark-operator/releases/tag/v2.4.0)
或以上版本。

请同时记住以下几点：
- 你需要激活将部署 SparkApplication 的命名空间，
  如[官方安装文档](https://www.kubeflow.org/docs/components/spark-operator/getting-started/#about-spark-job-namespaces)中所述。
- 你需要提前创建 spark 服务账号并附加适当的角色。
  详情请参考 [Spark Operator 的入门指南](https://www.kubeflow.org/docs/components/spark-operator/getting-started/#about-the-service-account-for-driver-pods)。
{{% /alert %}}

{{% alert title="Note" color="primary" %}}
要使用 SparkApplication，在 v0.8.1 之前的版本中，安装后需要重启 Kueue。
你可以通过运行以下命令来完成此操作：
`kubectl delete pods -l control-plane=controller-manager -n kueue-system`。
{{% /alert %}}

## Spark Operator 定义

### a. 队列选择

目标[本地队列](/docs/concepts/local_queue)应在 SparkApplication
配置的 `metadata.labels` 部分中指定。

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

{{% alert title="Note" color="primary" %}}
Kueue 仅对带有 `kueue.x-k8s.io/elastic-job: "true"` 注解的 SparkApplication 支持动态分配，参见下文的“动态分配”一节。
如果你在其他 SparkApplication 中设置了 `spec.dynamicAllocation.enabled=true`，Kueue 将在 Webhook 中拒绝此类资源。
{{% /alert %}}

### b. （可选）在 SparkApplication 中设置 Suspend 字段

```yaml
spec:
  suspend: true
```

默认情况下，Kueue 将通过 Webhook 将 `suspend`
设置为 true，并在 SparkApplication 被允许时取消挂起。

### c. 动态分配

Kueue 通过将 SparkApplication 作为[弹性工作负载](/zh-cn/docs/concepts/elastic_workload)运行来支持
[Spark 动态分配](https://spark.apache.org/docs/latest/job-scheduling.html#dynamic-resource-allocation)。
要启用它：

1. 启用 `ElasticJobsViaWorkloadSlices` 特性门控。
2. 为 SparkApplication 添加注解：

   ```yaml
   metadata:
     annotations:
       kueue.x-k8s.io/elastic-job: "true"
   ```
3. 在 SparkApplication 中启用动态分配：

   ```yaml
   spec:
     executor:
       instances: 1
     dynamicAllocation:
       enabled: true
       minExecutors: 0
       maxExecutors: 4
   ```

之后，在 Driver 持续运行期间，Kueue 会接纳 Spark Driver 添加或移除的 Executor：

- Kueue 会在 Executor 的 Pod 模板中添加 `kueue.x-k8s.io/elastic-job` 调度门控，
  因此 Driver 创建的 Executor 会保持门控状态，直到包含它们的 Workload 切片被接纳。Webhook 会拒绝移除该门控的 SparkApplication。
- 在 Driver 启动之前，Workload 请求 Driver 和 `spec.executor.instances`
  与 `spec.dynamicAllocation.minExecutors` 中较大者个 Executor。
  Driver 运行后，Executor 数量跟随 Spark Operator 在 `status.executorState` 中报告为 `PENDING`
  或 `RUNNING` 的 Executor，并保持在 `spec.dynamicAllocation.minExecutors`
  与 `spec.dynamicAllocation.maxExecutors` 之间。
- 当 Driver 添加 Executor 时，Kueue 会为更大的数量创建新的 Workload 切片，
  并在该切片被接纳后移除新 Executor 的调度门控。
  当 Driver 移除 Executor 时，Kueue 会降低已接纳 Workload 的数量并释放它们的配额。
- 扩容是原子的：新的 Executor 会保持门控状态，直到它们全部能放入配额。

在 Kueue 中使用动态分配有以下限制：

- 首次接纳不考虑 `spec.dynamicAllocation.initialExecutors`。当它更大时，
  多出的 Executor 会保持门控状态，直到下一个 Workload 切片被接纳。
- Spark Operator 对每个应用最多跟踪 `--max-tracked-executor-per-app` 个 Executor（默认 1000）。
  超出该限制的 Executor 不会被计数，因此会保持门控状态。
- Driver 可能会删除并重新创建处于门控状态超过 `spark.kubernetes.allocation.executor.timeout` 的 Executor。
  新的 Executor 同样带有门控。
- Kueue 仅从 `spec.dynamicAllocation` 读取动态分配设置，而不是从 `spec.sparkConf` 读取。

## SparkApplication 示例

{{< include "examples/jobs/sample-sparkapplication.yaml" "yaml" >}}
