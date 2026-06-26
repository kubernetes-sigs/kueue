---
title: "Run a JAXJob"
date: 2025-04-23
weight: 6
description: >
  Run a Kueue scheduled JAXJob
---

This page shows how to leverage Kueue's scheduling and resource management capabilities when running [Trainer](https://www.kubeflow.org/docs/components/training/jax/) JAXJobs.

This guide is for [batch users](/docs/tasks#batch-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/docs/overview).

{{% alert title="Warning" color="warning" %}}
**Deprecation Notice:** The integration with [Kubeflow Trainer v1](https://www.kubeflow.org/docs/components/trainer/legacy-v1/) (including JAXJob) is **deprecated** in Kueue and will be removed in a future release, tentatively **v0.20**.

Kubeflow Trainer v1 is now legacy. We strongly recommend migrating to [Kubeflow Trainer v2](https://github.com/kubeflow/trainer) (which is supported in Kueue via [TrainJob](/docs/tasks/run/trainjobs/)), or using an alternative framework such as [JobSet](/docs/tasks/run/jobsets/) to run your jobs.
{{% /alert %}}

## Before you begin

Check [administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) for details on the initial cluster setup.

Check [the Trainer installation guide](https://github.com/kubeflow/training-operator#installation).

Note that the minimum requirement trainer version is v1.9.0.

You can [modify kueue configurations from installed releases](/docs/installation#install-a-custom-configured-released-version) to include JAXJobs as an allowed workload.

{{% alert title="Note" color="primary" %}}
In order to use Trainer, prior to v0.8.1, you need to restart Kueue after the installation.
You can do it by running: `kubectl delete pods -l control-plane=controller-manager -n kueue-system`.
{{% /alert %}}

## JAXJob definition

### a. Queue selection

The target [local queue](/docs/concepts/local_queue) should be specified in the `metadata.labels` section of the JAXJob configuration.

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. Optionally set Suspend field in JAXJobs

```yaml
spec:
  runPolicy:
    suspend: true
```

By default, Kueue will set `suspend` to true via webhook and unsuspend it when the JAXJob is admitted.

## Sample JAXJob

This example is based on https://github.com/kubeflow/trainer/blob/da11d1116c29322c481d0b8f174df8d6f05004aa/examples/jax/cpu-demo/demo.yaml.

{{< include "examples/jobs/sample-jaxjob.yaml" "yaml" >}}
