---
title: "Using HAMi"
linkTitle: "HAMi vGPU"
date: 2025-12-15
weight: 6
description: >
  Using HAMi vGPU resources with Kueue
---

This page demonstrates how to use Kueue for running workloads with [HAMi](https://github.com/Project-HAMi/HAMi) (Heterogeneous AI Computing Virtualization Middleware) for vGPU resource management.

When working with vGPU resources through HAMi, Pods request per-vGPU resources (`nvidia.com/gpumem`, `nvidia.com/gpucores`) along with the number of vGPUs (`nvidia.com/gpu`). For quota management, you need to track the total resource consumption across all vGPU instances. 

Below we demonstrate how to support this with the [resource transformation for quota management](/docs/tasks/manage/administer_cluster_quotas/#transform-resources-for-quota-management) in Kueue.

This guide is for [batch users](/docs/tasks#batch-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/docs/overview).

## Before you begin

1. The `Deployment` integration is enabled by default.

2. Follow the [installation instructions for using a custom configuration](/docs/installation#install-a-custom-configured-released-version) to configure Kueue with ResourceTransformation.

3. Ensure your cluster has HAMi installed and vGPU resources available. HAMi provides vGPU resource management through resources like `nvidia.com/gpu`, `nvidia.com/gpucores`, and `nvidia.com/gpumem`.

## Using HAMi with Kueue

When running Pods that request vGPU resources on Kueue, take into consideration the following aspects:

### a. Configure ResourceTransformation

Configure Kueue with ResourceTransformation to automatically calculate total vGPU resources:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
resources:
  transformations:
  - input: nvidia.com/gpucores
    strategy: Replace
    multiplyBy: nvidia.com/gpu
    outputs:
      nvidia.com/total-gpucores: "1"
  - input: nvidia.com/gpumem
    strategy: Replace
    multiplyBy: nvidia.com/gpu
    outputs:
      nvidia.com/total-gpumem: "1"
```

This configuration tells Kueue to multiply per-vGPU resource values by the number of vGPU instances. For example, if a Pod requests `nvidia.com/gpu: 2` and `nvidia.com/gpumem: 1024`, Kueue will calculate `nvidia.com/total-gpumem: 2048` (1024 × 2).

### b. Configure ClusterQueue

Configure your ClusterQueue to track both vGPU instance counts and total resources:

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: vgpu-cluster-queue
spec:
  resourceGroups:
  - coveredResources: ["nvidia.com/gpu", "nvidia.com/total-gpucores", "nvidia.com/total-gpumem"]
    flavors:
    - name: default-flavor
      resources:
      - name: nvidia.com/gpu
        nominalQuota: 50
      - name: nvidia.com/total-gpucores
        nominalQuota: 1000
      - name: nvidia.com/total-gpumem
        nominalQuota: 10240
```

### c. Queue selection

The target [local queue](/docs/concepts/local_queue) should be specified in the `metadata.labels` section of the Pod configuration.

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: vgpu-queue
```

### d. Configure the resource needs

The resource needs of the workload can be configured in the `spec.containers` section:

```yaml
resources:
  limits:
    nvidia.com/gpu: "2"         # Number of vGPU instances
    nvidia.com/gpucores: "20"   # Cores per vGPU
    nvidia.com/gpumem: "1024"   # Memory per vGPU (MiB)
```

Kueue will automatically transform these into total resources:
- `nvidia.com/total-gpucores: 40` (20 cores × 2 vGPUs)
- `nvidia.com/total-gpumem: 2048` (1024 MiB × 2 vGPUs)

## Example

Here is a sample:

{{< include "examples/serving-workloads/sample-hami.yaml" "yaml" >}}

You can create the vGPU Deployment using the following command:

```sh
kubectl create -f https://kueue.sigs.k8s.io/examples/serving-workloads/sample-hami.yaml
```

To check the resource usage in the ClusterQueue, inspect the `status.flavorsReservation` field:

```bash
kubectl get clusterqueue hami-queue -o yaml
```

The `status.flavorsReservation` shows the current resource consumption for `nvidia.com/total-gpucores` and `nvidia.com/total-gpumem`:

```yaml
status:
  flavorsReservation:
  - name: hami-flavor
    resources:
    - name: nvidia.com/total-gpucores
      total: "60"  # Current usage (30 cores × 2 vGPUs)
    - name: nvidia.com/total-gpumem
      total: "2048"  # Current usage (1024 MiB × 2 vGPUs)
```