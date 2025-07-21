---
title: "在多集群环境中运行 Jobsets"
linkTitle: "Jobsets"
weight: 3
date: 2024-09-25
description: 在多集群环境中运行 Jobsets
---

## 在开始之前

请查阅 [MultiKueue 安装指南](/docs/tasks/manage/setup_multikueue) 了解如何正确设置 MultiKueue 集群。

为方便安装和使用，建议至少使用 Kueue v0.8.1 和 JobSet v0.6.0（更多详情见 [JobSet 安装](https://jobset.sigs.k8s.io/docs/installation/)）。

## 运行 Jobsets 示例

完成设置后，你可以通过运行 [`jobset-sample.yaml`](/docs/tasks/run/jobsets/#example-jobset) Job 进行测试。

在该设置中，如果未指定，`spec.managedBy` 字段会自动默认设置为 `kueue.x-k8s.io/multikueue`，只要指定了 `kueue.x-k8s.io/queue-name` 注解，并且对应的 Cluster Queue 使用了 Multi Kueue admission check。