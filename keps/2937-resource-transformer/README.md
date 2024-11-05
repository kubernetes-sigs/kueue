# KEP-2937: Configurable Resource Transformers

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
      - [Story 1A - Using Replace with MIG](#story-1a---using-replace-with-mig)
      - [Story 1B - Using Retain with MIG](#story-1b---using-retain-with-mig)
    - [Story 2](#story-2)
- [Design Details](#design-details)
  - [Specifying the Transformation](#specifying-the-transformation)
  - [Observability](#observability)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [ClusterQueue Scoped](#clusterqueue-scoped)
  - [ResourceFlavor Scoped](#resourceflavor-scoped)
  - [Workload Scoped](#workload-scoped)
<!-- /toc -->

## Summary

This KEP introduces a mechanism that enables cluster admins to customize
how the resource requests and limits of a Job are translated into
the resource requirements of its corresponding Workload. This enables
the admission and quota calculations done by ClusterQueues to be performed
on an abstracted and/or transformed set of resources without impacting
the actual resource requests/limits that the Pods created by the Workload
will present to the Kubernetes Scheduler when the Workload is admitted by
a ClusterQueue.

## Motivation

Kueue currently performs a direct and mostly non-configurable mapping of the resource
requests/limits of a Job into the resource requests of the Job's Workload.
The only supported configuration is the ability to ignore resources that match
a global `excludeResourcePrefixes` list.
This direct mapping results in two significant limitations that limit the expressivity
of Kueue's resource and quota mechanisms:
1. Only resources that are explicitly listed in a Jobs requests/limits can
be used in Kueue's quota calculations and admission decisions.
2. For a Job to be admissible by a ClusterQueue, the ClusterQueue must have
ResourceGroups whose CoveredResources include all non-excluded resources requested by the Job.

Injecting a more configurable transformation function into this mapping
will enable Kueue to support simpler and more powerful definitions of resource quotas.

### Goals

* Establish a mechanism that enables limited transformations on resources
  to be specified via external configuration.

### Non-Goals

* Supporting complex numerical transformations of resource quantities
* Supporting transformations that take multiple input resources
* Performing any mutation of the resource requests/limits of Jobs
* Ensuring that a Workload's resource requests is updated if the set of
configured transformations functions changes after its resource requests are computed.

## Proposal

* Add a configuration mechanism that enables a cluster admin to specify a `ResourceTransformation`.
* Extend `workload.NewInfo` to apply the resource mapping when computing the resources
needed to admit the workload immediately after it applies `dropExcludedResources`.
* Extend the `Status` field of Workload to make the transformed resources directly observable.

### User Stories (Optional)

In the user stories below, we use a 4-tuple (InputResourceName, OutputResourceName, OutputQuantity, Replace/Retain)
to express a resource transformation operation. In the actual implementation, this transformation would
be encoded as an entry in a `ResourceTransformation` which would be created by a cluster admin.

#### Story 1

This KEP will improve Kueue's ability to manage families of closely related
extended resources without creating overly complex ResourceGroups in its
ClusterQueues.  A motivating example is managing quotas for the various
MIG resources created by the NVIDIA GPU Operator using the mixed strategy
(see [MIG Mixed Strategy][mig]).
In the mixed strategy, instead of there being a single `nvidia.com/gpu` extended resource,
there is a proliferation of extended resource of the form `nvidia.com/mig-1g.5gb`, `nvidia.com/mig-2g.10gb`,
`nvidia.com/mig-4g.20gb`, and so on.  For quota management purposes, we would like to abstract
these into a single primary extended resource `example.com/accelerator-memory`.

##### Story 1A - Using Replace with MIG

If an operator such as [InstaSlice][instaslice] is deployed
that is capable of dynamically managing the MIG partitions available on the cluster,
the cluster admin may desire to completely elide the details of different MIG variants
from their quota management.

To do this, the cluster admin could register the following transformations:
* (`nvidia.com/mig-1g.5gb`, `example.com/accelerator-memory`, `5Gi`, `replace`)
* (`nvidia.com/mig-2g.10gb`, `example.com/accelerator-memory`, `10Gi`, `replace`)
* (`nvidia.com/mig-4g.20gb`, `example.com/accelerator-memory`, `20Gi`, `replace`)
* (`nvidia.com/mig-7g.40gb`, `example.com/accelerator-memory`, `40Gi`, `replace`)

In this scenario, the covered resources in a ClusterQueue would only need
to include `example.com/accelerator-memory`
and would not need to assign redundant and potentially confusing quotas to every MIG variant.

As a concrete example, consider a submitted Job requesting the resources below:
```yaml
  nvidia.com/mig-1g.5gb: 2
  nvidia.com/mig-2g.10gb: 1
  memory: 100M
  cpu: 1
```
this would yield a Workload with an effective request of:
```yaml
  example.com/accelerator-memory: 20G
  memory: 100M
  cpu: 1
```
and could be admitted to the `default-flavor` of a ClusterQueue with the ResourceGroup below:
```yaml
...
  - coveredResources: ["cpu", "memory", "example.com/accelerator-memory"]
    flavors:
    - name: default-flavor
      resources:
      - name: "cpu"
        nominalQuota: 8
      - name: "memory"
        nominalQuota: 500G
      - name: example.com/accelerator-memory
        nominalQuota: 80G
```

To compare, to achieve the identical effective quotas without `replace` the covered
resource stanza of each ClusterQueue would need to be:
```yaml
...
  - coveredResources: ["cpu", "memory", "example.com/accelerator-memory", "nvidia.com/mig-1g.5gb", "nvidia.com/mig-2g.10gb", "nvidia.com/mig-4g.20gb", "nvidia.com/mig-7g.40gb"]
    flavors:
    - name: default-flavor
      resources:
      - name: "cpu"
        nominalQuota: 8
      - name: "memory"
        nominalQuota: 500G
      - name: example.com/accelerator-memory
        nominalQuota: 200G
      - name: nvidia.com/mig-1g.5gb
        nominalQuota: 40  # 200G/5g
      - name: nvidia.com/mig-2g.10gb
        nominalQuota: 20  # 200G/10g
      - name: nvidia.com/mig-4g.20gb
        nominalQuota: 10  # 200G/20g
      - name: nvidia.com/mig-7g.40gb
        nominalQuota: 5  # 200G/40g
```

##### Story 1B - Using Retain with MIG

However, some cluster admins may desire to use `retain` for MIG resources to exercise
finer grained control over the use of MIG partitions.
To do this, the cluster admin could register the following transformations:
* (`nvidia.com/mig-1g.5gb`, `example.com/accelerator-memory`, `5Gi`, `retain`)
* (`nvidia.com/mig-2g.10gb`, `example.com/accelerator-memory`, `10Gi`, `retain`)
* (`nvidia.com/mig-4g.20gb`, `example.com/accelerator-memory`, `20Gi`, `retain`)
* (`nvidia.com/mig-7g.40gb`, `example.com/accelerator-memory`, `40Gi`, `retain`)

A Job requesting the resources below:
```yaml
  nvidia.com/mig-1g.5gb: 2
  nvidia.com/mig-2g.10gb: 1
  memory: 100M
  cpu: 1
```
would yield a Workload with an effective resource request of:
```yaml
  example.com/accelerator-memory: 20G
  nvidia.com/mig-1g.5gb: 2
  nvidia.com/mig-2g.10gb: 1
  memory: 100M
  cpu: 1
```

Below is an example ResourceGroup that the cluster admin could define that
prevents the usage of `7g.40gb` partitions by not listing them in the covered-resources
and puts a tighter quota on `4g.20gb` partitions than would be implied by the `accelerator-memory` quota:
```yaml
  - coveredResources: ["cpu", "memory", "example.com/accelerator-memory"]
    flavors:
    - name: default-flavor
      resources:
      - name: "cpu"
        nominalQuota: 8
      - name: "memory"
        nominalQuota: 500G
      - name: example.com/accelerator-memory
        nominalQuota: 200G
      - name: nvidia.com/mig-1g.5gb
        nominalQuota: 40  # 200G/5g
      - name: nvidia.com/mig-2g.10gb
        nominalQuota: 20  # 200G/10g
      - name: nvidia.com/mig-4g.20gb
        nominalQuota: 4  # less than the 10 that would be allowed by the accelerator-memory quota
        borrowingLimit: 0
```

[mig]: https://docs.nvidia.com/datacenter/cloud-native/kubernetes/latest/index.html#the-mixed-strategy
[instaslice]: https://github.com/openshift/instaslice-operator

#### Story 2

This KEP would allow Kueue to augment a Job's resource requests
for quota purposes with additional resources that are not actually
available in the Nodes of the cluster. For example, it could be used to
define quotas in terms of a fictional currency `example.com/credits`
to give "costs" to various resources. This could be used to approximate
the monetary cost of different cloud resources and thus apply a "budget" to a team.
We'd expect in this usage pattern that `retain` would be used so that the
transformed resource list is just augmented with the computed `credits` resource.
Most likely `credits` would be in a separate ResourceGroup than the "real" resources (similar to the
[example of a ClusterQueue with software licenses](https://kueue.sigs.k8s.io/docs/concepts/cluster_queue/#resource-groups))
and the `borrowingLimit` and `lendingLimit` for
`credits` would be set to `0` to enforce the intended fiscal constraint.

For example:
* (`foo.com/gpu`, `example.com/credits`, `10`, `retain`)
* (`cpu`, `example.com/credits`, `1`, `retain`)

```yaml
  - coveredResources: ["cpu", "memory", "foo.com/gpu"]
    flavors:
    - name: "spot"
      resources:
      - name: "cpu"
        nominalQuota: 10
      - name: "memory"
        nominalQuota: 36Gi
      - name: "foo.com/gpu"
        nominalQuota: 40
    - name: "on-demand"
      resources:
      - name: "cpu"
        nominalQuota: 18
      - name: "memory"
        nominalQuota: 72Gi
      - name: "foo.com/gpu"
        nominalQuota: 100
  - coveredResources: ["example.com/credits"]
    flavors:
    - name: "team1-budget"
      resources:
      - name: "example.com/credits"
        nominalQuota: 750
        borrowingLimit: 0
        lendingLimit: 0
```

With the single global `ResourceTransformation` proposed in this KEP, this use case may
be of limited applicability.  It would benefit from finer-grained
[alternatives](#alternatives) that would for example `cpu` allocated from
the `spot` flavor to cost fewer credits than when allocated from the `on-demand` flavor.

## Design Details

Since Kueue's workload.Info struct is already a well-defined
place where resource filtering is being applied to compute the effective resource
request from a Workload's PodSpec, the core of the implementation is
straightforward and follows from how `excludeResourcePrefixes` are already processed.

The non-trivial design issues are how to configure the transformations,
the possible granularity of that configuration, and how to maximize observability
of the effective resources requested by a Workload.

### Specifying the Transformation

In this KEP we propose to support a global set of resource transformations that would
be applied to all Workloads.  A `ResourceTransformation` instance would be added to Kueue's
`Configuration` API as part of the existing `Resources` struct.
More [flexible alternatives](#alternatives) were considered, but left as possible
future extensions.

The definitions below would be added to Kueue's `Configuration` types
```go
type Resources struct {
	// ExcludedResourcePrefixes defines which resources should be ignored by Kueue
	ExcludeResourcePrefixes []string `json:"excludeResourcePrefixes,omitempty"`

	// Transformations defines how to transform PodSpec resources into Workload resource requests.
	// +listType=map
	// +listMapKey=input
	Transformations []ResourceTransformation `json:"transformations,omitempty"`
}

type ResourceTransformationStrategy string

const Retain ResourceTransformationStrategy = "Retain"
const Replace ResourceTransformationStrategy = "Replace"

type ResourceTransformation struct {
	// Name of the input resource
	Input corev1.ResourceName `json:"input"`

	// Whether the input resource should be replaced or retained
	// +kubebuilder:default="Retain"
	Strategy ResourceTransformationStrategy `json:"strategy"`

	// Output resources and quantities per unit of input resource
	Outputs corev1.ResourceList `json:"outputs,omitempty"`
}
```
Given these types, the following list of 4-tuples

* (`nvidia.com/mig-1g.5gb`, `example.com/accelerator-memory`, `5Gi`, `replace`)
* (`nvidia.com/mig-2g.10gb`, `example.com/accelerator-memory`, `10Gi`, `replace`)
* (`nvidia.com/mig-4g.20gb`, `example.com/accelerator-memory`, `20Gi`, `replace`)
* (`nvidia.com/mig-1g.5gb`, `example.com/credits`, `10`, `replace`)
* (`nvidia.com/mig-2g.10gb`, `example.com/credits`, `15`, `replace`)
* (`nvidia.com/mig-4g.20gb`, `example.com/credits`, `30`, `replace`)
* (`cpu`, `example.com/credits`, `1`, `retain`)

would be represented in yaml when embedded in the Kueue Configuration object as
```yaml
resources:
  transformations:
  - input: nvidia.com/mig-1g.5gb:
    strategy: Replace
    outputs:
      example.com/accelerator-memory: 5G
      example.com/credits: 10
  - input: nvidia.com/mig-2g.10gb
    strategy: Replace
    outputs:
      example.com/accelerator-memory: 10G
      example.com/credits: 15
  - input: nvidia.com/mig-4g.20gb:
    strategy: Replace
    outputs:
      example.com/accelerator-memory: 20G
      example.com/credits: 30
  - cpu:
    strategy: Retain
    outputs:
      example.com/credits: 1
```

### Observability

Let's return to the first user story with MIG and look in more detail at how it works.
The transformation matrix is:
```yaml
resources:
  transformations:
  - input: nvidia.com/mig-1g.5gb:
    strategy: Replace
    outputs:
      example.com/accelerator-memory: 5G
  - input: nvidia.com/mig-2g.10gb:
    strategy: Replace
    outputs:
      example.com/accelerator-memory: 10G
  - input: nvidia.com/mig-4g.20gb:
    strategy: Replace
    outputs:
      example.com/accelerator-memory: 20G
  - input: nvidia.com/mig-7g.40gb:
    strategy: Replace
    outputs:
      example.com/accelerator-memory: 40G
```

We define a ClusterQueue with the covered resources below:
```yaml
...
  - coveredResources: ["cpu", "memory", "example.com/accelerator-memory"]
    flavors:
    - name: default-flavor
      resources:
      - name: "cpu"
        nominalQuota: 8
      - name: "memory"
        nominalQuota: 500G
      - name: example.com/accelerator-memory
        nominalQuota: 80G
```

The user submits a single Pod Job to this ClusterQueue with the resource requests/limits:
```yaml
    resources:
      requests:
        cpu: 1
        memory: 100G
      limits:
        nvidia.com/mig-1g.5gb: 2
        nvidia.com/mig-2g.10gb: 1
```

When the Job is admitted, the transformed resources appear in the Workload's Status as shown below:
```yaml
Status:
  Admission:
    Cluster Queue:  cluster-queue
    Pod Set Assignments:
      Count:  1
      Flavors:
        Cpu:                                default-flavor
        example.com/accelerator-memory:     default-flavor
        Memory:                             default-flavor
      Name:                                 main
      Resource Usage:
        Cpu:                                1
        example.com/accelerator-memory:     20G
        Memory:                             97656250Ki
```
and in the ClusterQueue Status we would see:
```yaml
Status:
  Admitted Workloads:  1
  Conditions:
    Last Transition Time:  2024-09-19T14:20:13Z
    Message:               Can admit new workloads
    Observed Generation:   3
    Reason:                Ready
    Status:                True
    Type:                  Active
  Flavors Reservation:
    Name:  default-flavor
    Resources:
      Borrowed:  0
      Name:      cpu
      Total:     1
      Borrowed:  0
      Name:      example.com/accelerator-memory
      Total:     20G
      Borrowed:  0
      Name:      memory
      Total:     97656250Ki
  Flavors Usage:
    Name:  default-flavor
    Resources:
      Borrowed:         0
      Name:             cpu
      Total:            1
      Borrowed:         0
      Name:             example.com/accelerator-memory
      Total:            20G
      Borrowed:         0
      Name:             memory
      Total:            97656250Ki
  Pending Workloads:    0
  Reserving Workloads:  1
```

After enough Workloads are admitted that there is queuing,
then the detailed status message in the `QuotaReserved` condition
of the Workload will indicate the resource(s) with insufficient quota, but the
actual transformed resource request is not observable.
```yaml
Spec:
  Active:  true
  Pod Sets:
    Count:  1
    Name:   main
    Template:
      Metadata:
      Spec:
        Containers:
          ...
          Resources:
            Limits:
              nvidia.com/mig-1g.5gb:   2
              nvidia.com/mig-2g.10gb:  1
            Requests:
              Cpu:                     1
              Memory:                  100G
              nvidia.com/mig-1g.5gb:   2
              nvidia.com/mig-2g.10gb:  1
...
Status:
  Conditions:
    Last Transition Time:  2024-09-19T14:59:01Z
    Message:               couldn't assign flavors to pod set main: insufficient unused quota for example.com/accelerator-memory in flavor default-flavor, 20G more needed
    Observed Generation:   1
    Reason:                Pending
    Status:                False
    Type:                  QuotaReserved
```

We could improve the observability by extending the Workload Status with a ResourceRequests
field that would contain a copy of the `TotalRequests` field of the controller-internal `workload.Info`
struct.  If we did this, we would instead see something like the below for a non-admitted Workload:
```yaml
Spec:
  Active:  true
  Pod Sets:
    Count:  1
    Name:   main
    Template:
      Metadata:
      Spec:
        Containers:
          ...
          Resources:
            Limits:
              nvidia.com/mig-1g.5gb:   2
              nvidia.com/mig-2g.10gb:  1
            Requests:
              Cpu:                     1
              Memory:                  100G
              nvidia.com/mig-1g.5gb:   2
              nvidia.com/mig-2g.10gb:  1
...
Status:
  Conditions:
    Last Transition Time:  2024-09-19T14:59:01Z
    Message:               couldn't assign flavors to pod set main: insufficient unused quota for example.com/accelerator-memory in flavor default-flavor, 20G more needed
    Observed Generation:   1
    Reason:                Pending
    Status:                False
    Type:                  QuotaReserved
  Resource Requests:
    Name: main
    Resources:
      Cpu:                             1
      example.com/accelerator-memory:  20G
      Memory:                          97656250Ki
```

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

##### Prerequisite testing updates

None

#### Unit Tests

Unit tests of the helper function invoked from `workload.NewInfo` will be added.

Unit tests of loading the `ResourceTransformation` from a `Configuration` will be added.

#### Integration tests

Because resource mappings are configured during manager startup, we need to run
an entire integration test suite with a non-empty resource transformation if we want to
do any integration testing with resource transformations. I'd propose to extend
`test/integration/controller/core` by adding a resource transformation to the manager that
maps from a synthetic extended resource to both `cpu` and `credits`.  We'd then add a
`test/integration/controller/core/resource_transform_test.go` that would define a cluster
queue with a resource group that covers `cpu` and `credit` and run a few quota and queuing
tests against it.

### Graduation Criteria

We should implement additional verification to flag overlaps between `resources.excludeResourcePrefixes`
and `resources.transformations`.  The implementation applies the exclusions before
the transformations, so if a resource both matches an exclusion prefix and is an input resource
to a transformation the transformation will never apply.  This could be detected when validating
the configuration and reported as a configuration error.

## Implementation History

2024-09-30: KEP Merged

## Drawbacks

Adds an additional dimension of configuration that needs to be documented
and maintained.

The existing `excludeResoucePrefixes` already meant that Kueue's
view of a Workload's resource request diverged from that of the
Kubernetes scheduler. However, this KEP allows significantly larger
divergences which puts additional pressure on ensuring the transformed
resource requests are observable to end users and cluster admins.

## Alternatives

A monolithic `ResourceTransformation` that is only read during operator startup
and cannot be selectively applied to Workloads may be overly limiting.
More flexible options could be supported by promoting `ResourceTransformation`
to a top-level cluster-scoped API object.  A cluster admin could
then define multiple `ResourceTransformation` instances and refer to them by name
in fields of other objects.

One advantage (and complexity) shared by all these alternatives is that
they enable resource mappings to be changed without requiring a restart of
the Kueue manager.  This comes at the implementation complexity of watching
for changes to `ResourceTransformation` instances and propagating them appropriately.

In all of these options, we would add either a `resourceMappingName` (`string`)
or a `resourceMappingNames` (`[]string`) field to an existing
API object.  In the later case, the named mappings would be combined
to construct a composite `ResourceTransformation` which would be stored as a new field in
the `Status` field of the API object.  If the merging process detects an inconsistent mapping
(for example the named mappings specify both `Retain` and `Replace` for the
same `input`), then an error would be reported via the `Conditions` of the API object's `Status`.

### ClusterQueue Scoped

A relatively simple option is to extend ClusterQueue with a `resourceMappingName(s)` field.
The logic of applying a resource mapping in `workload.NewInfo` is unchanged; the only difference
is that the resource mapping comes from the ClusterQueue's Status instead of from global `InfoOption`.

### ResourceFlavor Scoped

In the GitHub discussion of this KEP, we considered extended `ResourceFlavor` with a `resourceMappingName(s)` field.
This implementation looks significantly harder, as it would mean structural changes
in how flavor assignment is done.  In particular, we do not know the effective resource
request until we apply the transformation, but flavor assignment is done on a resource-by-resource
outer loop based on which ResourceGroup covers the requested resource.  Therefore it is not
clear where or how a ResourceFlavor scoped `ResourceTransformation` would be applied.

### Workload Scoped

The finest-grained option would be to support custom mapping on individual workloads.
This could be done by using an annotation to allow the user to specify a per-workload
resource mapping to be applied by Kueue (most likely in or around `workload.AdjustResources`).
The implementation of this (especially if restricted to an annotation that named a single mapping) would be
straightforward, but it is too fine-grained to be a good fit for any of the user stories discussed so far.
