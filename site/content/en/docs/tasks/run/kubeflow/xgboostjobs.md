---
title: "Run a XGBoostJob"
date: 2023-09-14
weight: 6
description: >
  Run a Kueue scheduled XGBoostJob
---

This page shows how to leverage Kueue's scheduling and resource management capabilities when running [Training Operator](https://www.kubeflow.org/docs/components/training/xgboost/) XGBoostJobs.

This guide is for [batch users](/docs/tasks#batch-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/docs/overview).

## Before you begin

Check [administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) for details on the initial cluster setup.

Check [the Training Operator installation guide](https://github.com/kubeflow/training-operator#installation).

Note that the minimum requirement training-operator version is v1.7.0.

You can [modify kueue configurations from installed releases](/docs/installation#install-a-custom-configured-released-version) to include XGBoostJobs as an allowed workload.

{{% alert title="Note" color="note" %}}
In order to use Training Operator you need to restart Kueue after the installation.
You can do it by running: `kubectl delete pods -lcontrol-plane=controller-manager -nkueue-system`.
{{% /alert %}}

## XGBoostJob definition

### a. Queue selection

The target [local queue](/docs/concepts/local_queue) should be specified in the `metadata.labels` section of the XGBoostJob configuration.

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. Optionally set Suspend field in XGBoostJobs

```yaml
spec:
  runPolicy:
    suspend: true
```

By default, Kueue will set `suspend` to true via webhook and unsuspend it when the XGBoostJob is admitted.

## Sample XGBoostJob

This example is based on https://github.com/kubeflow/training-operator/blob/afba76bc5a168cbcbc8685c7661f36e9b787afd1/examples/xgboost/xgboostjob.yaml.

{{< include "examples/jobs/sample-xgboostjob.yaml" "yaml" >}}
