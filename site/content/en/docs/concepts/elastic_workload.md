---
title: "Elastic Workloads"
date: 2025-04-16
weight: 4
description: >
  Workload types that support dynamic scaling.
---

{{< feature-state state="alpha" for_version="v0.13" >}}

## Elastic Workloads (Workload Slices)

Elastic Workloads extend the core `Workload` abstraction in Kueue to support **dynamic scaling** of admitted jobs, without requiring suspension or requeueing.
This is achieved through the use of **Workload Slices**, which track partial allocations of a parent job's scale-up and scale-down operations.

This feature enables more responsive and efficient scheduling for jobs that can adapt to changing cluster capacity, particularly in environments with fluctuating workloads or constrained resources.

## Dynamic Scaling

Traditionally, a `Workload` in Kueue represents a single atomic unit of admission.
Once admitted, it reflects a fixed set of pod replicas and consumes a defined amount of quota. If a job needs to scale up or down, the `Workload` must be suspended, removed, or replaced entirely.

While scaling **down** a workload is relatively straightforward and does not require additional capacity or a new workload slice, scaling **up** is more involved. It requires *additional capacity* that must be explicitly requested and *admitted* by Kueue through a new `Workload Slice`.

**Note:** While scaling **down** is conceptually similar to [Dynamic Reclaim](https://kueue.sigs.k8s.io/docs/concepts/workload/#dynamic-reclaim), it is an orthogonal concept, it neither intersects with nor depends on Dynamic Reclaim functionality.

## Use Cases

* Dynamically adjusting throughput for [embarrassingly parallel](https://en.wikipedia.org/wiki/Embarrassingly_parallel) jobs.
* Using AI/ML frameworks which support elasticity (Distributed Torch Elastic).

## Lifecycle

1. **Initial Admission**: A job is submitted and its first `Workload` is created and admitted.
2. **Scaling Up**: If the job requests more parallelism, a new slice is created with the **increased** replicas count. Once admitted, the new slice replaces the original workload by marking the old one as `Finished`.
3. **Scaling Down**: If the job reduces its parallelism, the updated pod count is recorded directly into the existing workload.
4. **Preemption**: Follows the existing workload preemption mechanism.
5. **Completion**: Follows the existing workload completion behavior.

## Example

{{< include "examples/jobs/sample-scalable-job.yaml" "yaml" >}}

The example above will result in an admitted workload and 3 running pods.
The parallelism can be adjusted (increased or decreased) as long as the job remains in an "Active" state (i.e., not yet completed).

## Feature Gate

Elastic Workloads via Workload Slices are gated by the following feature flag:

```yaml
ElasticJobsViaWorkloadSlices: true
```

Additionally, Elastic Job behavior must be explicitly enabled on a per-job basis via annotation:

```yaml
metadata:
  annotations:
    kueue.x-k8s.io/elastic-job: "true"
```

## Limitations

* Currently available only for `batch/v1.Job` workloads.
* Elastic workloads are not supported for jobs with partial admission enabled.

    * Attempting to scale jobs with partial admission enabled will result in an admission validation error similar to the following:

      ```text
      Error from server (Forbidden): error when applying patch:
      error when patching "job.yaml": admission webhook "vjob.kb.io" denied the request: spec.parallelism: Forbidden: cannot change when partial admission is enabled and the job is not suspended
      ```
* When scaling up a previously admitted job the new workload must reuse the originally assigned flavor, even if other eligible flavors have available capacity.
* No Multikueue support.
* No Topology-Aware Scheduling (TAS) support. 