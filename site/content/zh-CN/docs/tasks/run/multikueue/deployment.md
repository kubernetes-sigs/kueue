---
title: "在多集群环境中运行 Deployment"
linkTitle: "Deployment"
weight: 2
date: 2025-02-17
description: 运行 MultiKueue 调度的 Deployment。
---

## 开始之前 {#before-you-begin}

1. 请查阅 [MultiKueue 安装指南](/zh-CN/docs/tasks/manage/setup_multikueue)了解如何正确设置 MultiKueue 集群。

2. 按照[运行普通 Pod](/zh-CN/docs/tasks/run/plain_pods/#before-you-begin)的步骤，了解如何启用和配置 Pod 集成，这对于启用 Deployment 集成是必需的。

Deployment 会实时接收在工作集群创建的远程 Pod 的状态，并更新。

{{< feature-state state="beta" for_version="v0.11.0" >}}

{{% alert title="注意" color="primary" %}}
在当前实现中，当在有多个工作集群的环境中创建 Deployment 时，Pod 会被分配到任意工作集群。
{{% /alert %}}

## 示例 {#examples}

完成设置后，你可以通过运行以下示例进行测试：

{{< include "examples/serving-workloads/sample-deployment.yaml" "yaml" >}}
