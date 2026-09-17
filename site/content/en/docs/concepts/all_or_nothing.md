---
title: "All-or-nothing Scheduling"
date: 2026-07-03
weight: 8
description: >
  A summary of the mechanisms Kueue offers to approximate all-or-nothing (gang) scheduling.
---

Many batch workloads, such as synchronized distributed training, require all of their Pods to run at the same time to make progress. For such workloads, partial scheduling is wasteful at best: Pods that start hold onto resources while waiting for the rest, and two such jobs can deadlock by each holding a fraction of the resources the other needs.

Kueue implements all-or-nothing scheduling, frequently referred to as
[gang scheduling](https://en.wikipedia.org/wiki/Gang_scheduling), through multiple complementary mechanisms designed to guarantee full workload readiness upon admission.

## Quota-based admission: the first line of defense

The base mechanism are quotas themselves: by default,
a Workload is only unsuspended when the quota for *all* of its
[pod sets](/docs/concepts/workload#pod-sets) can be reserved at once. By default,
Kueue does not admit a fraction of a Workload (unless
[Partial Admission](/docs/concepts/workload#partial-admission) is explicitly
enabled to allow scaling down pod sets).

However, reserving aggregate quota alone does not guarantee that Pods can actually
schedule. Quota tracks total quantities of resources, whereas scheduling depends on
node layout, physical node availability, and node fragmentation.

## Topology Aware Scheduling: placement-checked admission

[Topology Aware Scheduling](/docs/concepts/topology_aware_scheduling/) (TAS)
makes Kueue track the free capacity of individual nodes and topology domains
(racks, blocks, etc.). Enabling TAS is **strongly recommended as a default** for
batch and ML workloads.

Without TAS, Kueue admits workloads purely based on aggregate cluster quota. If
your cluster has 8 GPUs free in total, but they are split across two nodes with
4 GPUs each, Kueue will admit a workload containing a single 8-GPU Pod based on
available aggregate quota. However, because a single Pod cannot be split across
nodes, it will get stuck indefinitely at the Kubernetes scheduler layer. This
scenario is highly unintuitive for users and difficult to debug.

When TAS is enabled, Kueue evaluates topology placement alongside quota availability
before committing admission. Kueue only admits the workload and reserves quota if all
of its Pods fit into the requested topology domains, assigning them to concrete
domains at admission time. This eliminates the main source of partial scheduling
(fragmentation) at admission time, and optimizes the placement for network locality.

## waitForPodsReady: timeout-based eviction

[`waitForPodsReady`](/docs/tasks/manage/setup_wait_for_pods_ready/) is a
cluster-wide, timeout-based implementation of all-or-nothing scheduling. Once
a Workload is admitted, Kueue monitors it until all of its Pods are ready. If
this doesn't happen within the configured timeout, the Workload is evicted,
its quota is released, and it is requeued with a configurable backoff.

Two settings are worth calling out:

- `blockAdmission`: when enabled, workloads are admitted one at a time and
  subsequent workloads wait until the previous workload's Pods are ready.
  This prevents two half-scheduled jobs from deadlocking each other.
- `recoveryTimeout`: bounds how long an already-running workload may wait for
  a replacement Pod (for example after a node failure) before being evicted.

This mechanism is a heuristic: it does not prevent partial scheduling, but it
guarantees that a partially scheduled workload does not hold resources
indefinitely.

## ProvisioningRequest: capacity-checked admission

In autoscaled clusters, the
[ProvisioningRequest admission check](/docs/concepts/admission_check/provisioning_request/)
delays the admission of a workload until the configured provisioning class reports
capacity for the request's configured [managedResources](/docs/concepts/admission_check/provisioning_request/#provisioningrequestconfig).
This integrates with Kubernetes Cluster Autoscaler's [ProvisioningRequest API](https://github.com/kubernetes/autoscaler/blob/master/cluster-autoscaler/proposals/provisioning-request.md).

The behavior depends on the provisioning class:
- `check-capacity.autoscaling.x-k8s.io` only verifies the existing capacity,
  failing if the resources are currently unavailable.
- `best-effort-atomic-scale-up.autoscaling.x-k8s.io` may request a scale-up of new
  nodes. However, because this is a "best effort" check, the actual atomicity of
  the scale-up depends on the underlying provider's capability to provision
  nodes as a single group. It does not necessarily guarantee strict all-or-nothing scale-up
  across all environments.

Additionally, cloud vendors and infrastructure providers may offer proprietary provisioning classes that expand on these capabilities, providing specialized
handling or advanced resource management tailored to their platforms.


## Choosing the Right Mechanism

The mechanisms are complementary and should be combined based on your cluster setup. We recommend evaluating and applying them in the following priority order:

1. **Baseline Quota Admission (Foundation):** Configure aggregate resource quotas for your cluster queues. Kueue's core admission engine evaluates all pod sets of a workload simultaneously, serving as the first line of defense against overcommitment.
2. **Enable Topology Aware Scheduling (TAS) by default (Primary Placement Control):** Strongly recommended for all multi-pod batch and ML workloads. Aggregate quota alone is blind to node layout; enabling TAS ensures Kueue validates physical node placement and topology domain fit before committing admission, eliminating job stalls caused by node fragmentation.
3. **Use Provisioning Request (for autoscaled clusters):** If your cluster uses dynamic capacity, combine quota and TAS with ProvisioningRequest (part of Kubernetes [Cluster Autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler/provisioningrequest)) so that admission waits for physical node scale-up or capacity confirmation before unsuspending workloads.
4. **Enable `waitForPodsReady` (Safety Net):** Configure `waitForPodsReady` across your cluster as a safety net. It automatically evicts and requeues admitted workloads if transient scheduling delays or node failures prevent Pods from reaching the `Ready` state within a configured timeout.
