---
title: "Share Quotas Across Resource Flavors"
linkTitle: "Share Quotas"
date: 2026-01-28
weight: 3
description: >
  Use resource transformations to enforce a shared quota across multiple resource flavors
---

This page demonstrates how to use [resource transformations](/docs/tasks/manage/administer_cluster_quotas/#transform-resources-for-quota-management) to enforce a unified quota that applies across multiple [resource flavors](/docs/concepts/cluster_queue#resourceflavor-object).

A common use case is when you have multiple hardware types (for example, different CPU
architectures or GPU models) but want to limit the total usage per team regardless of
which flavor they consume. By defining a virtual "credits" resource, you can assign
a cost to each flavor and enforce a combined quota.

The intended audience for this page are [batch administrators](/docs/tasks#batch-administrator).

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- The kubectl command-line tool has communication with your cluster.
- [Kueue is installed](/docs/installation).

## Use case: limit total CPU usage across flavors

Assume your cluster has two CPU flavors (for example, `on-demand` and `spot` nodes) and
you want to limit the total CPU usage per team to 14 CPUs, regardless of which flavor
they use.

You can accomplish this by:

1. Defining a virtual resource called `cpu_credits`
2. Configuring a resource transformation that generates 1 `cpu_credit` per CPU requested
3. Creating a separate ResourceGroup in the ClusterQueue that limits `cpu_credits`

### 1. Configure the resource transformation

Follow the [installation instructions for using a custom configuration](/docs/installation#install-a-custom-configured-released-version) and extend the Kueue configuration:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
resources:
  transformations:
  - input: cpu
    strategy: Retain
    outputs:
      cpu_credits: 1
```

This configuration tells Kueue to generate 1 `cpu_credit` for every CPU requested, while
retaining the original `cpu` resource for scheduling purposes.

### 2. Create the queuing infrastructure

Create the ResourceFlavors, ClusterQueue, and LocalQueue. You can apply the complete
setup at once:

```shell
kubectl apply -f https://kueue.sigs.k8s.io/examples/admin/shared-quota-setup.yaml
```

Or create the resources individually:

{{< include "examples/admin/shared-quota-setup.yaml" "yaml" >}}

With this configuration:

- Jobs can use up to 9 CPUs from `on-demand` nodes
- Jobs can use up to 9 CPUs from `spot` nodes
- The total combined CPU usage is limited to 14 CPUs (enforced via `cpu_credits`)

## Example workload

Here is an example Job that requests 3 CPUs:

{{< include "examples/admin/shared-quota-sample-job.yaml" "yaml" >}}

You can create the Job using the following command:

```shell
kubectl create -f https://kueue.sigs.k8s.io/examples/admin/shared-quota-sample-job.yaml
```

When this Job is submitted, the Workload will have the following resource requests:

```yaml
resourceRequests:
- name: main
  resources:
    cpu: 3
    cpu_credits: 3
```

## Observing the quota enforcement

After creating several jobs, you can observe the quota enforcement in action:

```bash
kubectl get workloads
```

Example output when the `cpu_credits` quota is exhausted:

```
NAME                        QUEUE        RESERVED IN           ADMITTED   AGE
job-sample-job-abc12-xyz   team-queue   team-cluster-queue    True       4s
job-sample-job-def34-uvw   team-queue   team-cluster-queue    True       3s
job-sample-job-ghi56-rst   team-queue   team-cluster-queue    True       3s
job-sample-job-jkl78-opq   team-queue   team-cluster-queue    True       2s
job-sample-job-mno90-lmn   team-queue                                    2s
```

The last job is pending because admitting it would exceed the 14 `cpu_credits` quota.
You can verify this by describing the workload:

```bash
kubectl describe workload job-sample-job-mno90-lmn
```

The events will show:

```
Events:
  Type     Reason   Age   From             Message
  ----     ------   ----  ----             -------
  Warning  Pending  10s   kueue-admission  couldn't assign flavors to pod set main: insufficient unused quota for cpu_credits in flavor credits, 1 more needed
```

## Extending this pattern

You can extend this pattern to:

### Assign different costs to different flavors

Configure transformations that generate different amounts of credits based on the
input resource. For example, you can assign different costs to different GPU types
to reflect their relative value or pricing:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
resources:
  transformations:
  - input: nvidia.com/gpu-a100
    strategy: Replace
    outputs:
      gpu_credits: 100
  - input: nvidia.com/gpu-v100
    strategy: Replace
    outputs:
      gpu_credits: 40
  - input: nvidia.com/gpu-t4
    strategy: Replace
    outputs:
      gpu_credits: 10
```

With a ClusterQueue configured to limit `gpu_credits`:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: "team-cluster-queue"
spec:
  namespaceSelector: {}
  resourceGroups:
  - coveredResources: ["nvidia.com/gpu-a100"]
    flavors:
    - name: "a100"
      resources:
      - name: "nvidia.com/gpu-a100"
        nominalQuota: 8
  - coveredResources: ["nvidia.com/gpu-v100"]
    flavors:
    - name: "v100"
      resources:
      - name: "nvidia.com/gpu-v100"
        nominalQuota: 16
  - coveredResources: ["nvidia.com/gpu-t4"]
    flavors:
    - name: "t4"
      resources:
      - name: "nvidia.com/gpu-t4"
        nominalQuota: 32
  - coveredResources: ["gpu_credits"]
    flavors:
    - name: "credits"
      resources:
      - name: gpu_credits
        nominalQuota: 500
```

This setup allows teams to use any combination of GPU types while staying within
their total "budget" of 500 credits. A team could use 5 A100s (500 credits), or
12 V100s (480 credits), or a mix of different types.

### Track costs across resource types

Generate credits from multiple input resources (CPU, memory, GPU) to enforce a
combined budget.

### Implement monetary budgets

Use credits to approximate the cost of cloud resources and enforce spending limits
per team.

For more examples of resource transformations, see:

- [Transform resources for quota management](/docs/tasks/manage/administer_cluster_quotas/#transform-resources-for-quota-management)
- [Using HAMi vGPU](/docs/tasks/run/using_hami)
