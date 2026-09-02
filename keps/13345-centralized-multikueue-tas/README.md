# KEP-13345: Support centralized scheduling and quota management in MultiKueue

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1: Global Fair Sharing](#story-1-global-fair-sharing)
    - [Story 2: Cross-Cluster Preemption](#story-2-cross-cluster-preemption)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Drawbacks](#drawbacks)
- [Design Details](#design-details)
  - [Configuration](#configuration)
  - [Remote Inventory Sync](#remote-inventory-sync)
  - [Manager-Authoritative Placement](#manager-authoritative-placement)
  - [Worker Pass-Through](#worker-pass-through)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This KEP proposes an option to use the MultiKueue manager as a central scheduler
across all worker clusters, specifically leveraging Topology Aware Scheduling
(TAS). In this model, worker clusters act simply as compute pools. The central
manager handles all admission, preemption, and fair-sharing decisions, and all
project or team-specific quotas are managed entirely on the manager cluster.

This new centralized scheduling scheme is necessarily more computationally
expensive than MultiKueue's current mode which primarily manages global quota
and dispatch and relies on workers to make authoritative scheduling decisions.
Users will be able to select which mode a MultiKueue instance uses for their
scheduling and scaling requirements.

## Motivation

Currently, MultiKueue delegates final scheduling decisions to the worker
clusters. While this works well for many setups, it makes it difficult to
achieve strict global fair sharing and cross-cluster preemption across an entire
fleet because the manager lacks visibility into the actual physical capacity and
topology of the worker nodes.

Allowing the central manager to make these decisions helps organizations that
manage compute capacity across multiple regions or clouds to better optimize
their resources. It also simplifies cluster administration by consolidating team
quota management into a single control plane, rather than needing to manage and
sync quotas across every worker cluster.

### Goals

- Enable the MultiKueue manager to act as a central scheduler across all worker clusters.
- Support all workloads currently supported by MultiKueue.
- Ensure global preemption and fair sharing work seamlessly across the fleet.
- The manager computes the exact node-level placement (e.g., `kubernetes.io/hostname`) for workloads.
- Workers execute the manager's placement verbatim without re-evaluating TAS.

### Non-Goals

- Supporting workloads that span multiple clusters (cross-cluster gangs). A
  single workload must still fit entirely within one worker cluster.
- Supporting dynamic node provisioning (autoscaling) for TAS workloads in this iteration.
- Fundamentally change the existing MultiKueue design.

## Proposal

We propose extending MultiKueue to allow the manager to ingest physical node and
pod inventory from worker clusters into its own scheduler TAS cache. When a
workload is admitted, the manager will compute the full `TopologyAssignment`
(treating the worker cluster as the top-level topology domain) and project a
host-exact placement onto the remote workload. The worker cluster will then skip
the workload during its own scheduling cycle and apply the manager's placement directly.

This solution works because all workloads are already mirrored in the manager
via MultiKueue. The reason global features like fair-sharing and cross-cluster
preemption don't work correctly today is simply because the manager lacks
information about the physical capacity of the clusters and the specific
topology placement of the workloads. By centralizing the TAS cache across all
clusters, we give the manager the missing physical context it needs, bringing
all of these advanced scheduling features alive globally without having to
rewrite them.

Furthermore, this architectural shift eliminates the need for several complex workarounds currently required in MultiKueue:

- It removes the need for `MultiKueueOrchestratedPreemption` and its associated complexities.
- It eliminates the need to declare higher, inflated quotas in the manager compared to the workers.

### User Stories (Optional)

#### Story 1: Global Fair Sharing

As a platform administrator managing a fleet of GPU clusters across multiple
regions, I want to define team quotas centrally on the manager cluster. If Team
A is under quota and Team B is over quota, I want the manager to ensure Team A
can preempt Team B's workloads globally, regardless of which specific worker
cluster Team B's workloads happen to be running on.

#### Story 2: Cross-Cluster Preemption

As a machine learning engineer, I submit a high-priority distributed training
job that requires a specific network topology (e.g., all pods on the same rack).
If the fleet is full, I want the central manager to find the optimal worker
cluster and preempt exactly the right lower-priority workloads on that specific
cluster to make room for my job's topology requirements.

### Risks and Mitigations

**Risk:** Scalability and performance bottlenecks on the manager cluster.

**Mitigation:** The manager's TAS cache does not store full pod specs. It only
caches a highly optimized, pre-aggregated counter of the resources used by
non-TAS pods per node. This ensures the memory footprint and CPU overhead remain
minimal even at large scale.

**Risk:** Worker cluster disconnection.

**Mitigation:** The system relies on MultiKueue's existing `workerLostTimeout`
mechanism. If a worker disconnects, the manager waits for a grace period. If the
worker does not reconnect, the manager evicts the workload and retries
scheduling it on a healthy cluster. When a worker reconnects, the manager
performs a fresh `List` to rebuild the TAS cache for that cluster.

## Drawbacks

- **Scalability Limits**: Centralizing all node and pod state into a single
  manager introduces a fundamental scalability limit. While the TAS cache is
  optimized (storing only aggregated resource counters rather than full pod
  specs), the manager must still process a high volume of watch events from
  every worker cluster. However, this architecture should comfortably support
  the currently documented limits for TAS (100k nodes) and MultiKueue (20 worker
  clusters).
- **Distributed System Complexity**: The manager's view of worker capacity is
  eventually consistent. Network partitions, watch delays, or API server latency
  on worker clusters can cause the manager to make scheduling decisions based on
  stale physical capacity data, potentially leading to transient placement
  failures or race conditions.
- **Single Point of Failure**: While MultiKueue already relies on the manager
  for admission, centralizing TAS makes the manager a harder dependency for
  actual node-level placement. If the manager's TAS cache is degraded, no
  topology-aware workloads can be scheduled anywhere in the fleet.

## Design Details

### Configuration

This KEP proposes adding a new mode for MultiKueue and preserving its current
strategy as the default. The new "centralized" mode necessarily changes the
scalability profile of the Kueue manager by requiring more data which comes from
other clusters.

To enable centralized TAS, users must first enable the
`MultiKueueCentralizedTAS` feature gate in the Kueue manager on the manager
cluster and each worker cluster. The feature gate allows a new
`multikueue.centralizedTAS` field to be set in the Kueue Configuration API:

```go
type MultiKueue struct {
	...

	// CentralizedTAS configures MultiKueue's "centralized" scheduling scheme.
	// This field is ignored when the MultiKueueCentralizedTAS feature gate is
	// disabled.
	// +optional
	CentralizedTAS *CentralizedTAS `json:"centralizedTAS,omitempty"`
}

// CentralizedTAS configures MultiKueue's "centralized" scheduling scheme.
type CentralizedTAS struct {
	// Enable controls whether a MultiKueue manager watches the required
	// resources in each worker cluster. It must be true to allow a ClusterQueue
	// to be configured to use centralized TAS.
	// +optional
	Enable *bool `json:"enable,omitempty"`
}
```

The Configuration API changes are intended to allow this functionality to be
disabled even if the feature gate is graduated to stable and removed.

When the Configuration's `multikueue.centralizedTAS.enable` is `true`, a
ClusterQueue can be configured to use centralized TAS with the
`kueue.x-k8s.io/multikueue-centralized-tas="true"` annotation in addition to a
MultiKueue AdmissionCheck. ClusterQueues may be created with a MultiKueue
AdmissionCheck without the annotation to use the existing MultiKueue scheduling
behavior.

In short, to enable centralized TAS for a Workload:

1. Enable the `MultiKueueCentralizedTAS` feature gate in the Kueue managers for
   the manager and worker clusters.
1. Set the manager's Configuration object to include
   `multikueue.centralizedTAS.enable=true`.
1. Annotate the appropriate ClusterQueues with
   `kueue.x-k8s.io/multikueue-centralized-tas="true"`.

### Remote Inventory Sync

The manager will watch Node and Pod objects from all connected worker
clusters. These events are fed directly into the manager's scheduler TAS cache,
which will be extended to index Nodes by cluster name in addition to their own
name.

To differentiate nodes across clusters, a cluster label (e.g.,
`kueue.x-k8s.io/multikueue-cluster`) is injected into the remote Node objects as
they are synced into the cache. This label serves as the top-level topology
domain for the manager.

The manager reuses the exact same `utiltas.BelongsToNonTASCache` logic as the
single-cluster controller to track non-TAS usage on the remote nodes.

### Manager-Authoritative Placement

When scheduling a workload, the manager computes the `TopologyAssignment` using
the worker cluster as the top level and hostname as the lowest level.

Before stamping the admission onto the remote workload, the manager projects
this assignment: it strips the cluster level and collapses the assignment to a
host-exact placement (`Levels: []string{corev1.LabelHostname}`).

### Worker Pass-Through

Workers are configured with a single, massive quota per resource flavor
(effectively acting as infinite compute pools from a quota perspective).

When the worker receives the remote workload with the manager-stamped admission,
it recognizes that the workload is already admitted. The worker skips the workload during its own
scheduling cycle, and the TAS ungater translates the workload's host-exact
`TopologyAssignment` into standard `nodeSelectors` on the pods.

### Test Plan

- **Unit tests**: Cover the topology projection logic
  (`projectAdmissionForWorker`) and the remote inventory cache sync logic.
- **e2e tests**: Implement a comprehensive e2e test suite covering:
  - Cross-cluster topology-correct preemption.
  - Global fair sharing across worker clusters.
  - Physical capacity enforcement (ensuring the manager blocks admission if the
    fleet is physically full, even if central quota is available).

### Graduation Criteria

TBD

## Alternatives

**Distributed TAS**: Let workers compute TAS and report back to the manager.

*Drawback*: This cannot guarantee strict global fair sharing or accurate
cross-cluster preemption without implementing complex two-phase commit protocols
between the manager and workers. The manager would have to blindly guess which
worker might be able to fit the topology, leading to high latency and scheduling
churn.

**Aggregated TAS Capacity**: Management cluster calculates `TopologyAssignment`s
based on aggregated capacity advertised by each worker cluster organized by
resource and topological domain instead of watching all Nodes and Pods across
all worker clusters.

*Drawback*: Requires a new API resource definition, more likely to provide
outdated information at less-than-extraordinary scale and load, overlaps less
with how utilization is calculated in single-cluster scenarios.
