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
- [运行 Kueue 管理的 Kubeflow MPIJob](/zh-CN/docs/tasks/run_kubeflow_jobs/run_mpijobs)。

### [Trainer](https://github.com/kubeflow/trainer) 集成

{{% alert title="注意" color="primary" %}}
Kueue 仅支持直到 Trainer v1.9.x 的传统作业，不支持新的 TrainJob。
{{% /alert %}}

- [运行 Kueue 管理的 Kubeflow PyTorchJob](/zh-CN/docs/tasks/run_kubeflow_jobs/run_pytorchjobs)。
- [运行 Kueue 管理的 Kubeflow TFJob](/zh-CN/docs/tasks/run_kubeflow_jobs/run_tfjobs)。
- [运行 Kueue 管理的 Kubeflow XGBoostJob](/zh-CN/docs/tasks/run_kubeflow_jobs/run_xgboostjobs)。
- [运行 Kueue 管理的 Kubeflow PaddleJob](/zh-CN/docs/tasks/run_kubeflow_jobs/run_paddlejobs)。
- [运行 Kueue 管理的 Kubeflow JAXJob](/zh-CN/docs/tasks/run_kubeflow_jobs/run_jaxjobs)。
