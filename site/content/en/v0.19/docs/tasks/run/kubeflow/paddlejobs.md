---
title: "Run a PaddleJob"
date: 2023-09-22
weight: 6
description: >
  Run a Kueue scheduled PaddleJob
---

This page shows how to leverage Kueue's scheduling and resource management capabilities when running [Trainer](https://www.kubeflow.org/docs/components/training/paddlepaddle/) PaddleJobs.

This guide is for [batch users](/v0.19/docs/tasks#batch-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/v0.19/docs/overview).

{{% alert title="Warning" color="warning" %}}
**Deprecation Notice:** The integration with [Kubeflow Trainer v1](https://www.kubeflow.org/docs/components/trainer/legacy-v1/) (including PaddleJob) is **deprecated** in Kueue and will be removed in a future release, tentatively **v0.20**.

Kubeflow Trainer v1 is now legacy. We strongly recommend migrating to [Kubeflow Trainer v2](https://github.com/kubeflow/trainer) (which is supported in Kueue via [TrainJob](/v0.19/docs/tasks/run/trainjobs/)), or using an alternative framework such as [JobSet](/v0.19/docs/tasks/run/jobsets/) to run your jobs. See the [Kubeflow Trainer v1 to v2 migration guide](https://trainer.kubeflow.org/en/latest/operator-guides/migration.html) for details on how to migrate.
{{% /alert %}}

## Before you begin

Check [administer cluster quotas](/v0.19/docs/tasks/manage/administer_cluster_quotas) for details on the initial cluster setup.

Check [the Trainer installation guide](https://www.kubeflow.org/docs/components/training/installation/).

Note that the minimum requirement trainer version is v1.7.0.

You can [modify kueue configurations from installed releases](/v0.19/docs/installation#install-a-custom-configured-released-version) to include PaddleJobs as an allowed workload.

{{% alert title="Note" color="primary" %}}
In order to use Trainer, prior to v0.8.1, you need to restart Kueue after the installation.
You can do it by running: `kubectl delete pods -l control-plane=controller-manager -n kueue-system`.
{{% /alert %}}

## PaddleJob definition

### a. Queue selection

The target [local queue](/v0.19/docs/concepts/local_queue) should be specified in the `metadata.labels` section of the PaddleJob configuration.

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. Optionally set Suspend field in PaddleJobs

```yaml
spec:
  runPolicy:
    suspend: true
```

By default, Kueue will set `suspend` to true via webhook and unsuspend it when the PaddleJob is admitted.

## Sample PaddleJob

This example is based on https://github.com/kubeflow/trainer/blob/288d680a699237fb61a74ada005e202721815ff2/examples/paddlepaddle/simple-cpu.yaml.

{{< include "v0.19/examples/jobs/sample-paddlejob.yaml" "yaml" >}}
