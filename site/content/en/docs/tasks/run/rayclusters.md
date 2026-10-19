---
title: "Run A RayCluster"
linkTitle: "RayClusters"
date: 2024-08-07
weight: 10
description: >
  Run a RayCluster with Kueue.
---

This page shows how to leverage Kueue's scheduling and resource management capabilities when running [RayCluster](https://docs.ray.io/en/latest/cluster/kubernetes/getting-started/raycluster-quick-start.html).

This guide is for [batch users](/docs/tasks#batch-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/docs/overview).

## Before you begin

1. Make sure you are using Kueue v0.6.0 version or newer and KubeRay v1.1.0 or newer.

2. Check [Administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) for details on the initial Kueue setup.

3. See [KubeRay Installation](https://docs.ray.io/en/latest/cluster/kubernetes/getting-started/raycluster-quick-start.html#step-2-deploy-a-kuberay-operator) for installation and configuration details of KubeRay.

{{% alert title="Note" color="primary" %}}
In order to use RayCluster, prior to v0.8.1, you need to restart Kueue after the installation.
You can do it by running: `kubectl delete pods -l control-plane=controller-manager -n kueue-system`.
{{% /alert %}}

## RayCluster definition

When running [RayClusters](https://docs.ray.io/en/latest/cluster/kubernetes/getting-started/raycluster-quick-start.html) on
Kueue, take into consideration the following aspects:

### a. Queue selection

The target [local queue](/docs/concepts/local_queue) should be specified in the `metadata.labels` section of the RayCluster configuration.

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. Configure the resource needs

The resource needs of the workload can be configured in the `spec`.

```yaml
spec:
  headGroupSpec:
    template:
      spec:
        containers:
          - resources:
              requests:
                cpu: "1"
  workerGroupSpecs:
    - template:
        spec:
          containers:
            - resources:
                requests:
                  cpu: "1"
```

Note that a RayCluster will hold resource quotas while it exists. For optimal resource management, you should delete a RayCluster that is no longer in use.

### c. Suspend control

Kueue controls the `spec.suspend` field of the RayCluster. When a RayCluster is admitted by Kueue, Kueue will unsuspend it by setting `spec.suspend` to `false`, regardless of its previous value.

### d. Limitations
- Limited Worker Groups: Because a Kueue workload can have a maximum of 18 PodSets, the maximum number of `spec.workerGroupSpecs` is 17
- In-Tree Autoscaling Constraints: Autoscaling is only supported for [elastic](/docs/concepts/elastic_workload) RayCluster objects. To enable in-tree autoscaling:

  1. Activate the `ElasticJobsViaWorkloadSlices` feature gate.
  2. Annotate the RayCluster object with:

     ```yaml
     metadata:
       annotations:
         kueue.x-k8s.io/elastic-job: "true"
     ```
  3. Enable the Ray autoscaler of your RayCluster object by setting:

     ```yaml
     spec:
       enableInTreeAutoscaling: true
     ```

## Partial preemption

{{< feature-state state="alpha" for_version="v0.21" >}}

Partial preemption lets Kueue shrink a lower-priority elastic RayCluster instead of evicting it. Kueue lowers the worker group's `replicas` to free quota, and the head Pod keeps running.

This requires the `PartialPreemption` and `ElasticJobsViaWorkloadSlices` feature gates. To opt in, annotate the RayCluster and set a `minReplicas` floor on its worker group:

```yaml
metadata:
  annotations:
    kueue.x-k8s.io/elastic-job: "true"
    kueue.x-k8s.io/partial-preemption: "true"
    kueue.x-k8s.io/partial-preemption-max-reclaimed-count: "2" # optional
spec:
  workerGroupSpecs:
    - groupName: workers
      replicas: 4
      minReplicas: 1
      maxReplicas: 4
```

The optional `kueue.x-k8s.io/partial-preemption-max-reclaimed-count` annotation caps the number of workers reclaimed in one preemption round.

Opting in sets the worker PodSet `minCount` to `minReplicas`. As a result, the RayCluster can also be partially admitted at first admission: Kueue lowers the worker `replicas` to the admitted count, which is never fewer than `minReplicas`. This is consistent with KEP-975, where `minCount` is the floor for both admission and preemption.

Kueue lowers the worker `replicas` to the reclaim target, which the scheduler keeps at or above `minReplicas`. Quota is released as soon as the worker group `replicas` (the spec count) are lowered, before KubeRay finishes deleting the surplus worker Pods.

KubeRay chooses which workers to delete when `replicas` are lowered, and may delete busy ones.

When Kueue shrinks, it records the original worker `replicas` and the value it set. If you change `replicas` between two shrinks, your value becomes the new original. When the RayCluster is evicted or suspended by Kueue, `replicas` go back to the original only if you have not changed them since Kueue's last shrink. Otherwise your value is kept. Kueue tracks this in an annotation on the RayCluster.

Limitations:
- The RayCluster must have exactly one worker group.
- `enableInTreeAutoscaling` must be `false`.
- `numOfHosts` must be at most `1` (unset or `1`).
- It cannot be combined with `kueue.x-k8s.io/elastic-job-scale-up-strategy: partial`.
- RayJob and RayService are not supported.
- While the RayCluster keeps running, reclaimed workers are not added back automatically. Scale `replicas` up again manually.
- Reclaim targets are not applied while a scale-up (a pending replacement Workload slice) is in progress. Shrinking happens only once the scale-up completes or is abandoned.
- MultiKueue is not supported: the annotation is rejected for RayClusters managed by MultiKueue (`spec.managedBy`).
- The `kueue.x-k8s.io/partial-preemption` annotation is immutable. It cannot be added, removed or changed after creation, regardless of suspension.
- The worker group's `minReplicas` is immutable for opted-in RayClusters.
- If the Workload is deleted while the RayCluster is shrunk, nothing restores the original worker `replicas`.

## Example RayCluster

The RayCluster looks like the following:

{{< include "examples/jobs/ray-cluster-sample.yaml" "yaml" >}}

You can submit a Ray Job using the [CLI](https://docs.ray.io/en/latest/cluster/running-applications/job-submission/quickstart.html) or log into the Ray Head and execute a job following this [example](https://ray-project.github.io/kuberay/deploy/helm-cluster/#end-to-end-example) with kind cluster.

{{% alert title="Note" color="primary" %}}
The example above comes from [here](https://raw.githubusercontent.com/ray-project/kuberay/v1.4.2/ray-operator/config/samples/ray-cluster.complete.yaml)
and only has the `queue-name` label added and requests updated.
{{% /alert %}}
