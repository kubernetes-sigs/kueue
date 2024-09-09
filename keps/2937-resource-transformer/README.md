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
  - [Granularity Options](#granularity-options)
  - [Observabilty](#observabilty)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
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

Kueue currently performs a direct and non-configurable mapping of the resource
requests/limits of a Job into the resource requests of the Job's Workload.
This direct mapping results in two significant limitations that limit the expressivity
of Kueue's resource and quota mechanisms:
1. For a Job to be admissible by a ClusterQueue, the ClusterQueue must have
a ResourceGroup whose CoveredResources include all resources requested by the Job.
2. Only resources that are explicitly listed in a Jobs requests/limits can
be used in Kueue's quota calculations.

Injecting a configurable transformation function into this mapping
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

* Add a configuration mechanism that enables a cluster admin to specify a 
`ResourceTransformation` matrix.
* Extend `workload.NewInfo` to apply the matrix when computing the resources needed to admit the workload
 immediately after it applies `dropExcludedResources`.
* Optionally extend the status information for non-admitted workloads to make the full set of
transformed resources directly observable.

### User Stories (Optional)

In the user stories below, we use a 4-tuple (InputResourceName, OutputResourceName, OutputQuantity, Replace/Retain)
to express a resource transformation operation. In the actual implementation, this transformation would
be encoded as an entry in a `ResourceTransformation` matrix which would be created by
a cluster admin.

#### Story 1

This KEP will improve Kueue's ability to manage families of closely related 
extended resources without creating overly complex ResourceGroups in its 
ClusterQueues.  A motivating example is managing quotas for the various 
MIG resources created by the NVIDIA GPU Operator using the mixed strategy
(see [MIG Mixed Strategy][mig]).
In the mixed strategy, instead of there being a single `nvidia.com/gpu` extended resource, 
there is a proliferation of extended resource of the form `nvidia.com/mig-1g.5gb`, `nvidia.com/mig-2g.10gb`,
`nvidia.com/mig-4g.20gb`, and so on.  For quota management purposes, we would like to abstract
these into a single primary extended resource `kueue.x-k8s.io/accelerator-memory`.

##### Story 1A - Using Replace with MIG

If an operator such as [InstaSlice][instaslice] is deployed
that is capable of dynamically managing the MIG partitions available on the cluster,
the cluster admin may desire to completely elide the details of different MIG variants
from their quota management.

To do this, the cluster admin could register the following transformations:
* (`nvidia.com/mig-1g.5gb`, `kueue.x-k8s.io/accelerator-memory`, `5G`, `replace`)
* (`nvidia.com/mig-2g.10gb`, `kueue.x-k8s.io/accelerator-memory`, `10G`, `replace`)
* (`nvidia.com/mig-4g.20gb`, `kueue.x-k8s.io/accelerator-memory`, `20G`, `replace`)
* (`nvidia.com/mig-7g.40gb`, `kueue.x-k8s.io/accelerator-memory`, `40G`, `replace`)

In this scenario, the covered resources in a ClusterQueue would only need
to include `kueue.x-k8s.io/accelerator-memory`
and would not need to assign redundant and potentially confusing quotas to every MIG variant.

As a concrete example, consider defining a ClusterQueue with the ResourceGroup below:
```yaml
...
  - coveredResources: ["cpu", "memory", "kueue.x-k8s.io/accelerator-memory"]
    flavors:
    - name: default-flavor
      resources:
      - name: "cpu"
        nominalQuota: 8
      - name: "memory"
        nominalQuota: 500G
      - name: kueue.x-k8s.io/accelerator-memory
        nominalQuota: 80G
```


A Job requesting the resources below:
```yaml
  nvidia.com/mig-1g.5gb: 2
  nvidia.com/mig-2g.10gb: 1
  memory: 100M
  cpu: 1
```
would yield a Workload with an effective request of:
```yaml
  kueue.x-k8s.io/accelerator-memory: 20G
  memory: 100M
  cpu: 1
```

To compare, to achieve the identical effective quotas without `replace` the covered
resource stanza of each ClusterQueue would need to be:
```yaml
...
  - coveredResources: ["cpu", "memory", "kueue.x-k8s.io/accelerator-memory", "nvidia.com/mig-1g.5gb", "nvidia.com/mig-2g.10gb", "nvidia.com/mig-4g.20gb", "nvidia.com/mig-7g.40gb"]
    flavors:
    - name: default-flavor
      resources:
      - name: "cpu"
        nominalQuota: 8
      - name: "memory"
        nominalQuota: 500G
      - name: kueue.x-k8s.io/accelerator-memory
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
* (`nvidia.com/mig-1g.5gb`, `kueue.x-k8s.io/accelerator-memory`, `5G`, `retain`)
* (`nvidia.com/mig-2g.10gb`, `kueue.x-k8s.io/accelerator-memory`, `10G`, `retain`)
* (`nvidia.com/mig-4g.20gb`, `kueue.x-k8s.io/accelerator-memory`, `20G`, `retain`)
* (`nvidia.com/mig-7g.40gb`, `kueue.x-k8s.io/accelerator-memory`, `40G`, `retain`)

A Job requesting the resources below:
```yaml
  nvidia.com/mig-1g.5gb: 2
  nvidia.com/mig-2g.10gb: 1
  memory: 100M
  cpu: 1
```
would yield a Workload with an effective resource request of:
```yaml
  kueue.x-k8s.io/accelerator-memory: 20G
  nvidia.com/mig-1g.5gb: 2
  nvidia.com/mig-2g.10gb: 1
  memory: 100M
  cpu: 1
```

Below is an example ResourceGroup that the cluster admin could define that
prevents the usage of `7g.40gb` paritions and puts a tighter cap on `4g.20gb`
partitions than would be implied by the `accelerator-memory` quota:
```yaml
  - coveredResources: ["cpu", "memory", "kueue.x-k8s.io/accelerator-memory"]
    flavors:
    - name: default-flavor
      resources:
      - name: "cpu"
        nominalQuota: 8
      - name: "memory"
        nominalQuota: 500G
      - name: kueue.x-k8s.io/accelerator-memory
        nominalQuota: 200G
      - name: nvidia.com/mig-1g.5gb
        nominalQuota: 40  # 200G/5g
      - name: nvidia.com/mig-2g.10gb
        nominalQuota: 20  # 200G/10g
      - name: nvidia.com/mig-4g.20gb
        nominalQuota: 4  # less than the 10 that would be allowed by the acelerator-memory quota
      - name: nvidia.com/mig-7g.40gb
        nominalQuota: 0  # not allowed -- inefficient use of GPU resources
```

[mig]: https://docs.nvidia.com/datacenter/cloud-native/kubernetes/latest/index.html#the-mixed-strategy
[instaslice]: https://github.com/openshift/instaslice-operator

#### Story 2

This KEP would allow Kueue to augment a Job's resource requests
for quota purposes with additional resources that are not actually 
available in the Nodes of the cluster. For example, it could be used to 
define quotas in terms of a fictional currency `kueue.x-k8s.io/credits`
to give "costs" to various resources. This could be used to approximate
the actual monetary cost of a cloud resource (for example a larger or more powerful GPU
could cost twice as many `credits` as a smaller or previous generation GPU).
We'd expect in this usage pattern that `Replace` would be used so that
resource list is just augmented with the computed `credits` resource.  We'd also
expect that a ClusterQueue's `borrowingLimit` for `credits` would be set to `0`
to enforce the intended fiscal constraint.

## Design Details

Since Kueue's workload.Info struct is already a well-defined
place where resource filtering is being applied to compute the effective resource
request from a Workload's PodSpec, the core of the implementation is
straightforward and follows from how `excludedResourcePrefixes` are already processed.

The non-trivial design issues are how to configure the transformations,
the possible granularity of that configuration, and how to maximize observability
of the effective resources requested by a Workload.

### Specifying the Transformation

A transformation matrix is defined as a single object.  Most likely
this object would be embedded inside Kueue's `Configuration` API, but
it would also be possible to make it a separate top-level config API or 
use a config map containing a single yaml field that would be deserialized
into the type defined below.  To support finer-grained configuration, instead
of defining a single controller-wide transformation matrix, they could be embedded
in ClusterQueue ResourceGroups (perhaps implicitly by instead embedding the
transformation matrix in a ResourceFlavor).

```go
type ResourceTransformationStrategy string
const Retain ResourceTransformationStrategy = "Retain"
const Replace ResourceTransformationStrategy = "Replace"

type ResourceTransformation struct {
  // key is the output resource name and value is the quantity of the output resource 
  // that corresponds to 1 unit of the input resource
  ConversionBaseValues map[corev1.ResourceName]resource.Quantity `json:"conversionBaseValues"`

  // should the input resource be replaced or retained
  Strategy ResourceTransformationStrategy `json:"strategy"`
}

// The map key is the input resource name
type ResourceTransformationMatrix map[corev1.ResourceName]ResourceTransformation

```
By using maps instead of arrays, we avoid needing
to define the semantics of multiple entries having the same input or output
resource name.  Using the input resource name as the key to the outer map
avoids conflicting definitions of `Replace` for an input resource.

As an example, the following list of 4-tuples

* (`nvidia.com/mig-1g.5gb`, `kueue.x-k8s.io/accelerator-memory`, `5G`, `replace`)
* (`nvidia.com/mig-2g.10gb`, `kueue.x-k8s.io/accelerator-memory`, `10G`, `replace`)
* (`nvidia.com/mig-4g.20gb`, `kueue.x-k8s.io/accelerator-memory`, `20G`, `replace`)
* (`nvidia.com/mig-1g.5gb`, `kueue.x-k8s.io/credits`, `10`, `replace`)
* (`nvidia.com/mig-2g.10gb`, `kueue.x-k8s.io/credits`, `15`, `replace`)
* (`nvidia.com/mig-4g.20gb`, `kueue.x-k8s.io/credits`, `30`, `replace`)
* (`cpu`, `kueue.x-k8s.io/credits`, `1`, `retain`)

would be represented in yaml when embedded in the Kueue Configuration object as
```yaml
resources:
  excludedResourcePrefixes: []
  transformations:
    nvidia.com/mig-1g.5gb:
      strategy: Replace
      conversionBaseValues:
        kueue.x-k8s.io/accelerator-memory: 5G
        kueue.x-k8s.io/credits: 10
    nvidia.com/mig-2g.10gb:
      strategy: Replace
      conversionBaseValues:
        kueue.x-k8s.io/accelerator-memory: 10G
        kueue.x-k8s.io/credits: 15
    nvidia.com/mig-4g.20gb:
      strategy: Replace
      conversionBaseValues:
        kueue.x-k8s.io/accelerator-memory: 20G
        kueue.x-k8s.io/credits: 30
    cpu:
      strategy: Retain
      conversionBaseValues:
        kueue.x-k8s.io/credits: 1
```

### Granularity Options

There are three natural options.

1. Following how `excludedResourcePrefixes` works, define a single transformation matrix at the operator level.
2. Allow per-ClusterQueue configuration by making the transformation matrix part of the ResourceGroup
3. Define the transformation matrix as part of the ResourceFlavor definition.

Options 1 and 2 are simplest to implement.  I have implemented 1 in a prototype already.  I'm confident 2 could be done almost
identically (there is just a different logic to compute the `InfoOptions` for the ClusterQueue).

Option 3 is interesting because it allows more flexibility than Option 1, but should avoid many redundant
specifications of the same transformation matrix (the big drawback of Option 2).  There may be some additional
implementation complexity because instead of applying the transformation in `workload.NewInfo()` we would
instead need to do it during flavor assignment.  It seems like doing it in `findFlavorForPodSetResource`
would work, but making the results observable in the Workload Status may be significantly harder, especially
with fungible flavors that may define different transformation functions for the same input resource.

### Observabilty

Let's return to the first user story with MIG and look in more detail at how it works.
The transformation matrix is:
```yaml
resources:
  excludedResourcePrefixes: []
  transformations:
    nvidia.com/mig-1g.5gb:
      strategy: Replace
      conversionBaseValues:
        kueue.x-k8s.io/accelerator-memory: 5G
    nvidia.com/mig-2g.10gb:
      strategy: Replace
      conversionBaseValues:
        kueue.x-k8s.io/accelerator-memory: 10G
    nvidia.com/mig-4g.20gb:
      strategy: Replace
      conversionBaseValues:
        kueue.x-k8s.io/accelerator-memory: 20G
    nvidia.com/mig-7g.40gb:
      strategy: Replace
      conversionBaseValues:
        kueue.x-k8s.io/accelerator-memory: 40G
```

We define a ClusterQueue with the covered resources below:
```yaml
...
  - coveredResources: ["cpu", "memory", "kueue.x-k8s.io/accelerator-memory"]
    flavors:
    - name: default-flavor
      resources:
      - name: "cpu"
        nominalQuota: 8
      - name: "memory"
        nominalQuota: 500G
      - name: kueue.x-k8s.io/accelerator-memory
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
        kueue.x-k8s.io/accelerator-memory:  default-flavor
        Memory:                             default-flavor
      Name:                                 main
      Resource Usage:
        Cpu:                                1
        kueue.x-k8s.io/accelerator-memory:  20G
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
      Name:      kueue.x-k8s.io/accelerator-memory
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
      Name:             kueue.x-k8s.io/accelerator-memory
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
    Message:               couldn't assign flavors to pod set main: insufficient unused quota for kueue.x-k8s.io/accelerator-memory in flavor default-flavor, 20G more needed
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
    Message:               couldn't assign flavors to pod set main: insufficient unused quota for kueue.x-k8s.io/accelerator-memory in flavor default-flavor, 20G more needed
    Observed Generation:   1
    Reason:                Pending
    Status:                False
    Type:                  QuotaReserved
  ResourceRequests:
    Cpu:                                1
    kueue.x-k8s.io/accelerator-memory:  20G
    Memory:                             97656250Ki
```

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

##### Prerequisite testing updates

None

#### Unit Tests

Unit tests of the helper function invoked from `workload.NewInfo` will be added.

Unit tests of loading the `ResourceTransformationMatrix` from a `Configuration` will be added.

#### Integration tests

Because resource transformations are configured during manager startup, we need to run
an entire integration test suite with a non-empty resource transformation if we want to
do any integration testing with resource transformations. I'd propose to extend
`test/integration/controller/core` by adding a resource transformation to the manager that
maps from a synthetic extended resource to both `cpu` and `credits`.  We'd then add a
`test/integration/controller/core/resource_transform_test.go` that would define a cluster
queue with a resource group that covers `cpu` and `credit` and run a few quota and queuing
tests against it.

### Graduation Criteria

## Implementation History

## Drawbacks

Adds an additional dimension of configuration that needs to be documented
and maintained.

## Alternatives

There are other mechanisms that could be used to configure Kueue with
the 4-tuples needed for a ResourceTransformation.

I roughed out several finer-grained options where we would define
a new custom resource to either specify a single 4-tuple or a single 
row or single column of the matrix.  A challenge with any of the finer-grained designs is that many 
extended resource names are not valid Kubernetes object names because
they include a `/`.  As a result, it will be quite easy for a cluster admin to define a collection
of custom resources that would result in multiple conflicting definitions of the same
cell in the matrix. The complexity of detecting and handling this plus the implementation
cost of an additional reconciller do not seem justified.
