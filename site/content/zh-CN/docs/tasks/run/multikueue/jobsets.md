---
title: "在多集群环境中运行 Jobset"
linkTitle: "Jobsets"
weight: 3
date: 2024-09-25
description: 运行 MultiKueue 调度的 Jobset。
---

## 开始之前 {#before-you-begin}

请查阅 [MultiKueue 安装指南](/zh-CN/docs/tasks/manage/setup_multikueue)了解如何正确设置 MultiKueue 集群。

为方便安装和使用，建议至少使用 Kueue v0.8.1 和 JobSet v0.6.0（更多详情见 [JobSet 安装文档](https://jobset.sigs.k8s.io/docs/installation/)）。

## 运行 Jobset 示例 {#run-jobsets-example}

完成设置后，你可以通过运行 [`jobset-sample.yaml`](/zh-CN/docs/tasks/run/jobsets/#example-jobset) Job 进行测试。

在该设置中，如果未显式指定 spec.managedBy 字段的取值，则取值自动默认为 `kueue.x-k8s.io/multikueue`。
前提是已设置了 `kueue.x-k8s.io/queue-name` 注解，并且对应的 ClusterQueue 使用了 MultiKueue 的准入检查。