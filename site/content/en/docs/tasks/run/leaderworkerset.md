---
title: "Run LeaderWorkerSet"
linkTitle: "LeaderWorkerSet"
date: 2025-02-17
weight: 6
description: >
   Run a LeaderWorkerSet as a Kueue-managed workload.
---

This page shows how to leverage Kueue's scheduling and resource management
capabilities when running [LeaderWorkerSet](https://github.com/kubernetes-sigs/lws).

We demonstrate how to support scheduling LeaderWorkerSets where a group of Pods constitutes
a unit of admission represented by a Workload. This allows to scale-up and down LeaderWorkerSets
group by group.

This integration is based on the [Plain Pod Group](https://kueue.sigs.k8s.io/docs/tasks/run/plain_pods/) integration.

This guide is for [serving users](/docs/tasks#serving-user) that have a basic understanding of Kueue.
For more information, see [Kueue's overview](/docs/overview).

## Before you begin

1. The `leaderworkerset.x-k8s.io/leaderworkerset` integration is enabled by default.

2. For Kueue v0.15 and earlier, learn how to [install Kueue with a custom manager configuration](/docs/installation/#install-a-custom-configured-released-version)
   and ensure that you have the `leaderworkerset.x-k8s.io/leaderworkerset` integration enabled, for example:
   ```yaml
   apiVersion: config.kueue.x-k8s.io/v1beta2
   kind: Configuration
   integrations:
     frameworks:
      - "leaderworkerset.x-k8s.io/leaderworkerset"
   ```
   {{% alert title="Pod integration requirements" color="primary" %}}
   Since Kueue v0.15, you don't need to explicitly enable `"pod"` integration to use the `"leaderworkerset.x-k8s.io/leaderworkerset"` integration.

   For Kueue v0.14 and earlier, `"pod"` integration must be explicitly enabled.

   See [Run Plain Pods](/docs/tasks/run/plain_pods/#before-you-begin) for configuration details.
   {{% /alert %}}

3. Check [Administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) for details on the initial Kueue setup.

## Running a LeaderWorkerSet admitted by Kueue

When running a LeaderWorkerSet on Kueue, take into consideration the following aspects:

### a. Queue selection

The target [local queue](/docs/concepts/local_queue) should be specified in the `metadata.labels` section of the LeaderWorkerSet configuration.

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. Configure the resource needs
The resource needs of the workload can be configured in the `spec.template.spec.containers`.

```yaml
spec:
  leaderWorkerTemplate:
    leaderTemplate:
      spec:
        containers:
          - resources:
              requests:
                cpu: "100m"
    workerTemplate:
      spec:
        containers:
          - resources:
              requests:
                cpu: "100m"
```

### c. Scaling

You can perform scale up or scale down operations on a LeaderWorkerSet `.spec.replicas`.

The unit of scaling is a LWS group. By changing the number of `replicas` in the LWS you can create
or delete entire groups of Pods. As a result of scale up the newly created group of Pods is
suspended by a scheduling gate, until the corresponding Workload is admitted.

## Example
Here is a sample LeaderWorkerSet:

{{< include "examples/serving-workloads/sample-leaderworkerset.yaml" "yaml" >}}

You can create the LeaderWorkerSet using the following command:

```sh
kubectl create -f sample-leaderworkerset.yaml
```

## Configure Topology Aware Scheduling

For performance-sensitive workloads like large-scale inference or distributed training, you may require the Leader and Worker pods to be co-located within a specific network topology domain (e.g., a rack or a data center block) to minimize latency.

Kueue supports Topology Aware Scheduling (TAS) for LeaderWorkerSet by reading annotations from the Pod templates. To enable this:

- [Configure the cluster for Topology Aware Scheduling](/docs/concepts/topology_aware_scheduling).
- Add the `kueue.x-k8s.io/podset-required-topology` annotation to both the `leaderTemplate` and the `workerTemplate`.
- Add the `kueue.x-k8s.io/podset-group-name` annotation to both the `leaderTemplate` and the `workerTemplate` with the same value. This ensures that the Leader and Workers are scheduled in the same topology domain.

### Example: Rack-Level Co-location

The following example uses the `podset-group-name` annotation to ensure that the Leader and all Workers are scheduled within the same rack (represented by the `cloud.provider.com/topology-rack` label).

{{< include "examples/serving-workloads/sample-leaderworkerset-tas.yaml" "yaml" >}}

When `replicas` is greater than 1 (as in the example above where `replicas: 2`), the topology constraints apply to each replica individually. This means that for each replica, the Leader and its Workers will be co-located in the same topology domain (e.g., rack), but different replicas may be assigned to different topology domains.

## Multikueue
Check [MultiKueue](/docs/tasks/run/multikueue/leaderworkerset) for details on running LeaderWorkerSets in MultiKueue environment.
