---
title: "运行 PaddleJob"
date: 2023-09-22
weight: 6
description: >
  使用 Kueue 调度 PaddleJob
---

此页面展示了在运行 [Trainer](https://www.kubeflow.org/docs/components/training/paddlepaddle/)
PaddleJob 时，如何利用 Kueue 的调度和资源管理能力。

本指南适用于对 Kueue 有基本了解的[批处理用户](/zh-CN/docs/tasks#batch-user)。
欲了解更多信息，请参阅 [Kueue 概述](/zh-CN/docs/overview)。

## 开始之前  {#before-you-begin}

查阅[管理集群配额](/zh-CN/docs/tasks/manage/administer_cluster_quotas)，
以获取初始集群设置的详细信息。

查阅 [Trainer 安装指南](https://www.kubeflow.org/docs/components/training/installation/)。

请注意，Trainer 最低版本要求为 v1.7.0。

你可以[修改已安装版本的 Kueue 配置](/zh-CN/docs/installation#install-a-custom-configured-released-version)，
以添加 PaddleJob 到允许的工作负载中。

{{% alert title="注意" color="primary" %}}
要使用 Trainer，在 v0.8.1 之前，你需要在安装后重启 Kueue。
你可以通过运行以下命令来实现：`kubectl delete pods -l control-plane=controller-manager -n kueue-system`。
{{% /alert %}}

## PaddleJob 定义  {#paddlejob-definition}

### a. 队列选择

目标[本地队列](/zh-CN/docs/concepts/local_queue)应在 PaddleJob
配置的 `metadata.labels` 部分中指定。

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. 可选择在 PaddleJob 中设置 Suspend 字段

```yaml
spec:
  runPolicy:
    suspend: true
```

默认情况下，Kueue 将通过 Webhook 将 `suspend` 设置为 true，并在 PaddleJob 被接受时取消挂起。

## 示例 PaddleJob

此示例基于 https://github.com/kubeflow/trainer/blob/288d680a699237fb61a74ada005e202721815ff2/examples/paddlepaddle/simple-cpu.yaml。

{{< include "examples/jobs/sample-paddlejob.yaml" "yaml" >}}
