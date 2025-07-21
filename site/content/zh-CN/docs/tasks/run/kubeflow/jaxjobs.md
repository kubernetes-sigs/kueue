---
title: "运行 JAXJob"
date: 2025-04-23
weight: 6
description: >
  运行由 Kueue 调度的 JAXJob
---

本页面展示了在运行 [Trainer](https://www.kubeflow.org/docs/components/training/jax/) JAXJobs 时，如何利用 Kueue 的调度和资源管理能力。

本指南适用于对 Kueue 有基本了解的[批量用户](/docs/tasks#batch-user)。更多信息请参见 [Kueue 概述](/docs/overview)。

## 开始之前

请查阅 [管理集群配额](/docs/tasks/manage/administer_cluster_quotas) 以了解初始集群设置的详细信息。

请查阅 [Trainer 安装指南](https://github.com/kubeflow/training-operator#installation)。

注意，Trainer 的最低要求版本为 v1.9.0。

你可以[从已安装版本修改 kueue 配置](/docs/installation#install-a-custom-configured-released-version)以将 JAXJobs 包含为允许的工作负载。

{{% alert title="注意" color="primary" %}}
在 v0.8.1 之前版本中使用 Trainer 时，安装后需要重启 Kueue。
你可以通过运行：`kubectl delete pods -l control-plane=controller-manager -n kueue-system` 来实现。
{{% /alert %}}

## JAXJob 定义

### a. 队列选择

目标 [本地队列](/docs/concepts/local_queue) 应在 JAXJob 配置的 `metadata.labels` 部分指定。

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. 可选地在 JAXJobs 中设置 Suspend 字段

```yaml
spec:
  runPolicy:
    suspend: true
```

默认情况下，Kueue 会通过 webhook 将 `suspend` 设置为 true，并在 JAXJob 被接纳时自动取消挂起。

## JAXJob 示例

本示例基于 https://github.com/kubeflow/trainer/blob/da11d1116c29322c481d0b8f174df8d6f05004aa/examples/jax/cpu-demo/demo.yaml。

{{< include "examples/jobs/sample-jaxjob.yaml" "yaml" >}}
