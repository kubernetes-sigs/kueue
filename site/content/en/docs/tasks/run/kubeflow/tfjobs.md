---
title: "Run a TFJob"
date: 2023-08-23
weight: 6
description: >
  Run a Kueue scheduled TFJob
---

This page shows how to leverage Kueue's scheduling and resource management capabilities when running [Training Operator](https://www.kubeflow.org/docs/components/training/tftraining/) TFJobs.

This guide is for [batch users](/docs/tasks#batch-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/docs/overview).

## Before you begin

Check [administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) for details on the initial cluster setup.

Check [the Training Operator installation guide](https://github.com/kubeflow/training-operator#installation).

Note that the minimum requirement training-operator version is v1.7.0.

You can [modify kueue configurations from installed releases](/docs/installation#install-a-custom-configured-released-version) to include TFJobs as an allowed workload.

{{% alert title="Note" color="note" %}}
In order to use Training Operator you need to restart Kueue after the installation.
You can do it by running: `kubectl delete pods -lcontrol-plane=controller-manager -nkueue-system`.
{{% /alert %}}

## TFJob definition

### a. Queue selection

The target [local queue](/docs/concepts/local_queue) should be specified in the `metadata.labels` section of the TFJob configuration.

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. Optionally set Suspend field in TFJobs

```yaml
spec:
  runPolicy:
    suspend: true
```

By default, Kueue will set `suspend` to true via webhook and unsuspend it when the TFJob is admitted.

## Sample TFJob

This example is based on https://github.com/kubeflow/training-operator/blob/48dbbf0a8e90e52c55ec05d0f689fcbf83c6b441/examples/tensorflow/dist-mnist/tf_job_mnist.yaml.

{{< include "examples/jobs/sample-tfjob.yaml" "yaml" >}}
