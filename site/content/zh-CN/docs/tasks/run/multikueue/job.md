---
title: "在多集群环境中运行 Job"
linkTitle: "Kubernetes Job"
weight: 2
date: 2024-11-05
description: 运行 MultiKueue 调度的 Kubernetes Job。
---

## 开始之前 {#before-you-begin}

请查阅 [MultiKueue 安装指南](/zh-CN/docs/tasks/manage/setup_multikueue)了解如何正确设置 MultiKueue 集群。

MultiKueue 在工作集群上执行的 Job 的当前状态会实时同步到管理集群。

这样，用户和自动化工具可以在不访问工作集群的情况下，直接跟踪 Job 的状态（`.status`），
从而实现 MultiKueue 的透明化。

## 示例 {#examples}

完成设置后，你可以通过运行以下示例进行测试：

{{< include "examples/jobs/sample-job.yaml" "yaml" >}}
