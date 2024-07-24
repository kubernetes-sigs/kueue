---
title: "Run a MPIJob"
date: 2023-05-16
weight: 6
description: >
  Run a Kueue scheduled MPIJob
---

This page shows how to leverage Kueue's scheduling and resource management capabilities when running [MPI Operator](https://www.kubeflow.org/docs/components/training/mpi/) MPIJobs.

This guide is for [batch users](/docs/tasks#batch-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/docs/overview).

## Before you begin

Check [administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) for details on the initial cluster setup.

Check [the MPI Operator installation guide](https://github.com/kubeflow/mpi-operator#installation).

You can [modify kueue configurations from installed releases](/docs/installation#install-a-custom-configured-released-version) to include MPIJobs as an allowed workload.

{{% alert title="Note" color="note" %}}
In order to use MPIJob you need to restart Kueue after the installation.
You can do it by running: `kubectl delete pods -lcontrol-plane=controller-manager -nkueue-system`.
{{% /alert %}}

## MPI Operator definition

### a. Queue selection

The target [local queue](/docs/concepts/local_queue) should be specified in the `metadata.labels` section of the MPIJob configuration.

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. Optionally set Suspend field in MPIJobs

```yaml
spec:
  runPolicy:
    suspend: true
```

By default, Kueue will set `suspend` to true via webhook and unsuspend it when the MPIJob is admitted.

## Sample MPI Job

This example is based on https://github.com/kubeflow/mpi-operator/blob/ccf2756f749336d652fa6b10a732e241a40c7aa6/examples/v2beta1/pi/pi.yaml.

{{< include "examples/jobs/sample-mpijob.yaml" "yaml" >}}

For equivalent instructions for doing this in Python, see [Run Python Jobs](/docs/tasks/run/python_jobs/#mpi-operator-job).
