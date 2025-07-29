---
title: "Run A RayCluster"
linkTitle: "RayClusters"
date: 2024-08-07
weight: 6
description: >
  Run a RayCluster on Kueue.
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

### c. Limitations

Kueue supports autoscaling for RayCluster objects if you enable the integration of Kueue with [plain pods](/docs/tasks/run/plain_pods/).

- **If you opt in to Kueue managing plain Pods**, you can enable RayCluster’s autoscaling by setting `spec.rayClusterSpec.enableInTreeAutoscaling` to `true`.

  In this configuration, KubeRay handles autoscaling, and Kueue creates one Workload object per worker Pod. You can define **any number** of worker groups in `spec.workerGroupSpecs`.

- **If you do not opt in to Kueue managing plain Pods**, you must disable RayCluster’s autoscaling by setting `spec.rayClusterSpec.enableInTreeAutoscaling` to `false`.

  In this configuration, Kueue manages resource allocation for the entire RayCluster. You must also **limit the number of worker groups**. Since a Kueue Workload supports a maximum of 8 PodSets, your RayCluster can include **at most 7** entries in the `spec.workerGroupSpecs` field.

The default value of `spec.rayClusterSpec.enableInTreeAutoscaling` in RayCluster objects is `false`.

## Example RayCluster

The RayCluster looks like the following:

{{< include "examples/jobs/ray-cluster-sample.yaml" "yaml" >}}

You can submit a Ray Job using the [CLI](https://docs.ray.io/en/latest/cluster/running-applications/job-submission/quickstart.html) or log into the Ray Head and execute a job following this [example](https://ray-project.github.io/kuberay/deploy/helm-cluster/#end-to-end-example) with kind cluster.

{{% alert title="Note" color="primary" %}}
The example above comes from [here](https://raw.githubusercontent.com/ray-project/kuberay/v1.1.1/ray-operator/config/samples/ray-cluster.complete.yaml)
and only has the `queue-name` label added and requests updated.
{{% /alert %}}