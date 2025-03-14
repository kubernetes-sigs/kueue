---

title: "Run with Kubeflow"
linkTitle: "Kubeflow Jobs"
weight: 6
date: 2023-08-23
description: >
  How to run Kueue with Kubeflow
no_list: true
---

The tasks below show you how to run Kueue managed Kubeflow Jobs.

### [MPI Operator](https://github.com/kubeflow/mpi-operator) Integration
- [Run a Kueue managed Kubeflow MPIJob](/docs/tasks/run_kubeflow_jobs/run_mpijobs).

### [Trainer](https://github.com/kubeflow/trainer) Integration

{{% alert title="Note" color="primary" %}}
Kueue supports only traditional Jobs served until Trainer v1.9.x and does not support new TrainJob. 
{{% /alert %}}

- [Run a Kueue managed Kubeflow PyTorchJob](/docs/tasks/run_kubeflow_jobs/run_pytorchjobs).
- [Run a Kueue managed Kubeflow TFJob](/docs/tasks/run_kubeflow_jobs/run_tfjobs).
- [Run a Kueue managed Kubeflow XGBoostJob](/docs/tasks/run_kubeflow_jobs/run_xgboostjobs).
- [Run a Kueue managed kubeflow PaddleJob](/docs/tasks/run_kubeflow_jobs/run_paddlejobs).
- [Run a Kueue managed kubeflow JAXJob](/docs/tasks/run_kubeflow_jobs/run_jaxjobs).
