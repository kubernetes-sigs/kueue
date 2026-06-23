---
title: "在多集群中运行 TrainJob"
linkTitle: "TrainJob"
weight: 3
date: 2025-11-06
description: >
  从 Kubeflow Trainer v2 运行一个由 MultiKueue 调度的 TrainJob。
---

## 开始之前

查阅 [MultiKueue 安装指南](/docs/tasks/manage/setup_multikueue)以了解如何正确设置 MultiKueue 集群。

为了简化设置和使用，我们建议至少使用 Kueue v0.11.0 版本以及
Kubeflow Trainer 至少 v2.0.0 版本。

查看 [Trainer 安装](https://www.kubeflow.org/zh/docs/components/trainer/operator-guides/installation/)获取
Kubeflow Trainer v2 的安装和配置详情。

{{% alert title="注意" color="primary" %}}
TrainJob 是 Kubeflow Trainer v2 的一部分，它引入了一个统一的 API 用于所有训练框架。
它取代了 Trainer v1 中特定于框架的工作类型（PyTorchJob、TFJob 等）。

对于遗留的 Kubeflow Jobs（v1），请参阅[在多集群中运行 Kubeflow Job](/docs/tasks/run/multikueue/kubeflow/)。
{{% /alert %}}

## MultiKueue 集成

完成设置后，你可以通过运行一个 TrainJob
[`sample-trainjob`](/docs/tasks/run/trainjobs/#using-clustertrainingruntime) 来测试它。

{{% alert title="注意" color="primary" %}}
Kueue 默认将 `spec.managedBy` 字段设置为 `kueue.x-k8s.io/multikueue` 在管理集群上的 TrainJob 中。

这允许 Trainer 忽略由 MultiKueue 管理的位于管理集群上的 Job，并特别跳过 Pod 创建。

Pod 将在选定的工作集群上 TrainJob 的镜像副本中创建，实际计算也将在此发生。
TrainJob 的镜像副本不设置该字段。
{{% /alert %}}

## ClusterTrainingRuntime 和 TrainingRuntime

在 MultiKueue 环境中的 TrainJob 可与 ClusterTrainingRuntime 和 TrainingRuntime 协同工作：

- **ClusterTrainingRuntime**：必须安装在所有工作集群上。运行时定义应在各集群间保持一致。
- **TrainingRuntime**：必须安装在工作集群上对应命名空间中，TrainJob 将在其下运行。

确保你的 TrainJob 引用的运行时配置在目标工作集群上可用，然后才分发作业。

## 示例

这是一个完整的示例，展示如何使用 MultiKueue 运行 TrainJob：

1. **创建一个 ClusterTrainingRuntime** 在所有集群（管理和工作）上：

```yaml
apiVersion: trainer.kubeflow.org/v1alpha1
kind: ClusterTrainingRuntime
metadata:
  name: torch-distributed
  labels:
    trainer.kubeflow.org/framework: torch
spec:
  mlPolicy:
    numNodes: 1
    torch:
      numProcPerNode: auto
  template:
    spec:
      replicatedJobs:
        - name: node
          template:
            metadata:
              labels:
                trainer.kubeflow.org/trainjob-ancestor-step: trainer
            spec:
              template:
                spec:
                  containers:
                    - name: node
                      image: pytorch/pytorch:2.7.1-cuda12.8-cudnn9-runtime
```

2. 在管理集群上**创建启用 MultiKueue 的 TrainJob**：

```yaml
apiVersion: trainer.kubeflow.org/v1alpha1
kind: TrainJob
metadata:
  name: pytorch-multikueue
  namespace: default
  labels:
    kueue.x-k8s.io/queue-name: multikueue-queue
spec:
  runtimeRef:
    name: torch-distributed
    kind: ClusterTrainingRuntime
  trainer:
    numNodes: 2
    resourcesPerNode:
      requests:
        cpu: "4"
        memory: "8Gi"
        nvidia.com/gpu: "1"
```

TrainJob 将根据 MultiKueue 配置和可用资源被派发到指定的 worker 集群。
训练 Pod 将在选定的 worker 集群上创建并执行。
