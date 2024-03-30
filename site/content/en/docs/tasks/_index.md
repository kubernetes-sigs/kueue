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

- [Setup role-based access control](manage/rbac)
  to Kueue objects.
- [Administer cluster quotas](manage/administer_cluster_quotas) with ClusterQueues and LocalQueues.
- Setup [Sequential Admission with Ready Pods](manage/setup_sequential_admission).
- As a batch administrator, you can learn how to
  [monitor pending workloads](manage/monitor_pending_workloads).
- As a batch administrator, you can learn how to [run a Kueue managed Jobs with a custom WorkloadPriority](manage/run_job_with_workload_priority).
- As a batch administrator, you can learn how to [setup a MultiKueue environment](manage/setup_multikueue).

### Batch user

A _batch user_ runs [workloads](/docs/concepts/workload). A typical
batch user is a researcher, AI/ML engineer, data scientist, among others.

As a batch user, you can learn how to:
- [Run a Kueue managed batch/Job](run/jobs).
- [Run a Kueue managed Flux MiniCluster](run/flux_miniclusters).
- [Run a Kueue managed Kubeflow Job](run/kubeflow).
  Kueue supports MPIJob v2beta1, PyTorchJob, TFJob, XGBoostJob, PaddleJob, and MXJob.
- [Run a Kueue managed KubeRay RayJob](run/rayjobs).
- [Run a Kueue managed KubeRay RayCluster](run/rayclusters).
- [Submit Kueue jobs from Python](run/python_jobs).
- [Run a Kueue managed plain Pod](run/plain_pods).
- [Run a Kueue managed JobSet](run/jobsets).

### Platform developer

A _platform developer_ integrates Kueue with other software and/or contributes to Kueue.

As a platform developer, you can learn how to:
- [Integrate a custom Job with Kueue](dev/integrate_a_custom_job).
- [Enable pprof endpoints](dev/enabling_pprof_endpoints).

## Troubleshooting

Sometimes things go wrong.
You can follow the [Troubleshooting guides](troubleshooting) to understand the state of the system.
