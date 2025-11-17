---
title: "Run a SparkApplication"
date: 2024-11-17
weight: 7
description: >
  Run a Kueue scheduled SparkApplication
---

This page shows how to leverage Kueue's scheduling and resource management capabilities when running [Spark Operator](https://github.com/kubeflow/spark-operator) SparkApplication.

This guide is for [batch users](/docs/tasks#batch-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/docs/overview).

## Before you begin

Check [administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) for details on the initial cluster setup.

Check [the Spark Operator installation guide](https://www.kubeflow.org/docs/components/spark-operator/getting-started/#installation).

You can [modify kueue configurations from installed releases](/docs/installation#install-a-custom-configured-released-version) to include SparkApplication as an allowed workload.

{{% alert title="Note" color="primary" %}}
In order to use SparkApplication integration, you must install [Spark Operator](https://github.com/kubeflow/spark-operator) [v2.4.0](https://github.com/kubeflow/spark-operator/releases/tag/v2.4.0) or above.

Please also remember that you will have to activate namespaces that you will deploy SparkApplication as described in [the official installation docs](https://www.kubeflow.org/docs/components/spark-operator/getting-started/#about-spark-job-namespaces).
{{% /alert %}}

{{% alert title="Note" color="primary" %}}
In order to use SparkApplication, prior to v0.8.1, you need to restart Kueue after the installation.
You can do it by running: `kubectl delete pods -l control-plane=controller-manager -n kueue-system`.
{{% /alert %}}

## Spark Operator definition


### a. Queue selection

The target [local queue](/docs/concepts/local_queue) should be specified in the `metadata.labels` section of the SparkApplication configuration.

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

{{% alert title="Note" color="primary" %}}
SparkApplication integration does not support dynamic allocation. If you set `spec.dynamicAllocation.enabled=true` in SparkApplication, Kueue will reject such resources in the webhook.
{{% /alert %}}

### b. Optionally set Suspend field in SparkOperation

```yaml
spec:
  suspend: true
```

By default, Kueue will set `suspend` to true via webhook and unsuspend it when the SparkApplication is admitted.

## Sample SparkApplication

{{< include "examples/jobs/sample-sparkapplication.yaml" "yaml" >}}

Please remember that you will have to create spark serviceaccount and attach a proper role beforehand. Please refer to [Spark Operator's Getting Started Guide](https://www.kubeflow.org/docs/components/spark-operator/getting-started/#about-the-service-account-for-driver-pods) for details.
