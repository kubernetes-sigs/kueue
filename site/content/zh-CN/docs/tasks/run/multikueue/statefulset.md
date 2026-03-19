---
title: "在多集群中运行 StatefulSet"
linkTitle: "StatefulSet"
weight: 2
date: 2025-01-15
description: >
  运行 MultiKueue 调度的 StatefulSet。
---

## 开始之前

检查 [MultiKueue 安装指南](/docs/tasks/manage/setup_multikueue)以了解如何正确设置 MultiKueue 集群。

{{% alert title="Pod 集成要求" color="primary" %}}
自 Kueue v0.15 起，你不需要显式启用 `"pod"` 集成即可使用 `"statefulset"` 集成。

对于 Kueue v0.14 及更早版本，必须显式启用 `"pod"` 集成。

有关配置详情，请参阅[运行普通 Pod](/docs/tasks/run/plain_pods/#before-you-begin)。
{{% /alert %}}

## 运行 StatefulSet 示例

一旦设置完成，你可以通过运行下面的示例来测试它：

{{< include "examples/serving-workloads/sample-statefulset.yaml" "yaml" >}}
