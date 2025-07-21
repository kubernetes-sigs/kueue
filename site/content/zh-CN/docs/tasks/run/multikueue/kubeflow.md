---
title: "在多集群环境中运行 Kubeflow Jobs"
linkTitle: "Kubeflow"
weight: 4
date: 2024-09-25
description: 在多集群环境中运行 Kubeflow Jobs
---

## 在开始之前

请查阅 [MultiKueue 安装指南](/docs/tasks/manage/setup_multikueue) 了解如何正确设置 MultiKueue 集群。

为方便安装和使用，建议至少使用 Kueue v0.11.0，Kubeflow Trainer 至少 v1.9.0。

有关 Trainer 的安装和配置详情，请参见 [Trainer 安装](https://www.kubeflow.org/docs/components/training/installation/#installing-the-training-operator)。

{{% alert title="Note" color="primary" %}}
在 Kueue（低于 v0.11.0）支持 [ManagedBy 特性](https://github.com/kubeflow/trainer/issues/2193) 之前，<b>管理集群</b> 上的 Kubeflow Trainer 安装必须仅限于 CRD。

要安装 CRD，请运行：
```bash
kubectl apply -k "github.com/kubeflow/trainer.git/manifests/base/crds?ref=v1.9.0"
```
{{% /alert %}}

## MultiKueue 集成

完成设置后，你可以通过运行任意一个 Kubeflow Job（如 PyTorchJob [`sample-pytorchjob.yaml`](/docs/tasks/run/kubeflow/pytorchjobs/#sample-pytorchjob)）进行测试。

{{% alert title="Note" color="primary" %}}
注意：Kueue 会在管理集群上的所有 Kubeflow Job 默认设置 `spec.runPolicy.managedBy` 字段为 `kueue.x-k8s.io/multikueue`。

这使得 Trainer 能够忽略由 MultiKueue 管理的 Job，特别是跳过 Pod 的创建。

Pod 会在选定的工作集群上的 Job 镜像副本中被创建并实际运行。
Job 镜像副本不会设置该字段。
{{% /alert %}}

## 与 MPI Operator 协同工作
为了让 MPI-operator 和 Trainer 能在同一集群上工作，需要：
1. 从 `base/crds/kustomization.yaml` 中移除 `kubeflow.org_mpijobs.yaml` 条目 - https://github.com/kubeflow/trainer/issues/1930
2. 修改 Trainer 部署以启用除 MPI 以外的所有 kubeflow jobs -  https://github.com/kubeflow/trainer/issues/1777
  