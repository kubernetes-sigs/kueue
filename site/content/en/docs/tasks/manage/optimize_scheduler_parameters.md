---
title: "Optimize Scheduler Parameters"
date: 2026-11-26
weight: 14
description: >
  Tune the Kueue scheduler parameters in the Kueue Configuration.
---

This page shows you how to tune the Kueue scheduler parameters in the Kueue
Configuration. These parameters do not configure kube-scheduler.

The intended audience for this page are [batch administrators](/docs/tasks#batch-administrator).

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- The kubectl command-line tool has communication with your cluster.
- [Kueue is installed](/docs/installation).

## Choose a parameter

Start from the symptom that you see in your cluster. The following table lists
the parameters on this page and the symptom that each parameter addresses:

| Symptom | Parameter | Section |
| --- | --- | --- |
| Workloads behind the head of a large `BestEffortFIFO` ClusterQueue wait too long for admission. | `inadmissibleWorkloadRequeuer.batchPeriod` | [Inadmissible workload requeuer](#inadmissible-workload-requeuer) |

## Inadmissible workload requeuer

{{< feature-state state="beta" for_version="v0.21" >}}

The `inadmissibleWorkloadRequeuer.batchPeriod` field sets how long Kueue waits
before it moves inadmissible workloads back to the queue. The field applies to
the whole cluster. You cannot set it per ClusterQueue. The field requires the
`ConfigurableInadmissibleWorkloadRequeuer` feature gate, which is enabled by
default since v0.21.

### How the batch period works

In a `BestEffortFIFO` ClusterQueue, a workload that fails admission becomes
inadmissible. Kueue does not evaluate an inadmissible workload again until a
change happens that can make it admissible. The following changes are some
examples:

- A workload finishes, is deleted, or is evicted.
- A ClusterQueue, a [Cohort](/docs/concepts/cohort), a ResourceFlavor, or an
  AdmissionCheck changes.

After such a change, Kueue waits for the batch period. Then Kueue moves the
inadmissible workloads of the affected ClusterQueues back to the queue. Changes
that happen during the wait do not extend it. If no such change happens, the
workloads stay inadmissible. The batch period is not a resync period.

In a `StrictFIFO` ClusterQueue, a workload that fails admission usually goes
back to the queue at once, so the batch period has little effect. For the
differences between the queueing strategies, see
[Queueing strategy](/docs/concepts/cluster_queue/#queueing-strategy).

### When to change the batch period

Consider a longer batch period when workloads behind the head of a large
`BestEffortFIFO` ClusterQueue wait too long for admission.

In a large cluster where workloads finish often, the default batch period of 1s
moves inadmissible workloads back to the queue about every second. Kueue orders
the queue by priority and creation time, so the same workloads return to the
head each time. The Kueue scheduler keeps evaluating these workloads and does
not reach the workloads behind them. With a longer batch period, workloads that
fail admission stay inadmissible for longer, so the Kueue scheduler moves on to
the workloads behind them.

{{% alert title="Note" color="primary" %}}
A longer batch period can delay admission after quota frees up. It also makes
Kueue more likely to admit new workloads before older inadmissible workloads.
{{% /alert %}}

### Configure the batch period

The default batch period is 1s. The value is a duration string, such as `30s`
or `2m`. The value must be greater than 0s and at most 10m.

Follow the
[installation instructions for using a custom configuration](/docs/installation/#install-a-custom-configured-released-version)
and extend the Kueue Configuration. The following example sets the batch period
to 30s:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
inadmissibleWorkloadRequeuer:
  batchPeriod: 30s
```

For the field definition, see the
[`InadmissibleWorkloadRequeuer` API reference](/docs/reference/kueue-config.v1beta2/#config-kueue-x-k8s-io-v1beta2-InadmissibleWorkloadRequeuer).

## What's next

- Read the [Kueue Configuration API reference](/docs/reference/kueue-config.v1beta2/).
- Read the [metrics reference](/docs/reference/metrics/).
