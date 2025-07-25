---
title: "运行 TFJob"
date: 2023-08-23
weight: 6
description: >
  使用 Kueue 调度 TFJob
---

此页面展示了在运行 [Trainer](https://www.kubeflow.org/docs/components/training/tftraining/)
TFJob 时，如何利用 Kueue 的调度和资源管理能力。

本指南适用于对 Kueue 有基本了解的[批处理用户](/zh-CN/docs/tasks#batch-user)。
欲了解更多信息，请参阅 [Kueue 概述](/zh-CN/docs/overview)。

## 开始之前  {#before-you-begin}

请查看[管理集群配额](/zh-CN/docs/tasks/manage/administer_cluster_quotas)，
以获取初始集群设置的详细信息。

请查阅 [Trainer 安装指南](https://www.kubeflow.org/docs/components/training/installation/)。

请注意，Trainer 的最低要求版本是 v1.7.0。

你可以[修改已安装版本的 Kueue 配置](/zh-CN/docs/installation#install-a-custom-configured-released-version)，
以将 TFJob 添加到允许的工作负载中。

{{% alert title="注意" color="primary" %}}
为了使用 Trainer，在 v0.8.1 之前，你需要在安装后重启 Kueue。
你可以通过运行以下命令来实现：`kubectl delete pods -l control-plane=controller-manager -n kueue-system`。
{{% /alert %}}

## TFJob 定义  {#tfjob-definition}

### a. 队列选择

目标 [本地队列](/zh-CN/docs/concepts/local_queue) 应当在 TFJob
配置的 `metadata.labels` 部分指定。

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. 可选择在 TFJob 中设置 Suspend 字段

```yaml
spec:
  runPolicy:
    suspend: true
```

默认情况下，Kueue 将通过 Webhook 将 `suspend` 设置为 true，并在 TFJob 被接受时取消挂起。

## TFJob 示例

此示例基于 https://github.com/kubeflow/trainer/blob/48dbbf0a8e90e52c55ec05d0f689fcbf83c6b441/examples/tensorflow/dist-mnist/tf_job_mnist.yaml。

{{< include "examples/jobs/sample-tfjob.yaml" "yaml" >}}
