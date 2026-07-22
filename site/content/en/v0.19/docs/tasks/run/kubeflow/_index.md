---
title: "Kubeflow Jobs (v1)"
linkTitle: "Kubeflow Jobs (v1)"
weight: 7
date: 2023-08-23
description: >
  Run Kueue managed Kubeflow Trainer v1 Jobs
no_list: true
---

The tasks below show you how to run Kueue managed Kubeflow Trainer v1 Jobs.

{{% alert title="Warning" color="warning" %}}
**Deprecation Notice:** The integration with [Kubeflow Trainer v1](https://www.kubeflow.org/docs/components/trainer/legacy-v1/) (PyTorchJob, TFJob, XGBoostJob, PaddleJob, JAXJob) is **deprecated** in Kueue and will be removed in a future release, tentatively **v0.20**.

Kubeflow Trainer v1 is now legacy. We strongly recommend migrating to [Kubeflow Trainer v2](https://github.com/kubeflow/trainer) (which is supported in Kueue via [TrainJob](/v0.19/docs/tasks/run/trainjobs/)), or using an alternative framework such as [JobSet](/v0.19/docs/tasks/run/jobsets/) to run your jobs. See the [Kubeflow Trainer v1 to v2 migration guide](https://trainer.kubeflow.org/en/latest/operator-guides/migration.html) for details on how to migrate.
{{% /alert %}}

### [Trainer v1](https://github.com/kubeflow/trainer) Integration
- [Run a Kueue managed Kubeflow PyTorchJob](/v0.19/docs/tasks/run/kubeflow/pytorchjobs/).
- [Run a Kueue managed Kubeflow TFJob](/v0.19/docs/tasks/run/kubeflow/tfjobs/).
- [Run a Kueue managed Kubeflow XGBoostJob](/v0.19/docs/tasks/run/kubeflow/xgboostjobs/).
- [Run a Kueue managed Kubeflow PaddleJob](/v0.19/docs/tasks/run/kubeflow/paddlejobs/).
- [Run a Kueue managed Kubeflow JAXJob](/v0.19/docs/tasks/run/kubeflow/jaxjobs/).

### [MPI Operator](https://github.com/kubeflow/mpi-operator) Integration
- [Run a Kueue managed Kubeflow MPIJob](/v0.19/docs/tasks/run/kubeflow/mpijobs/).

### [Spark Operator](https://github.com/kubeflow/spark-operator) Integration
- [Run a Kueue managed Kubeflow SparkApplication](/v0.19/docs/tasks/run/kubeflow/sparkapplications/)
