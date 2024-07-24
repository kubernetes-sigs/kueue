---
title: "Run A RayCluster"
linkTitle: "RayClusters"
date: 2024-01-17
weight: 6
description: >
  Run a RayCluster on Kueue.
---

This page shows how to leverage Kueue's scheduling and resource management capabilities when running [RayCluster](https://docs.ray.io/en/latest/cluster/getting-started.html).

This guide is for [batch users](/docs/tasks#batch-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/docs/overview).

## Before you begin

1. Make sure you are using Kueue v0.6.0 version or newer and KubeRay 1.1.0 or newer.

2. Check [Administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) for details on the initial Kueue setup.

3. See [KubeRay Installation](https://ray-project.github.io/kuberay/deploy/installation/) for installation and configuration details of KubeRay.

{{% alert title="Note" color="note" %}}
In order to use RayCluster you need to restart Kueue after the installation.
You can do it by running: `kubectl delete pods -lcontrol-plane=controller-manager -nkueue-system`.
{{% /alert %}}

## RayCluster definition

When running [RayClusters](https://docs.ray.io/en/latest/cluster/getting-started.html) on
Kueue, take into consideration the following aspects:

### a. Queue selection

The target [local queue](/docs/concepts/local_queue) should be specified in the `metadata.labels` section of the RayCluster configuration.

```yaml
metadata:
  name: raycluster-sample
  namespace: default
  labels:
    kueue.x-k8s.io/queue-name: local-queue
```

### b. Configure the resource needs

The resource needs of the workload can be configured in the `spec`.

```yaml
    headGroupSpec:
       spec:
        affinity: {}
        containers:
        - env: []
          image: rayproject/ray:2.7.0
          imagePullPolicy: IfNotPresent
          name: ray-head
          resources:
            limits:
              cpu: "1"
              memory: 2G
            requests:
              cpu: "1"
              memory: 2G
          securityContext: {}
          volumeMounts:
          - mountPath: /tmp/ray
            name: log-volume
    workerGroupSpecs:
      template:
        spec:
          affinity: {}
          containers:
          - env: []
          image: rayproject/ray:2.7.0
          imagePullPolicy: IfNotPresent
          name: ray-worker
          resources:
            limits:
            cpu: "1"
            memory: 1G
            requests:
            cpu: "1"
            memory: 1G
```

Note that a RayCluster will hold resource quotas while it exists. For optimal resource management, you should delete a RayCluster that is no longer in use.

### c. Limitations
- Limited Worker Groups: Because a Kueue workload can have a maximum of 8 PodSets, the maximum number of `spec.workerGroupSpecs` is 7
- In-Tree Autoscaling Disabled: Kueue manages resource allocation for the RayCluster; therefore, the cluster's internal autoscaling mechanisms need to be disabled

## Example RayCluster

The RayCluster looks like the following:

{{< include "examples/jobs/ray-cluster-sample.yaml" "yaml" >}}

You can submit a Ray Job using the [CLI](https://docs.ray.io/en/latest/cluster/running-applications/job-submission/quickstart.html) or log into the Ray Head and execute a job following this [example](https://ray-project.github.io/kuberay/deploy/helm-cluster/#end-to-end-example) with kind cluster. 