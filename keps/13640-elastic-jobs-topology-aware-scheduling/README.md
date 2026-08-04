# KEP-13640: Support required/preferred Topology-Aware Scheduling (TAS) for Elastic Jobs

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
- [Design Details](#design-details)
  - [Example: Preferred Topology](#example-preferred-topology)
  - [Example: Required Topology](#example-required-topology)
  - [Algorithmic Technical Details](#algorithmic-technical-details)
    - [Fast-Path Scale-Up (Preferred)](#fast-path-scale-up-preferred)
    - [Node-Hot-Swap Iterative Scale-Up (Required)](#node-hot-swap-iterative-scale-up-required)
    - [Scale-Down Operations](#scale-down-operations)
<!-- /toc -->

## Summary

This KEP proposes extending the existing Elastic Jobs integration with Topology Aware Scheduling (TAS) to support both **preferred** and **required** topology modes. 

Previously, when scaling elastic jobs (e.g., dynamically sized jobs), only the `unconstrained` topology mode was supported. Any job specifying `required` or `preferred` topology annotations alongside the `kueue.x-k8s.io/elastic-job: "true"` label was rejected at creation via admission webhooks. This proposal outlines the algorithmic design for incrementally packing scaled pods into optimal topology domains.

## Motivation

AI and ML training workloads frequently rely on high-bandwidth communication between pods (e.g., NCCL all-reduce). Topology Aware Scheduling guarantees that pods are co-located within specific physical network boundaries (e.g., racks, blocks). When dealing with Elastic Jobs that scale up and down dynamically based on available cluster capacity, it is critical that newly scaled pods are integrated into the topology constraints smoothly, preventing straggler pods from slowing down the entire distributed training process.

## Proposal

### User Stories

- **Story 1 (Required)**: As an AI researcher, my workload is running with 4 pods inside a single "rack". Cluster resources open up, and Kueue wants to scale my job up to 8 pods. The job has a `required` topology of "rack". The scale-up must strictly enforce that all 4 new pods fit into the *current* rack boundary. If the rack is full, the scale-up does not scatter pods outside the rack.
- **Story 2 (Preferred)**: As an AI researcher, my workload has a `preferred` topology of "rack". A scale-up of 4 pods is evaluated. The current rack only has room for 2 more pods. Because it is preferred, Kueue adds 2 pods to my current rack to maximize locality, and gracefully falls back to placing the remaining 2 pods in the next best rack.

## Design Details

Below are concrete examples of how users will configure Elastic Jobs with TAS, followed by the internal algorithmic mechanisms handling the scaling.

### Example: Preferred Topology

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: tas-elastic-preferred
  labels:
    kueue.x-k8s.io/queue-name: user-queue
    kueue.x-k8s.io/elastic-job: "true"
spec:
  parallelism: 4
  template:
    metadata:
      annotations:
        kueue.x-k8s.io/podset-preferred-topology: cloud.provider.com/topology-rack
    spec:
      containers:
      - name: worker
        image: mpioperator/mpi-pi:openmpi
```
If this job scales from 4 to 8, the algorithm attempts to pack all 8 pods into the existing rack. If the rack only holds 6, it will allocate 6 pods to the current rack and fallback to placing the remaining 2 in the next available rack.

### Example: Required Topology

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: tas-elastic-required
  labels:
    kueue.x-k8s.io/queue-name: user-queue
    kueue.x-k8s.io/elastic-job: "true"
spec:
  parallelism: 4
  template:
    metadata:
      annotations:
        kueue.x-k8s.io/podset-required-topology: cloud.provider.com/topology-rack
    spec:
      containers:
      - name: worker
        image: mpioperator/mpi-pi:openmpi
```
If this job scales from 4 to 8, it *must* find 4 slots within the exact same rack as the original 4 pods. If the rack cannot accommodate them, the scale-up is capped or rejected.

### Algorithmic Technical Details

#### Fast-Path Scale-Up (Preferred)
Preferred topology during elastic scale-up leverages a "Fast-Path" placement strategy utilizing standard TAS logic (e.g., `LeastFreeCapacity` or `BestFit`):
1. The scheduler extracts the `TopologyAssignment` from the active slice.
2. It attempts to satisfy the full capacity (`currCount + deltaCount`) starting within the preferred domain footprint.
3. If the preferred domain lacks sufficient capacity, the algorithm naturally falls back to satisfying the placement in the next best available domains. The placement is fragmented across domains optimally based on the scoring algorithm.

#### Node-Hot-Swap Iterative Scale-Up (Required)
Placing all `deltaCount` pods at once via standard logic in Required mode could result in a fragmented placement that spans multiple topology domains unacceptably. 

To resolve this, we use an iterative **Node-Hot-Swap Algorithm**:
1. We analyze the current assignment footprint via a `findScaleUpDomain` helper function. This helper navigates the topology tree to identify the minimal bounding topology domain where the delta pods should be securely placed.
2. Rather than a bulk placement, the scaler iterates pod-by-pod.
3. For each `1..deltaCount` iteration, a single pod is mapped into the `currAssignment` strictly within the identified required boundary.
4. If at any point during the loop the required domain fills up, the scale-up placement terminates early. This guarantees we incrementally build the assignment without ever violating the required boundary.
5. The `currAssignment` is passed as the final TopologyAssignment for the new workload slice.

#### Scale-Down Operations
During scale-down operations, the workload slice simply retains the existing topology assignment for the remaining pods. Pods are removed from the assignment proportionately, ensuring that the tightest possible topology is preserved for the reduced workload size. If nodes are detected as unhealthy during scale-down, the operation respects minimum replica counts and maintains locality constraints on the healthy nodes.
