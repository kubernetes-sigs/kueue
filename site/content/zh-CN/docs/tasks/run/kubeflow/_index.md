---
title: "运行 Kubeflow"
linkTitle: "Kubeflow Jobs"
weight: 6
date: 2023-08-23
description: >
  如何运行 Kueue 管理的 Kubeflow 作业
no_list: true
---

下面的任务向你展示如何运行 Kueue 管理的 Kubeflow 作业。

### 集成 [MPI Operator](https://github.com/kubeflow/mpi-operator)
- [运行 Kueue 管理的 Kubeflow MPIJob](/zh-cn/docs/tasks/run/kubeflow/mpijobs)。

### [Trainer](https://github.com/kubeflow/trainer) 集成

{{% alert title="警告" color="warning" %}}
**弃用通知：** Kueue 中与 [Kubeflow Trainer v1](https://www.kubeflow.org/docs/components/trainer/legacy-v1/)（PyTorchJob、TFJob、XGBoostJob、PaddleJob、JAXJob）的集成已**弃用**，并将于未来的版本（暂定 **v0.20**）中移除。

Kubeflow Trainer v1 现在已是传统遗留项目（legacy）。我们强烈建议迁移到 [Kubeflow Trainer v2](https://github.com/kubeflow/trainer)（在 Kueue 中已通过 [TrainJob](/docs/tasks/run/trainjobs/)（英文文档）提供支持），或者使用其他替代框架（例如 [JobSet](/zh-cn/docs/tasks/run/jobsets/)）来运行您的作业。
{{% /alert %}}

- [运行 Kueue 管理的 Kubeflow PyTorchJob](/zh-cn/docs/tasks/run/kubeflow/pytorchjobs)。
- [运行 Kueue 管理的 Kubeflow TFJob](/zh-cn/docs/tasks/run/kubeflow/tfjobs)。
- [运行 Kueue 管理的 Kubeflow XGBoostJob](/zh-cn/docs/tasks/run/kubeflow/xgboostjobs)。
- [运行 Kueue 管理的 Kubeflow PaddleJob](/zh-cn/docs/tasks/run/kubeflow/paddlejobs)。
- [运行 Kueue 管理的 Kubeflow JAXJob](/zh-cn/docs/tasks/run/kubeflow/jaxjobs)。
