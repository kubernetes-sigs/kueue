---
title: "Admission Fair Sharing"
date: 2025-05-28
weight: 6
description: >
  A mechanism for ordering workloads based on the historical resource usage of their source LocalQueues, giving preference to those that have consumed fewer resources over time.
---

{{< feature-state state="beta" for_version="v0.15" >}}

{{% alert title="Note" color="primary" %}}
`AdmissionFairSharing` is currently an beta feature and is enabled by default.

You can disable it by editing the `AdmissionFairSharing` feature gate. Check the [Installation](/docs/installation/#change-the-feature-gates-configuration) guide for details on feature gate configuration.
{{% /alert %}}


# Admission Fair Sharing

Admission Fair Sharing helps distribute resources fairly between multiple LocalQueues targeting the same ClusterQueue. It orders workloads based on the historical resource usage of their source LocalQueues, giving preference to those that have consumed less resources over time.

## How it works

When multiple workloads compete for resources within a ClusterQueue:

1. Kueue tracks resource usage history for each LocalQueue
2. Workloads from LocalQueues with lower historical usage get admitted before those from high-usage queues
3. Usage values decay over time based on configurable parameters

### Entry Penalty

{{% alert title="Note" color="primary" %}}
Entry Penalty is available since Kueue v0.13.0.
{{% /alert %}}

To prevent exploitation where tenants could submit many workloads quickly before usage statistics are updated, Kueue applies an entry penalty to each admitted workload. This penalty is immediately added to the LocalQueue's usage statistics. This ensures that even if a tenant submits multiple workloads rapidly, subsequent workloads will be properly prioritized based on the updated usage including the penalty. The point at which the penalty settles is the [accounting anchor](#accounting-anchor).

For example, if Tenant A has low historical usage and Tenant B has high usage, but Tenant B submits 100 workloads simultaneously, without the entry penalty all 100 workloads might be admitted before the usage statistics update. With the entry penalty, each admitted workload immediately increases Tenant B's usage statistics, so subsequent workloads from Tenant B will be properly deprioritized in favor of workloads from Tenant A.

### Accounting anchor
{{< feature-state state="alpha" for_version="v0.20" >}}

Usage is accounted from the point a workload is admitted. The `AdmissionFairSharingReservedAnchor`
feature gate moves that point to quota reservation, for ClusterQueues that use
`UsageBasedAdmissionFairSharing`: a workload then contributes to fair-sharing usage as soon as it
actively holds quota, even while its AdmissionChecks are pending, and its entry penalty settles at the
same point. The reported `consumedResources` does not follow at once, since it is a decaying average:
it closes half of the remaining gap every `usageHalfLifeTime`.

{{% alert title="Note" color="primary" %}}
`AdmissionFairSharingReservedAnchor` is an alpha feature and is disabled by default.

You can enable it by editing the `AdmissionFairSharingReservedAnchor` feature gate. Check the [Installation](/docs/installation/#change-the-feature-gates-configuration) guide for details on feature gate configuration.
{{% /alert %}}

## Configuration

### Kueue configuration

The following parameters can be configured in Kueue's configuration `.admissionFairSharing`:

- `usageHalfLifeTime`: Controls how quickly historical usage decays
- `usageSamplingInterval`: How frequently usage is sampled
- `resourceWeights`: Relative importance of different resource types

#### Example configuration:

```
admissionFairSharing:
  usageHalfLifeTime: "168h"
  usageSamplingInterval: "5m"
  resourceWeights:
    cpu: 2.0 # cpu usage is twice more important than memory usage
    memory: 1.0
```

### ClusterQueue configuration

Enable Admission Fair Sharing by adding an AdmissionScope to your ClusterQueue:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: sample-queue
spec:
  admissionScope:
    admissionMode: UsageBasedAdmissionFairSharing
  resources:
    # ...existing resource configuration...
```

### LocalQueue configuration

You can define a `fairSharing` section in your LocalQueue to adjust its weight in the fair sharing calculation (defaults to `1`):

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: LocalQueue
metadata:
  name: team-a-queue
  namespace: team-a
spec:
  clusterQueue: shared-queue
  fairSharing:
    weight: "2"  # This queue will be treated as if it used half as many resources
```

### Observability

You can track the historical resource usage of each LocalQueue in its `status.FairSharing` e.g. using command:
```
kubectl get lq user-queue -o jsonpath={.status.fairSharing}
```

Output should be similar to:

```
{"admissionFairSharingStatus":{"consumedResources":{"cpu":"31999m"},"lastUpdate":"2025-06-03T14:25:15Z"},"weightedShare":0}
```
