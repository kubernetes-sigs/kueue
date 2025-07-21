---
title: "运行 MPIJob"
date: 2023-05-16
weight: 6
description: 运行由 Kueue 调度的 MPIJob
---

本页面展示了在运行 [MPI Operator](https://www.kubeflow.org/docs/components/training/mpi/) MPIJobs 时，如何利用 Kueue 的调度和资源管理能力。

本指南适用于对 Kueue 有基本了解的[批量用户](/docs/tasks#batch-user)。更多信息请参见 [Kueue 概述](/docs/overview)。

## 开始之前

请查阅 [管理集群配额](/docs/tasks/manage/administer_cluster_quotas) 以了解初始集群设置的详细信息。

请查阅 [MPI Operator 安装指南](https://github.com/kubeflow/mpi-operator#installation)。

你可以[从已安装版本修改 kueue 配置](/docs/installation#install-a-custom-configured-released-version)以将 MPIJobs 包含为允许的工作负载。

{{% alert title="注意" color="primary" %}}
在 v0.8.1 之前版本中使用 MPIJob 时，安装后需要重启 Kueue。
你可以通过运行：`kubectl delete pods -l control-plane=controller-manager -n kueue-system` 来实现。
{{% /alert %}}

{{% alert title="注意" color="primary" %}}
当同时使用 MPI Operator 和 Trainer 时，必须禁用 Trainer 的 MPIJob 选项。
Trainer 部署需要修改以启用除 MPIJob 外的所有 kubeflow 作业，具体请参见[此处](https://github.com/kubeflow/trainer/issues/1777)。
{{% /alert %}}

## MPI Operator 定义

### a. 队列选择

目标 [本地队列](/docs/concepts/local_queue) 应在 MPIJob 配置的 `metadata.labels` 部分指定。

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. 可选地在 MPIJobs 中设置 Suspend 字段

```yaml
spec:
  runPolicy:
    suspend: true
```

默认情况下，Kueue 会通过 webhook 将 `suspend` 设置为 true，并在 MPIJob 被接纳时自动取消挂起。

## MPIJob 示例

本示例基于 https://github.com/kubeflow/mpi-operator/blob/ccf2756f749336d652fa6b10a732e241a40c7aa6/examples/v2beta1/pi/pi.yaml。

{{< include "examples/jobs/sample-mpijob.yaml" "yaml" >}}

如需在 Python 中实现等效操作，请参见[运行 Python 作业](/docs/tasks/run/python_jobs/#mpi-operator-job)。
