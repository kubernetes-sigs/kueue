---
title: "Run a SparkApplication"
date: 2025-11-17
weight: 7
description: >
  Run a Kueue scheduled SparkApplication
---

{{< feature-state state="alpha" for_version="v0.17" >}}

This page shows how to leverage Kueue's scheduling and resource management capabilities when running [Spark Operator](https://github.com/kubeflow/spark-operator) SparkApplication.

This guide is for [batch users](/docs/tasks#batch-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/docs/overview).

{{% alert title="Note" color="primary" %}}
`SparkApplicationIntegration` is currently an alpha feature and is disabled by default.

To enable it, the `SparkApplicationIntegration` feature gate needs to be activated, and `sparkoperator.k8s.io/sparkapplication` must be added as an allowed workload.

{{% /alert %}}

## Before you begin

Enable SparkApplication integration in Kueue. You can [modify Kueue configurations from installed releases](/docs/installation#install-a-custom-configured-released-version) to include `sparkoperator.k8s.io/sparkapplication` as an allowed workload.

Enable the `SparkApplicationIntegration` feature gate. Check the [installation](/docs/installation/#change-the-feature-gates-configuration) guide for details on feature gate configuration.

Check [administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) for details on the initial cluster setup.

Check [the Spark Operator installation guide](https://www.kubeflow.org/docs/components/spark-operator/getting-started/#installation).

{{% alert title="Note" color="primary" %}}
In order to use SparkApplication integration, you must install [Spark Operator](https://github.com/kubeflow/spark-operator) [v2.4.0](https://github.com/kubeflow/spark-operator/releases/tag/v2.4.0) or above.

Please also remember belows:
- You will have to activate namespaces that you will deploy SparkApplication as described in [the official installation docs](https://www.kubeflow.org/docs/components/spark-operator/getting-started/#about-spark-job-namespaces).
- You will have to create spark serviceaccount and attach a proper role beforehand. Please refer to [Spark Operator's Getting Started Guide](https://www.kubeflow.org/docs/components/spark-operator/getting-started/#about-the-service-account-for-driver-pods) for details.
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
