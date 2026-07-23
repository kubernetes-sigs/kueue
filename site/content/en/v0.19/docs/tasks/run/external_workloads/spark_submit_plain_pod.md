---
title: "Run Spark with spark-submit using Plain Pods"
linkTitle: "Spark (Plain Pods)"
date: 2026-06-24
weight: 9
description: >
  Integrate Kueue with Spark applications submitted via spark-submit using the Plain Pod integration.
---

This page shows how to leverage Kueue's scheduling and resource management capabilities when running
[Spark](https://spark.apache.org/docs/latest/running-on-kubernetes.html) applications submitted with
`spark-submit` directly to Kubernetes, i.e. without the [Spark Operator](https://github.com/kubeflow/spark-operator).

This guide is for [batch users](/v0.19/docs/tasks#batch-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/v0.19/docs/overview).

If you run Spark through the Spark Operator, prefer the first-class
[SparkApplication integration](/v0.19/docs/tasks/run/kubeflow/sparkapplications/) instead.

When using `spark-submit` in cluster mode, Spark creates a driver Pod that in turn creates the
executor Pods. Kueue can manage these Pods through the [Plain Pod](/v0.19/docs/tasks/run/plain_pods)
integration, where the driver Pod and each executor Pod are represented as independent Plain Pods.

{{% alert title="Note" color="warning" %}}
This is not an officially supported Kueue integration for Spark; it is a community workaround that
works with the current Plain Pod implementation. It relies on explicitly opting executor Pods into
Kueue's `pod` integration and may change in a future release. See
[issue #4106](https://github.com/kubernetes-sigs/kueue/issues/4106) for the discussion on a
first-class solution.
{{% /alert %}}

## Before you begin

1. Learn how to [install Kueue with a custom manager configuration](/v0.19/docs/installation/#install-a-custom-configured-released-version).

1. Follow the steps in [Run Plain Pods](/v0.19/docs/tasks/run/plain_pods/#before-you-begin) to learn how to enable and configure the `pod` integration.

1. Check [Administer cluster quotas](/v0.19/docs/tasks/manage/administer_cluster_quotas) for details on the initial Kueue setup.

1. Install [Spark](https://spark.apache.org/downloads.html) and learn how to
[run Spark on Kubernetes](https://spark.apache.org/docs/latest/running-on-kubernetes.html) with `spark-submit`.

## Queue selection

Kueue can manage the Spark driver Pod through the regular Plain Pod flow. Explicitly set the
`kueue.x-k8s.io/queue-name` label on the driver Pod, or rely on a default LocalQueue if one is
configured in the namespace.

Executor Pods created by the Spark driver need additional metadata to opt into the Plain Pod
integration. Set the following on each executor Pod:

- the `kueue.x-k8s.io/queue-name` label, to select the target [local queue](/v0.19/docs/concepts/local_queue);
- the `kueue.x-k8s.io/managed: "true"` label, which marks the Pod as managed by Kueue;
- the `kueue.x-k8s.io/pod-suspending-parent` annotation, which opts the Pod into the `pod`
  integration even though its owner is another Pod (the Spark driver).

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
    kueue.x-k8s.io/managed: "true"
  annotations:
    kueue.x-k8s.io/pod-suspending-parent: spark-driver
```

The value of the `kueue.x-k8s.io/pod-suspending-parent` annotation is a free-form description of the
suspending parent and is not validated by Kueue, so you can use any value that documents the intent
(for example `spark-driver`).

With `spark-submit`, the equivalent configuration is set through `--conf` flags:

```bash
spark-submit \
  --master k8s://https://<k8s-apiserver>:<port> \
  --deploy-mode cluster \
  --conf spark.kubernetes.driver.label.kueue.x-k8s.io/queue-name=user-queue \
  --conf spark.kubernetes.executor.label.kueue.x-k8s.io/queue-name=user-queue \
  --conf spark.kubernetes.executor.label.kueue.x-k8s.io/managed=true \
  --conf spark.kubernetes.executor.annotation.kueue.x-k8s.io/pod-suspending-parent=spark-driver \
  ...
```

Once the corresponding Workload is admitted, Kueue removes the scheduling gate and the Pod is
scheduled by kube-scheduler.

## Limitations

- Kueue represents the Spark driver Pod and each executor Pod as independent Plain Pod Workloads,
  not the Spark application as a single Workload. As a result, Kueue does not admit the driver and
  executors atomically.
