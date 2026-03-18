---
title: "运行 SparkApplication"
date: 2025-11-17
weight: 7
description: >
  运行 Kueue 调度的 SparkApplication
---

{{< feature-state state="alpha" for_version="v0.17" >}}

{{% alert title="Note" color="primary" %}}
`SparkApplicationIntegration` 目前仍处于 Alpha 测试阶段，默认情况下处于禁用状态。

你可以通过编辑 `SparkApplicationIntegration` 特性门控来启用它。
有关特性门控配置的详细信息，
请参阅[安装](/docs/installation/#change-the-feature-gates-configuration)指南。
{{% /alert %}}

此页面展示了在运行 [Spark Operator](https://github.com/kubeflow/spark-operator)
SparkApplication 时，如何利用 Kueue 的调度和资源管理能力。

本指南适用于对 Kueue 有基本了解的[批处理用户](/docs/tasks#batch-user)。
欲了解更多信息，请参阅 [Kueue 概述](/docs/overview)。

## 在你开始之前

检查[管理集群配额](/docs/tasks/manage/administer_cluster_quotas)，
以获取初始集群设置的详细信息。

查阅 [Spark Operator 安装指南](https://www.kubeflow.org/docs/components/spark-operator/getting-started/#installation)。

你可以[修改已安装版本的 Kueue 配置](/docs/installation#install-a-custom-configured-released-version)，
以将 SparkApplication 纳入支持的工作负载中。

{{% alert title="注意" color="primary" %}}

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
SparkApplication 集成不支持动态分配。如果你在 SparkApplication
中设置了 `spec.dynamicAllocation.enabled=true`，Kueue 将在 Webhook 中拒绝此类资源。
{{% /alert %}}

### b. （可选）在 SparkOperation 中设置 Suspend 字段

```yaml
spec:
  suspend: true
```

默认情况下，Kueue 将通过 Webhook 将 `suspend`
设置为 true，并在 SparkApplication 被允许时取消挂起。

## SparkApplication 示例

{{< include "examples/jobs/sample-sparkapplication.yaml" "yaml" >}}
