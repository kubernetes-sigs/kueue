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
Kueue supports dynamic allocation only for SparkApplications with the `kueue.x-k8s.io/elastic-job: "true"` annotation,
see [Dynamic allocation](#d-dynamic-allocation).
If you set `spec.dynamicAllocation.enabled=true` in any other SparkApplication, Kueue will reject it in the webhook.
{{% /alert %}}

### b. Optionally set Suspend field in SparkOperation

```yaml
spec:
  suspend: true
```

By default, Kueue will set `suspend` to true via webhook and unsuspend it when the SparkApplication is admitted.

### c. Resource requests

Spark, not the Spark Operator, computes the CPU and memory requests of the driver and executor Pods
from the SparkApplication configuration. Kueue reproduces this computation to reserve the quota the
Pods will actually use:

- CPU: `coreRequest` if set, otherwise `cores`, otherwise `1`.
- Memory: `memory` (default `1g`) plus `memoryOverhead`. If `memoryOverhead` is not set, the overhead is
  `max(memoryOverheadFactor * memory, 384m)`, where `memoryOverheadFactor` defaults to `0.1` for
  Java and Scala applications and to `0.4` for Python and R applications. Executors of Python
  applications also get `spark.executor.pyspark.memory`, and executors get `spark.memory.offHeap.size`
  when `spark.memory.offHeap.enabled` is `true`.

Memory values use the Java format (for example `512m` or `2g`). The corresponding properties in
`spec.sparkConf` (for example `spark.driver.cores` or `spark.kubernetes.memoryOverheadFactor`) are
honored with the same precedence as `spark-submit` applies: typed driver and executor fields take
precedence over `spec.sparkConf`, which takes precedence over `spec.memoryOverheadFactor`.

{{% alert title="Note" color="primary" %}}
Kueue uses `spec.type` to decide whether an application is a Python or R application, while Spark
decides based on the extension of `spec.mainApplicationFile`. Make sure they agree, otherwise the
default memory overhead factor reserved by Kueue will differ from the one Spark applies.

Resources set through Spark Pod template files (`spark.kubernetes.driver.podTemplateFile` and
`spark.kubernetes.executor.podTemplateFile`) are not taken into account.
{{% /alert %}}

### d. Dynamic allocation

Kueue supports [Spark dynamic allocation](https://spark.apache.org/docs/latest/job-scheduling.html#dynamic-resource-allocation)
by running the SparkApplication as an [elastic workload](/docs/concepts/elastic_workload). To enable it:

1. Activate the `ElasticJobsViaWorkloadSlices` feature gate.
2. Annotate the SparkApplication with:

   ```yaml
   metadata:
     annotations:
       kueue.x-k8s.io/elastic-job: "true"
   ```
3. Enable dynamic allocation in the SparkApplication:

   ```yaml
   spec:
     executor:
       instances: 1
     dynamicAllocation:
       enabled: true
       minExecutors: 0
       maxExecutors: 4
   ```

Kueue then admits the executors that the Spark driver adds or removes while the driver keeps running:

- Kueue adds the `kueue.x-k8s.io/elastic-job` scheduling gate to the executor Pod template, so the executors
  that the driver creates stay gated until a Workload slice that includes them is admitted.
  The webhook rejects a SparkApplication that removes this gate.
- Before the driver starts, the Workload requests the driver and the larger of `spec.executor.instances`
  and `spec.dynamicAllocation.minExecutors` executors.
  Once the driver runs, the executor count follows the executors that the Spark Operator reports as `PENDING`
  or `RUNNING` in `status.executorState`, kept between `spec.dynamicAllocation.minExecutors`
  and `spec.dynamicAllocation.maxExecutors`.
- When the driver adds executors, Kueue creates a new Workload slice for the larger count and removes the
  scheduling gate from the new executors once that slice is admitted.
  When the driver removes executors, Kueue lowers the count of the admitted Workload and releases their quota.
- Scale-up is atomic: the new executors stay gated until all of them fit in the quota.

Dynamic allocation with Kueue has the following limitations:

- The first admission does not take `spec.dynamicAllocation.initialExecutors` into account. When it is higher,
  the additional executors stay gated until the next Workload slice is admitted.
- The Spark Operator tracks at most `--max-tracked-executor-per-app` executors per application (1000 by default).
  Executors beyond that limit are not counted, so they stay gated.
- The driver may delete and recreate executors that stay gated for longer than `spark.kubernetes.allocation.executor.timeout`.
  The new executors are gated too.
- Kueue reads the dynamic allocation settings from `spec.dynamicAllocation` only, not from `spec.sparkConf`.

## Sample SparkApplication

{{< include "examples/jobs/sample-sparkapplication.yaml" "yaml" >}}
