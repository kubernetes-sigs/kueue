---
title: "Run a PyTorchJob"
date: 2023-08-09
weight: 6
description: >
  Run a Kueue scheduled PyTorchJob
---

This page shows how to leverage Kueue's scheduling and resource management capabilities when running [Trainer](https://www.kubeflow.org/docs/components/training/pytorch/) PyTorchJobs.

This guide is for [batch users](/docs/tasks#batch-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/docs/overview).

## Before you begin

Check [administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) for details on the initial cluster setup.

Check [the Trainer installation guide](https://www.kubeflow.org/docs/components/training/installation/).

Note that the minimum requirement trainer version is v1.7.0.

You can [modify kueue configurations from installed releases](/docs/installation#install-a-custom-configured-released-version) to include PyTorchJobs as an allowed workload.

{{% alert title="Note" color="primary" %}}
In order to use Trainer, prior to v0.8.1, you need to restart Kueue after the installation.
You can do it by running: `kubectl delete pods -l control-plane=controller-manager -n kueue-system`.
{{% /alert %}}

## PyTorchJob definition

### a. Queue selection

The target [local queue](/docs/concepts/local_queue) should be specified in the `metadata.labels` section of the PyTorchJob configuration.

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. Optionally set Suspend field in PyTorchJobs

```yaml
spec:
  runPolicy:
    suspend: true
```

By default, Kueue will set `suspend` to true via webhook and unsuspend it when the PyTorchJob is admitted.

## Sample PyTorchJob

This example is based on https://github.com/kubeflow/trainer/blob/855e0960668b34992ba4e1fd5914a08a3362cfb1/examples/pytorch/simple.yaml.

{{< include "examples/jobs/sample-pytorchjob.yaml" "yaml" >}}
