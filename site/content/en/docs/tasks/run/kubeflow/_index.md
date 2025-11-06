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

{{% alert title="Note" color="primary" %}}
**Kubeflow Trainer v2 is now available:** Consider using [TrainJob](/docs/tasks/run/trainjobs/) which provides a unified API for all training frameworks. Trainer v1 APIs (PyTorchJob, TFJob, etc.) are stable and production-ready.
{{% /alert %}}

### [Trainer v1](https://github.com/kubeflow/trainer) Integration
- [Run a Kueue managed Kubeflow PyTorchJob](/docs/tasks/run/kubeflow/pytorchjobs/).
- [Run a Kueue managed Kubeflow TFJob](/docs/tasks/run/kubeflow/tfjobs/).
- [Run a Kueue managed Kubeflow XGBoostJob](/docs/tasks/run/kubeflow/xgboostjobs/).
- [Run a Kueue managed Kubeflow PaddleJob](/docs/tasks/run/kubeflow/paddlejobs/).
- [Run a Kueue managed Kubeflow JAXJob](/docs/tasks/run/kubeflow/jaxjobs/).

### [MPI Operator](https://github.com/kubeflow/mpi-operator) Integration
- [Run a Kueue managed Kubeflow MPIJob](/docs/tasks/run/kubeflow/mpijobs/).
