---
title: "在多集群环境中运行 MPIJob"
linkTitle: "MPIJob"
weight: 5
date: 2024-10-25
description: 在多集群环境中运行 MPIJob
---

## 开始之前 {#before-you-begin}

请查阅 [MultiKueue 安装指南](/zh-CN/docs/tasks/manage/setup_multikueue)了解如何正确设置 MultiKueue 集群。

为保证正确安装和使用，需至少使用 Kueue v0.9.0 及 MPI Operator v0.6.0。

### 集群上的安装 {#installation-on-the-clusters}

{{% alert title="注意" color="primary" %}}
注意：MPI Operator 和 Trainer 必须同时运行在同一集群上，但 Trainer 部署需做特殊配置。
详见 [与 MPI Operator 协同工作](/zh-CN/docs/tasks/run/multikueue/kubeflow#working-alongside-mpi-operator)。
{{% /alert %}}

有关 MPI Operator 的安装和配置详情，请参见 [MPI Operator 安装文档](https://www.kubeflow.org/docs/components/training/user-guides/mpi/#installation)。

## MultiKueue 集成 {#multikueue-integration}

完成设置后，你可以通过运行 MPIJob [`sample-mpijob.yaml`](/zh-CN/docs/tasks/run/kubeflow/mpijobs/#sample-mpijob)进行测试。

{{% alert title="注意" color="primary" %}}
注意：Kueue 会在管理集群上的 MPIJob 默认设置 `spec.runPolicy.managedBy` 字段为 `kueue.x-k8s.io/multikueue`。

这使得 MPI Operator 能够忽略由 MultiKueue 管理的 Job，特别是跳过 Pod 的创建。

Pod 会在选定的工作集群上的 Job 镜像副本中被创建并实际运行。
Job 镜像副本未设置此字段。
{{% /alert %}}