---
title: "Run A RayJob"
linkTitle: "RayJobs"
date: 2024-08-07
weight: 6
description: >
  Run a Kueue scheduled RayJob.
---

This page shows how to leverage Kueue's scheduling and resource management capabilities when running [KubeRay's](https://github.com/ray-project/kuberay)
[RayJob](https://docs.ray.io/en/latest/cluster/kubernetes/getting-started/rayjob-quick-start.html).

This guide is for [batch users](/docs/tasks#batch-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/docs/overview).

## Before you begin

1. Make sure you are using Kueue v0.6.0 version or newer and KubeRay v1.1.0 or newer.

2. Check [Administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) for details on the initial Kueue setup.

3. See [KubeRay Installation](https://docs.ray.io/en/latest/cluster/kubernetes/getting-started/raycluster-quick-start.html#step-2-deploy-a-kuberay-operator) for installation and configuration details of KubeRay.

{{% alert title="Note" color="primary" %}}
In order to use RayJob, prior to v0.8.1, you need to restart Kueue after the installation.
You can do it by running: `kubectl delete pods -l control-plane=controller-manager -n kueue-system`.
{{% /alert %}}

## RayJob definition

When running [RayJobs](https://docs.ray.io/en/latest/cluster/kubernetes/getting-started/rayjob-quick-start.html) on
Kueue, take into consideration the following aspects:

### a. Queue selection

The target [local queue](/docs/concepts/local_queue) should be specified in the `metadata.labels` section of the RayJob configuration.

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. Configure the resource needs

The resource needs of the workload can be configured in the `spec.rayClusterSpec`.

```yaml
spec:
  rayClusterSpec:
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

### c. Limitations

- A Kueue managed RayJob cannot use an existing RayCluster.
- The RayCluster should be deleted at the end of the job execution, `spec.ShutdownAfterJobFinishes` should be `true`.
- Because Kueue will reserve resources for the RayCluster, `spec.rayClusterSpec.enableInTreeAutoscaling` should be `false`.
- Because a Kueue workload can have a maximum of 8 PodSets, the maximum number of `spec.rayClusterSpec.workerGroupSpecs` is 7.

## Example RayJob

In this example, the code is provided to the Ray framework via a ConfigMap.

{{< include "examples/jobs/ray-job-code-sample.yaml" "yaml" >}}

The RayJob looks like the following:

{{< include "examples/jobs/ray-job-sample.yaml" "yaml" >}}

You can run this RayJob with the following commands:

```sh
# Create the code ConfigMap (once)
kubectl apply -f ray-job-code-sample.yaml
# Create a RayJob. You can run this command multiple times
# to observe the queueing and admission of the jobs.
kubectl create -f ray-job-sample.yaml
```

{{% alert title="Note" color="primary" %}}
The example above comes from [here](https://raw.githubusercontent.com/ray-project/kuberay/v1.1.1/ray-operator/config/samples/ray-job.sample.yaml) 
and only has the `queue-name` label added and requests updated.
{{% /alert %}}