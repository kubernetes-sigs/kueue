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
- Setup [All-or-nothing with ready Pods](manage/setup_wait_for_pods_ready).
- Set up [observability](manage/observability) with Prometheus metrics.
- As a batch administrator, you can learn how to
  [monitor pending workloads](manage/monitor_pending_workloads).
- As a batch administrator, you can learn how to [run a Kueue managed Jobs with a custom WorkloadPriority](manage/run_job_with_workload_priority).
- As a batch administrator, you can learn how to [setup a MultiKueue environment](manage/setup_multikueue).
- As a batch administrator, you can learn how to [use third-party certificate authority with Kueue](manage/productization/cert_manager).

### Batch user

A _batch user_ runs [workloads](/docs/concepts/workload). A typical
batch user is a researcher, AI/ML engineer, data scientist, among others.

As a batch user, you can learn how to:
- [Run a Kueue managed batch/Job](run/jobs).
- [Run a Kueue managed Kubeflow TrainJob (v2)](run/trainjobs).
  Kueue supports Kubeflow Trainer v2's unified TrainJob API.
- [Run a Kueue managed Kubeflow Job (v1)](run/kubeflow).
  Kueue supports MPIJob v2beta1, PyTorchJob, TFJob, XGBoostJob and PaddleJob.
- [Run a Kueue managed KubeRay RayJob](run/rayjobs).
- [Run a Kueue managed KubeRay RayCluster](run/rayclusters).
- [Submit Kueue jobs from Python](run/python_jobs).
- [Run a Kueue managed plain Pod](run/plain_pods).
- [Run a Kueue managed JobSet](run/jobsets).
- [Submit jobs to MultiKueue](run/multikueue).
- [Run external workloads](run/external_workloads).
  Kueue allows one to use built-in integrations (such as Pods or Jobs) to run external workloads.

### Serving user

A _serving user_ runs [workloads](/docs/concepts/workload). 
A serving user runs serving workloads, for example, to expose a trained AI/ML model for inference.

As a serving user, you can learn how to:
- [Run a Kueue managed Deployment](run/deployment).
- [Run a Kueue managed StatefulSet](run/statefulset).
- [Run a Kueue managed LeaderWorkerSet](run/leaderworkerset).
- [Run a Kueue managed KubeRay RayService](run/rayservices).

### Platform developer

A _platform developer_ integrates Kueue with other software and/or contributes to Kueue.

As a platform developer, you can learn how to:
- [Integrate a custom Job with Kueue](dev/integrate_a_custom_job).
- [Integrate a custom workload with Kueue using built-in frameworks](dev/external_frameworks).
- [Enable pprof endpoints](dev/enabling_pprof_endpoints).
- [Develop a custom AdmissionCheck Controller](dev/develop-acc).
- Set up a local [dev monitoring environment](dev/setup_dev_monitoring) with Prometheus.

## Troubleshooting

Sometimes things go wrong.
You can follow the [Troubleshooting guides](troubleshooting) to understand the state of the system.
