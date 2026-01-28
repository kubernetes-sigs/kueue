---
title: "在多集群环境中运行 AppWrappers"
linkTitle: "AppWrappers"
weight: 3
date: 2025-06-23
description: 在多集群环境中运行 AppWrappers
---

## 开始之前 {#before-you-begin}

请查阅 [MultiKueue 安装指南](/zh-CN/docs/tasks/manage/setup_multikueue)了解如何正确设置 MultiKueue 集群。

为方便安装和使用，建议至少使用 Kueue v0.11.0 及 AppWrapper Operator v1.1.1 及以上版本。

有关 AppWrapper Operator 的安装和配置详情，请参见 [AppWrapper 快速入门指南](https://project-codeflare.github.io/appwrapper/quick-start/)。

{{% alert title="注意" color="primary" %}}
MultiKueue 对 AppWrappers 的支持在 Kueue v0.11.0 之前的版本中不可用。
{{% /alert %}}

## MultiKueue 集成 {#multikueue-integration}

完成设置后，你可以通过运行 AppWrapper [`appwrapper-pytorch-sample.yaml`](/zh-CN/docs/tasks/run/appwrappers/#example-appwrapper-containing-a-pytorchjob)进行测试。

{{% alert title="注意" color="primary" %}}
注意：Kueue 会在管理集群上的 AppWrappers 默认设置 `spec.managedBy` 字段为 `kueue.x-k8s.io/multikueue`。

这使得 AppWrapper Operator 能够忽略由 MultiKueue 管理的 AppWrapper 任务，特别是跳过对被包装资源的创建。

这些资源会在选定的工作集群上的 AppWrapper 镜像副本中被创建并实际运行。
AppWrapper 镜像副本未设置此字段。
{{% /alert %}}
