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

### e. Recommended autoscaler settings

With Kueue quota, two Ray autoscaler defaults can leave an elastic Ray workload
partly admitted, or waiting for quota it no longer needs.
Use Ray 2.60.0 or later, and set:

```yaml
spec:
  enableInTreeAutoscaling: true
  autoscalerOptions:
    ...
    env:
    - name: AUTOSCALER_MAX_CONCURRENT_LAUNCHES
      value: "1024" # At least the most worker Pods a single scale-up can add.
    - name: RAY_AUTOSCALER_PENDING_NODE_IDLE_TIMEOUT_S
      value: "60"
  headGroupSpec:
    template:
      spec:
        containers:
        - name: ray-head
          image: rayproject/ray:2.60.0 # Ray 2.60.0+
```

- `AUTOSCALER_MAX_CONCURRENT_LAUNCHES` (default `10`): the autoscaler adds at most
  this many workers per step, so a large scale-up creates several Workload slices.
  Kueue can admit the first slices while later slices wait for quota.
  See [ray-project/ray#67124](https://github.com/ray-project/ray/issues/67124).
- `RAY_AUTOSCALER_PENDING_NODE_IDLE_TIMEOUT_S` (default disabled): removes gated
  pending workers that no resource demand needs after that many seconds.
  Available since Ray 2.60.0, and only with autoscaler v2 (`autoscalerOptions.version: v2`).
  See [ray-project/ray#67102](https://github.com/ray-project/ray/pull/67102).
  For a RayJob, the autoscaler can remove pending workers before the job is
  submitted, because there is no resource demand yet.
  [kuberay-project/kuberay#5434](https://github.com/kuberay-project/kuberay/issues/5434)
  tracks a fix for this and enabling the setting by default.

## Example RayCluster

The RayCluster looks like the following:

{{< include "examples/jobs/ray-cluster-sample.yaml" "yaml" >}}

You can submit a Ray Job using the [CLI](https://docs.ray.io/en/latest/cluster/running-applications/job-submission/quickstart.html) or log into the Ray Head and execute a job following this [example](https://ray-project.github.io/kuberay/deploy/helm-cluster/#end-to-end-example) with kind cluster.

{{% alert title="Note" color="primary" %}}
The example above comes from [here](https://raw.githubusercontent.com/ray-project/kuberay/v1.4.2/ray-operator/config/samples/ray-cluster.complete.yaml)
and only has the `queue-name` label added and requests updated.
{{% /alert %}}
