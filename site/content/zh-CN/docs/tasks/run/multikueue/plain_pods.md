---
title: "在多集群环境中运行普通 Pod"
linkTitle: "Plain Pod"
weight: 2
date: 2025-02-17
description: 在多集群环境中运行普通 Pod

---

## 开始之前 {#before-you-begin}

1. 请查阅 [MultiKueue 安装指南](/zh-CN/docs/tasks/manage/setup_multikueue)了解如何正确设置 MultiKueue 集群。

2. 按照[运行 Plain Pod](/zh-CN/docs/tasks/run/plain_pods/#before-you-begin)的步骤，
   了解如何启用和配置 `pod` 集成。

在管理集群上创建的 Pod 会自动被管控，并实时接收来自远程副本的状态更新。

{{< feature-state state="beta" for_version="v0.11.0" >}}

## 示例 {#examples}

完成设置后，你可以通过运行以下示例进行测试：

1. 单个普通 Pod
{{< include "examples/pods-kueue/kueue-pod.yaml" "yaml" >}}

2. Pod 组
{{< include "examples/pods-kueue/kueue-pod-group.yaml" "yaml" >}}