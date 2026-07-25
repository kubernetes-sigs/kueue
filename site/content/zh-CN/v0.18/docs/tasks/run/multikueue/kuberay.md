---
title: "在多集群环境中运行 KubeRay Job"
linkTitle: "KubeRay"
weight: 4
date: 2025-03-19
description: 在多集群环境中运行 KubeRay Job
---

## 开始之前 {#before-you-begin}

请查阅 [MultiKueue 安装指南](/zh-CN/docs/tasks/manage/setup_multikueue)了解如何正确设置 MultiKueue 集群。

为方便安装和使用，建议至少使用 Kueue v0.11.0 及 KubeRay Operator v1.3.1 及以上版本。

有关 KubeRay Operator 的安装和配置详情，
请参见 [KubeRay Operator 安装文档](https://docs.ray.io/en/latest/cluster/kubernetes/getting-started/raycluster-quick-start.html#step-2-deploy-a-kuberay-operator)。

{{% alert title="注意" color="primary" %}}
在 Kueue（低于 v0.11.0）支持 [ManagedBy 特性](https://github.com/ray-project/kuberay/issues/2544)之前，
<b>管理集群</b>上的 KubeRay Operator 安装必须仅限于 CRD。

要安装 CRD，请运行：
```bash
kubectl create -k "github.com/ray-project/kuberay/ray-operator/config/crd?ref=v1.3.0"
```
{{% /alert %}}

## MultiKueue 集成 {#multikueue-integration}

完成设置后，你可以通过运行 RayJob [`ray-job-sample.yaml`](/zh-CN/docs/tasks/run/rayjobs/#example-rayjob)进行测试。

{{% alert title="注意" color="primary" %}}
注意：Kueue 会在管理集群上的 KubeRay Job（RayJob、RayCluster、RayService）默认设置 `spec.managedBy` 字段为 `kueue.x-k8s.io/multikueue`。

这使得 KubeRay Operator 能够忽略由 MultiKueue 管理的 Job，特别是跳过 Pod 的创建。

Pod 会在选定的工作集群上的 Job 镜像副本中被创建并实际运行。
Job 镜像副本未设置此字段。
{{% /alert %}}
