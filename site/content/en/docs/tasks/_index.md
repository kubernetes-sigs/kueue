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

- As a batch administrator, you can learn how to [setup role-based access control](/docs/tasks/rbac)
  to Kueue objects.
- As a batch administrator, you can learn how to
  [administer cluster quotas](/docs/tasks/administer_cluster_quotas) with ClusterQueues and LocalQueues.
- As a batch administrator, you can learn how to setup
  [Sequential Admission with Ready Pods](/docs/tasks/setup_sequential_admission).
- As a batch administrator, you can learn how to enable
  [pprof endpoints](/docs/tasks/enabling_pprof_endpoints).

### Batch user

A _batch user_ runs [workloads](/docs/concepts/workload). A typical
batch user is a researcher, AI/ML engineer, data scientist, among others.

- As a batch user, you can learn how to [run a Kueue managed batch/Job](/docs/tasks/run_jobs).
- As a batch user, you can learn how to [run a Kueue managed Flux MiniCluster](/docs/tasks/run_flux_minicluster).
- As a batch user, you can learn how to [run a Kueue managed Kubeflow MPIJob](/docs/tasks/run_mpi_jobs).
- As a batch user, you can learn how to [run a Kueue managed KubeRay RayJob](/docs/tasks/run_rayjobs).
- As a batch developer user, you can learn how to [submit Kueue jobs from Python](/docs/tasks/run_python_jobs).
