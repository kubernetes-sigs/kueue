# KEP-NNNN: Your short, descriptive title

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
  - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
    - [Integration support](#integration-support)
      - [JobSet](#jobset)
    - [Support for the &quot;auto&quot; mode](#support-for-the-auto-mode)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [Non-exclusive use of nodes](#non-exclusive-use-of-nodes)
    - [Node topology changes](#node-topology-changes)
    - [Race condition when accounting for DaemonSet pods](#race-condition-when-accounting-for-daemonset-pods)
- [Design Details](#design-details)
  - [Hierarchy representation](#hierarchy-representation)
  - [Admin-facing API](#admin-facing-api)
  - [User-facing API](#user-facing-api)
  - [Validation](#validation)
  - [Internal APIs](#internal-apis)
  - [Computing the assignment](#computing-the-assignment)
  - [Enforcing the assignment](#enforcing-the-assignment)
  - [Support for ReplicatedJobs in JobSet](#support-for-replicatedjobs-in-jobset)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha (MVP):](#alpha-mvp)
    - [Beta](#beta)
    - [Stable](#stable)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Account usage by watching DaemonSet pods](#account-usage-by-watching-daemonset-pods)
  - [Use label for workload](#use-label-for-workload)
<!-- /toc -->

## Summary

This KEP introduces a mechanism, called Topology Aware Scheduling (TAS), to
facilitate Job scheduling in Kueue by leveraging the information about the
hierarchical organization of a datacenter.

First, we start by observing that data centers have organizational units (like
racks and blocks). VMs running within the same organizational unit have better
network bandwidth than VMs on different units. Second, the units form
a hierarchical structure - there are multiple nodes within a rack, and there are
multiple racks within a block. We say that nods placed in different racks are
more distant than nodes placed within the same rack. Similarly, nodes placed in
different blocks are more distant than two nodes within the same block.

Based on these observations we propose a convention to expose the information
about the node placement in a datacenter hierarchy using node labels.

We also propose a set of APIs for Kueue administrators and users to utilize this
information in order to optimize the network throughput between the pods.

## Motivation

It is common that AI / ML workloads require a significant amount of pod-to-pod
communication to make progress. Thus, it is important, for runtime and overall
cost, to make sure there is a high throughput network connection between the
running pods.

For example, some workloads may run twice slower if there is a pod placed on
a node which belongs to a different block. However, currently the end user of
Kueue has no way to "require" that all pods of its workload run within the same
block.

### Goals

- allow a user to express that a workload can schedule only if all of its pods
  can be placed on nodes which are close together (within the same level of
  hierarchy)
- allow a user to express that a workload prefers to schedule all of its pods
  within the same level of hierarchy, but automatically relax the constraint if
  not possible.

### Non-Goals

- support MultiKueue at the management cluster
- support Cluster Autoscaler (ProvisioningRequest in particular)

The above features might be pursued in the future in follow up KEPs.

## Proposal

* Introduce convention to represent the topological hierarchy as node labels
* Provide API to configure the list of node labels representing levels in the
  hierarchy per Resource Flavor
* Introduce a set of Job annotations which allow to describe requirements for
  TAS

### User Stories

#### Story 1

As an ML researcher I run AI training workloads which require exchanging huge
amounts of data between pods. The workloads take twice longer and are twice
more expensive when there are pods running in different datacenter racks. I
don't want to start my workload if the pods cannot be placed within the same
rack.

#### Story 2

As an ML researcher I run AI training workloads which require communicating
moderate amounts of data between pods. I would like to optimize the runtime of
the workloads, by placing the pods as close to another as possible, ideally
within a single rack, but I'm ok running the workload on nodes scattered across
datacenter if placing all pods within the same hierarchy rack is not possible at
the given time.

### Notes/Constraints/Caveats (Optional)

#### Integration support

In the alpha iteration we aim to support Job & JobSet. If other integrations
follow naturally we may as well support them in alpha.

##### JobSet

One complication we noticed for JobSet is that the proposed design assumes
injecting the dedicated scheduling gate, called `kueue.x-k8s.io/topology` into
the PodTemplate. However, currently JobSet's `spec.replicatedJob` field is
immutable, even if the JobSet is suspended. This has been relaxed in JobSet 0.6
(see [PR](https://github.com/kubernetes-sigs/jobset/pull/623)).

#### Support for the "auto" mode

We are considering introducing the "auto" mode where the user does not need to
specify the level by node label value, but the lowest level is selected
automatically.

One approach is to accept "auto" as a specific value for the "prefer" and
"require" labels. However, we are going to defer this for Beta or GA based on
the users' feedback.

### Risks and Mitigations

#### Non-exclusive use of nodes

The TAS feature assumes exclusive use of the nodes, that is all pods running on
the nodes come from workloads assigned to the same TAS Resource Flavor. This can
be achieved by admins by using a distinct label corresponding to a node group
meant for TAS.

In order to reduce the risk of such misconfiguration we require every TAS
Resource Flavor to have at least one label.

#### Node topology changes

A node can be removed during runtime of a workload, and the replacement for the
pod running on this node may not be able to find a replacement matching the
TopologyAssignment. In that case the workload may not be able to progress.

First, note that the issue also exists for regular workloads, but for TAS
workloads, given the additional constraints, it might be harder to find a
replacement node.

In order to mitigate this risk we propose to extend the waitForPodsReady
mechanism with a new timeout, called replacement timeout, which defines the
timeout for all pods to be ready again. The time is computed since the last
transition to the PodsReady=false, more details in the KEP PR. This mechanism
will also be helpful for regular workloads.

A more involving approach would be to recompute the TopologyAdmission, however,
until now we don't modify the workload's admission while the workload is
scheduled, so it would require extra investigation and effort. We will consider
this before graduation to GA based on investigation if feasible and feedback
from users.

#### Race condition when accounting for DaemonSet pods

There is a risk that workloads are scheduled before the DaemonSet pods are
accounted by TAS.

We consider this risk as not that relevant for alpha as the TAS workloads are
expected to mostly consume accelerators (like TPUs and GPUs), so they are not
expected to compete for resources with DaemonSet pods.

One way to mitigate the risk is to implement tracking for DaemonSets
(see [Account usage by watching DaemonSet pods](#account-usage-by-watching-daemonset-pods)).
We will re-evaluate the need for Beta or GA based on the users' feedback.

## Design Details

### Hierarchy representation

We propose the model for representing the hierarchy of nodes within a datacenter
by using node labels. We assume the node labels are set up by a cloud provider,
or set up manually by administrators of on-premise clusters.

Additionally, we assume that every node used for TAS has a set of the labels
which identifies uniquely its location in the tree structure. We do not assume
global uniqueness of labels on each level, i.e. there could be two nodes with
the same "rack" label, but in different "blocks".

For example, this is a representation of the dataset hierarchy;

|  node  |  cloud.provider.com/topology-block | cloud.provider.com/topology-rack |
|:------:|:----------------------------------:|:--------------------------------:|
| node-1 |               block-1              |              rack-1              |
| node-2 |               block-1              |              rack-2              |
| node-3 |               block-2              |              rack-1              |
| node-4 |               block-2              |              rack-3              |

Note that, there is a pair of nodes, node-1 and node-3, with the same value of
the "cloud.provider.com/topology-rack" label, but in different blocks.

### Admin-facing API

```golang
// ResourceFlavorSpec defines the desired state of the ResourceFlavor
type ResourceFlavorSpec struct {
    ...

  // TopologyName indicates the name of the topology for the ResourceFlavor.
  // When specified, it enables scraping of the topology information from the
  // nodes matching to the Resource Flavor node labels.
  TopologyName *string
}

// TopologySpec defines the desired state of Topology
type TopologySpec struct {
	// Levels defines the levels of topology.
	Levels []TopologyLevel
}

// TopologyLevel defines the desired state of TopologyLevel
type TopologyLevel struct {
	// NodeLabel indicates the name of the node label for a specific topology
	// level. Examples:
  // - cloud.provider.com/topology-block
  // - cloud.provider.com/topology-rack
	NodeLabel string
}
```

Example TAS Resource Flavor & Topology configuration:

```yaml
kind: ResourceFlavor
metadata:
  name: "tas-flavor"
spec:
  nodeLabels:
    "cloud.provider.com/node-group: tas"
  topologyName: default
—k
kind: Topology
metadata:
  name: "default"
spec:
  levels:
  - nodeLabel: cloud.provider.com/topology-block
  - nodeLabel: cloud.provider.com/topology-rack
```

### User-facing API

The user will need to point the workload to the ClusterQueue with the TAS
ResourceFlavor, and add one of the annotations:

```golang
const (

  // This annotation indicates that a workload requires Topology Aware Scheduling,
  // and running all pods on nodes closely connected within the same level of
  // hierarchy is a strong requirement for scheduling the workload.
  RequireTopologyAnnotation = "kueue.x-k8s.io/require-topology"

  // This annotation indicates that a workload requires Topology Aware Scheduling,
  // but running all pods without the same topology level is a preference rather
  // than requirement. There is a distinguished value "auto" which means that
  // the lowest hierarchy level should be used.
  PreferTopologyAnnotation = "kueue.x-k8s.io/prefer-topology"
)
```

### Validation

We introduce the following validations:
- the value of `kueue.x-k8s.io/require-topology` is one of the labels specified
  in the `LevelLabels` structure
- the value of `kueue.x-k8s.io/prefer-topology` is one of the labels specified
  in the `LevelLabels` structure, or "auto"
- the ResourceFlavor defining`TopologyAwareScheduling` needs to have at least
  one node label

### Internal APIs

We extend the `Workload` structure to reflect the topology request at the
Job level.

```golang
type WorkloadSpec struct {
  ...
  // TopologyRequest defines the topology requested for the corresponding Job.
  TopologyRequest *TopologyRequest
}

type TopologyRequest struct {
  // Policy defines the policy used for TAS. Possible values are:
  // - Prefer set when `kueue.x-k8s.io/prefer-topology` annotation is set on the Job
  // - Require set when `kueue.x-k8s.io/require-topology` annotation is set on the Job
  Policy TopologyRequestPolicy

  // Level indicated by the `kueue.x-k8s.io/prefer-topology` or `kueue.x-k8s.io/require-topology `
  // annotation
  Level string
}
```

We extend the `PodSetAssignment` structure to keep track of the number of pods
at each topology level to the specific subset of nodes.

```golang
type PodSetAssignment struct {
  ...

  // TopologyAssignment indicates the resources assigned per topology level
  TopologyAssignment *TopologyAssignment
}

type TopologyAssignment struct {
     // Slices contains the list of assignments split into slices
     Slices []TopologyAssignmentSlice
}

type TopologyAssignmentSlice struct {
  // NodeLabels constitutes the nodeSelector for a given slice of pods. It
  // defines values for all labels configured in the Topology.Levels.
  NodeLabels map[string]string

  // Count indicates the number of pods in a given TopologyAssignmentSlice
  Count int
}
```

Kueue uses the `kueue.x-k8s.io/topology` scheduling gate to delay the
`nodeSelector` assignment, because different pods in the same PodSet may have
different values:

```golang
const (
  // TopologySchedulingGate is used to delay topology assignment for pods
  // once all the pods are created.
  TopologySchedulingGate = "kueue.x-k8s.io/topology"

  // WorkloadAnnotation indicates the name of the workload assigned.
  WorkloadAnnotation = "kueue.x-k8s.io/workload"

  // PodSetLabel indicates the name of the PodSet in the workload
  PodSeLabel = "kueue.x-k8s.io/podset"
)
```

### Computing the assignment

The extended pod assignment is set on admitting the workload. In order to
compute the assignment Kueue caches the information about the node allocatable
capacity and the current usage by other workloads in this resource flavor.

The information about the allocatable capacity is scraped from nodes based on
the `status.allocatable` field.

The cached information about the used resources is updated whenever a workload
is admitted, suspended, resumed or finished. Additionally, we scrape the usage
information from all non-TAS pods bound to the TAS nodes - this is done to
account for Pods not created by Workloads not managed by Kueue (such as
DaemonSet pods).

The available capacity for TAS nodes is then computed as a difference between
the allocatable space and current usage.

For a given PodSet Kueue:
- when the `require-topology` is used, then Kueue tries to find any value of the
 level label which can accommodate all the pods. If there is no such value, then
 the workload keeps waiting in the queue.
- when the `prefer-topology` is used, then Kueue tries to use heuristics to
  minimize the number of values of the label used. One approach is to greedily
  choose the value which can accommodate the most pods.

### Enforcing the assignment

When the workload has the PodSet assignments which are non-homogenous (all Pods
landing in the same topology) and is about to start we modify the corresponding
PodTemplates in the Job object to inject the `kueue.x-k8s.io/topology`
scheduling gate. For homogenous PodSet assignments it is enough to inject the
corresponding nodeSelector in the PodTemplates.

Then, there is a new component, called `TopologyUngater`, which is a Workload
reconciler which lists all pods for a given TAS PodSet, and ensures that the
pods in the expected number are un-gated to a given value.

Along with the scheduling gate, to each pod template the
`kueue.x-k8s.io/workload` and `kueue.x-k8s.io/podset` labels / annotations are
added to facilitate quick lookup (List request) for all pods corresponding to
the workload (and more specifically PodSetAssignment) by `TopologyAssigner`.

The TopologyUngater watches for pod events which trigger the reconciliation
loop. The reconciliations are batched by 1s periods to make sure multiple
created pods at the similar time don't trigger too many reconciliations.

In order to prevent ungating more pods as expected for a PodSet we consider to
use the expectations mechanism. The expectations are set for when we are about
to ungate a Pod. The expectation is fulfield if the Pod is observed as ungated
or the ungating request fails. We hold ungating if there are pending ungatings
within the PodSet.

### Support for ReplicatedJobs in JobSet

Currently, in the PodSet we just keep the number of Pods created for a given
PodTemplate. However, in case of JobSet the pods are naturally split between
instances of replicated Jobs.

Thus, without any extra work for JobSet, if JobSet has more than one
ReplicatedJob its Pods could thus be assigned by TAS to different topologies,
making the assignment suboptimal.

In order to improve accuracy of the support for ReplicatedJobs we do the
following API adjustments:

```golang
type PodSet struct {
  ...

  // ReplicatedJobCount indicates the number of replicated Jobs being span by
  // the PodSet. Each replicated Job is assumed to have the same number of Pods
  // with the same template.
  // Default: 1
  ReplicatedJobCount *int32

  // ReplicatedJobKeyLabel specifies the name of the label which indicates
  // the specific Job instance among ReplicatedJobs.
  ReplicatedJobKeyLabel *string
}
```

In case of JobSet `ReplicatedJobKeyLabel` could be either
`jobset.sigs.k8s.io/job-index` or `jobset.sigs.k8s.io/replicatedjob-name` as
both will uniquely identify a specific Job among the set of ReplicatedJob.

```golang
type PodSetAssignment struct {

  // ReplicatedJobKey indicates the value of the Pod label which is used to
  // specify a specific Job among a set of ReplicatedJobs.
  ReplicatedJobKey *string
}
```
Then, when the compute the assignments per pods sets corresponding to
ReplicatedJobs, rather than the entire PodSet. Finally, the `PodSetUngater`
ungates pods per replicatedJob assignment.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

##### Prerequisite testing updates

#### Unit Tests

We add integration tests for the following scenarios:
- a new node is added making workload admission possible
- the PodSet assignment is computed successfully for `require-topology`
- the PodSet assignment cannot be computed for `require-topology`
- the PodSet assignment is computed successfully for `prefer-topology` across
 multiple values
- the PodSet assignment cannot be computed for `prefer-topology`
- the schedulingGate is added to the pod template

#### Integration tests

We are going to add the integration tests to make sure the implementation is
well covered. In particular, the following scenarios need coverage:
- adding new nodes
- PodSet assignment for `prefer-topology` and `require-topology`
- Job's pod template has the "topology" scheduling gate injected
- un-gate pods by with the "topology" scheduling gate
- the Workload can be suspended and unsuspended

### Graduation Criteria

#### Alpha (MVP):

- support for all built-in integrations: Job and JobSet
- support single-level hierarchy
- support TAS with minimal cross to other features (no cohorts, no preemption,
  no reclaimable pods)

The new validations which are for MVP, but likely will be relaxed in the future:
- ClusterQueue is marked inactive if it contains a TAS ResourceFlavor and
  belongs to a cohort
- ClusterQueue is marked inactive if it contains a TAS ResourceFlavor and
  enables preemptions
- ClusterQueue is marked inactive if it contains a TAS ResourceFlavor and uses
  MultiKueue admission check
- ClusterQueue is marked inactive if it contains a TAS ResourceFlavor and uses
  ProvisioningRequest admission check

#### Beta

- support for all other integrations
- support multi-level hierarchy
- support replacement timeout in WaitForPodsReady
- re-evaluate accounting for used resources by watching DaemonSets
- re-evaluate the need for the "auto" mode which does not require a user to
  specify the hierarchy name, but just selects the lowest one
- support for ReplicatedJobs in JobSet

#### Stable

Consider the following improvements and implement if feasible:
- put pods with consecutive indexes of an IndexedJob on close nodes
- support the following features: reclaimable, pods, cohorts preemption
 within cluster queue
- support re-computing the TopologyAssignment while the workload
 is running
- perform full scheduling simulation rather than just capacity counting
 (including pod affinities and anti-affinities)

## Implementation History

- 2024-07-25 - PR merged [Allow mutating schedulingGates when the Jobset is suspended](Allow mutating schedulingGates when the Jobset is suspended)
- 2024-07-30 - this KEP


## Drawbacks

Tracking of nodes by Kueue will increase its code complexity and memory
consumption.

## Alternatives

### Account usage by watching DaemonSet pods

The idea is to account for DaemonSet pods by tracking the DaemonSets rather than
their Pods. This can prevent the [race condition](Race condition when accounting
for DaemonSet pods) risk.

**Reasons for discarding/deferring**

When accounting for the the DaemonSets we would need to verify their selectors
would allow match a node in question.

This is a complication which may not be necessary for MVP since the workloads
will mostly expected to consume accelerators (GPUs and TPUs).

We will consider this for Beta or GA based on the users' feedback.

### Use label for workload

We considered using the `kueue.x-k8s.io/workload` label rather than annotation.

**Reasons for discarding/deferring**

Currently workloads can have names which exceed the maximal length for a label.
So, we would need to shorten the maximal workload name by reducing the const
`maxPrefixLength` in `workload_names.go`. However, we can also facilitate
fast lookups based on labels if we index the workloads.
