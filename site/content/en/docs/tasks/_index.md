---

title: "Tasks"
linkTitle: "Tasks"
weight: 6
date: 2022-02-14
description: >
  Doing common Kueue tasks
no_list: true
---

The following tasks show you how to perform operations based on the Kueue user
personas such as _batch administrators_ and _batch users_.

### Batch administrator

A _batch administrator_ manages the cluster infrastructure and establishes
quotas and queues.

As a batch administrator, you can learn how to:

- [Setup role-based access control](/docs/tasks/rbac)
  to Kueue objects.
- [Administer cluster quotas](/docs/tasks/administer_cluster_quotas) with ClusterQueues and LocalQueues.
- Setup [Sequential Admission with Ready Pods](/docs/tasks/setup_sequential_admission).
- As a batch administrator, you can learn how to
  [monitor pending workloads](/docs/tasks/monitor_pending_workloads).

### Batch user

A _batch user_ runs [workloads](/docs/concepts/workload). A typical
batch user is a researcher, AI/ML engineer, data scientist, among others.

As a batch user, you can learn how to:
- [Run a Kueue managed batch/Job](/docs/tasks/run_jobs).
- [Run a Kueue managed Flux MiniCluster](/docs/tasks/run_flux_minicluster).
- [Run a Kueue managed Kubeflow Job](/docs/tasks/run_kubeflow_jobs).
  Kueue supports MPIJob v2beta1, PyTorchJob, TFJob, XGBoostJob, and PaddleJob.
- [Run a Kueue managed KubeRay RayJob](/docs/tasks/run_rayjobs).
- [Submit Kueue jobs from Python](/docs/tasks/run_python_jobs).

### Platform developer

A _platform developer_ integrates Kueue with other software and/or contributes to Kueue.

As a platform developer, you can learn how to:
- [Integrate a custom Job with Kueue](/docs/tasks/integrate_a_custom_job).
- [Submit Kueue jobs from Python](/docs/tasks/run_python_jobs).
- [Enable pprof endpoints](/docs/tasks/enabling_pprof_endpoints).
