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

### d. Updating the Pod templates

While Kueue manages a LeaderWorkerSet, it rejects changes to the fields of the Pod templates
that affect admission, such as the resource requests, the `nodeSelector` or the `affinity`.

Since Kueue v0.21, you can add `tolerations` to the Pod templates, for example to tolerate a new node taint.
Existing tolerations cannot be removed or changed, because the new Pods could then fail to run
on the nodes that Kueue already assigned to the LeaderWorkerSet.
Kueue copies the new tolerations to the Workloads of the LeaderWorkerSet:

- A pending Workload can be admitted to the nodes whose taints they tolerate.
- An admitted Workload keeps its admission, and Topology Aware Scheduling checks node taints
  against the new tolerations, so the restarted Pods can keep running on the assigned nodes.

Kueue copies the new tolerations to the Workloads of all groups right away, while LeaderWorkerSet
updates their Pods gradually. Until this rolling update completes:

- Do not add the taints that the new tolerations match.
- Do not raise `rolloutStrategy.rollingUpdateConfiguration.partition`. LeaderWorkerSet would keep
  the groups below the partition, and the groups that a scale-up creates there, on the Pods
  without the new tolerations.

Kueue rejects adding tolerations while the partition is greater than 0, because LeaderWorkerSet
does not update the groups below the partition.

In MultiKueue, Kueue also copies the new tolerations to the Workloads and the LeaderWorkerSet
on the worker cluster. Together with the tolerations, Kueue copies the
`rolloutStrategy.rollingUpdateConfiguration.partition`, so that the worker cluster updates all groups too.

To turn this off, disable the `AllowAddingPodTemplateTolerations`
[feature gate](/docs/getting-started/installation/#feature-gates-for-alpha-and-beta-features).

### e. Group identity

Kueue supports only the default `spec.groupIdentity: Ordinal`. With
`spec.groupIdentity: Hash`, the leader Pods are managed by a Deployment and run
without admission, while the worker Pods stay gated.

Kueue rejects `spec.groupIdentity: Hash` when you create a LeaderWorkerSet that
Kueue manages, or when an update brings a LeaderWorkerSet under Kueue management,
for example by adding the `kueue.x-k8s.io/queue-name` label. In a namespace with
a [default LocalQueue](/docs/tasks/manage/enforce_job_management/setup_default_local_queue/),
this includes a LeaderWorkerSet created without the `kueue.x-k8s.io/queue-name`
label, because Kueue sets the label to `default` on create.

A LeaderWorkerSet with `spec.groupIdentity: Hash` that Kueue already manages is
not rejected on update and keeps running this way. This includes one created
before Kueue added this validation, and one that Kueue started to manage without
an update, for example when its namespace started to match
`managedJobsNamespaceSelector` with
[`manageJobsWithoutQueueName`](/docs/tasks/manage/enforce_job_management/manage_jobs_without_queue_name/)
enabled. Kueue still creates its Workloads by group
ordinal, so they can be admitted and reserve quota that no Pod uses. Kueue
returns a warning when you update such a LeaderWorkerSet, for example with
`kubectl apply`. `spec.groupIdentity` is immutable, so delete the
LeaderWorkerSet and create it again with `spec.groupIdentity: Ordinal`.

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

## Troubleshooting

For general troubleshooting guidance, see the [Kueue troubleshooting guide](/docs/tasks/troubleshooting).

### Long LeaderWorkerSet names

By default, Kueue stores the pod-group identifier in the
`kueue.x-k8s.io/pod-group-name` label, which inherits Kubernetes'
63-character label value limit. Because Kueue derives this value by appending
a group suffix to the LeaderWorkerSet name, the effective LWS name limit is
**39 characters**.

Enable the alpha
[`WorkloadIdentifierAnnotations`](/docs/getting-started/installation/#feature-gates-for-alpha-and-beta-features)
feature gate to store the identifier in an annotation instead, removing
Kueue's label-length constraint. With the feature gate enabled, the effective
limit shifts to the upstream LWS constraint of **51 characters**
(see [Unable to Create LWS Object with a Name Exceeding 51 Characters](https://lws.sigs.k8s.io/docs/troubleshooting/#3-unable-to-create-lws-object-with-a-name-exceeding-51-characters) in the LWS troubleshooting guide).
