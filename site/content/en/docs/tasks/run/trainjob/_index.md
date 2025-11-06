---
title: "Kubeflow TrainJob (v2)"
linkTitle: "Kubeflow TrainJob (v2)"
weight: 6
date: 2024-11-05
description: >
  Run Kueue managed Kubeflow TrainJob from Trainer v2
no_list: true
---

The page below shows you how to run Kueue managed Kubeflow TrainJob.

**Kubeflow Trainer v2** introduces the unified `TrainJob` API that replaces framework-specific job types (PyTorchJob, TFJob, etc.) with a runtime-based approach.

{{% alert title="Note" color="primary" %}}
Kubeflow Trainer v2 is currently in **alpha**. For production workloads, see [Kubeflow Jobs (v1)](/docs/tasks/run/kubeflow/).
{{% /alert %}}

- [Run a Kueue managed TrainJob](/docs/tasks/run/trainjob/trainjobs/)
