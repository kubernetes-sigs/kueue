---
title: "运行 MPIJob"
date: 2023-05-16
weight: 6
description: >
  使用 Kueue 调度 MPIJob
---

本页面展示了在运行 [MPI Operator](https://www.kubeflow.org/docs/components/training/mpi/)
MPIJob 时，如何利用 Kueue 的调度和资源管理能力。

本指南适用于对 Kueue 有基本了解的[批处理用户](/zh-CN/docs/tasks#batch-user)。
更多信息，请参阅 [Kueue 概述](/zh-CN/docs/overview)。

## 开始之前  {#before-you-begin}

查阅[管理集群配额](/zh-CN/docs/tasks/manage/administer_cluster_quotas)，
以获取初始集群设置的详细信息。

查阅 [MPI Operator 安装指南](https://github.com/kubeflow/mpi-operator#installation)。

你可以[修改已安装版本的 Kueue 配置](/zh-CN/docs/installation#install-a-custom-configured-released-version)，
以将 MPIJob 添加到允许的工作负载中。

{{% alert title="Note" color="primary" %}}
要在 v0.8.1 版本之前使用 Trainer，你需要在安装后重启 Kueue。
你可以通过运行以下命令来实现：`kubectl delete pods -l control-plane=controller-manager -n kueue-system`。
{{% /alert %}}

{{% alert title="Note" color="primary" %}}
在同时使用 MPI Operator 和 Trainer 时，需要禁用 Trainer 的 MPIJob 选项。
需要修改 Trainer 部署，以启用除 MPIJob 之外的所有 Kubeflow 作业，
如[此处](https://github.com/kubeflow/trainer/issues/1777)所述。
{{% /alert %}}

## MPI Operator 定义   {#mpi-operator-definition}

### a. 队列选择

目标[本地队列](/zh-CN/docs/concepts/local_queue)应在 MPIJob 配置的
`metadata.labels` 部分中指定。

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. 可选择在 MPIJobs 中设置 Suspend 字段

```yaml
spec:
  runPolicy:
    suspend: true
```

默认情况下，Kueue 将通过 Webhook 将 `suspend` 设置为 true，并在
MPIJob 被接受时取消挂起。

## MPIJob 示例

此示例基于 https://github.com/kubeflow/mpi-operator/blob/ccf2756f749336d652fa6b10a732e241a40c7aa6/examples/v2beta1/pi/pi.yaml。

{{< include "examples/jobs/sample-mpijob.yaml" "yaml" >}}

有关在 Python 中执行此操作的等效说明，请参阅[运行 Python 作业](/zh-CN/docs/tasks/run/python_jobs/#mpi-operator-job)。
