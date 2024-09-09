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
  - [Option 1: Singleton Matrix Input Focused](#option-1-singleton-matrix-input-focused)
  - [Option 2: Singleton Matrix Output Focused](#option-2-singleton-matrix-output-focused)
  - [Comparison](#comparison)
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
the actual resource requests/limits that the Pods created by the Job 
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
* Extend the `workload.AdjustResources` function so that it applies this
matrix when adjusting the resource request for its input Workload object.

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

#### Story 1A - Using Replace with MIG

If an operator such as [InstaSlice][instaslice] is deployed
that is capable of dynamically managing the MIG partitions available on the cluster,
the cluster admin may desire to completely elide the details of different MIG variants
from their quota management.

To do this, the cluster admin could register the following transformations:
* (`nvidia.com/mig-1g.5gb`, `kueue.x-k8s.io/accelerator-memory`, `5G`, `replace`)
* (`nvidia.com/mig-2g.10gb`, `kueue.x-k8s.io/accelerator-memory`, `10G`, `replace`)
* (`nvidia.com/mig-4g.20gb`, `kueue.x-k8s.io/accelerator-memory`, `20G`, `replace`)
* (`nvidia.com/mig-7g.40gb`, `kueue.x-k8s.io/accelerator-memory`, `40G`, `replace`)

A Job requesting the resources below:
```yaml
  nvidia.com/mig-1g.5gb: 2
  nvidia.com/mig-2g.10gb: 1
  memory: 100M
  cpu: 1
```
would yield a Workload requesting:
```yaml
  kueue.x-k8s.io/accelerator-memory: 20G
  memory: 100M
  cpu: 1
```

In this scenario, the covered resources in a ClusterQueue would only need
to include `kueue.x-k8s.io/accelerator-memory`
and would not need to assign redundant and potentially confusing quotas to every MIG variant.

```yaml
...
  - coveredResources: ["cpu", "memory", "kueue.x-k8s.io/accelerator-memory"]
    flavors:
    - name: default-flavor
      resources:
      - name: "cpu"
        nominalQuota: ...
      - name: "memory"
        nominalQuota: ...
      - name: kueue.x-k8s.io/accelerator-memory
        nominalQuota: 320G
```
To compare, to acheive the identical effective quotas without `replace` the covered
resource stanza of each ClusterQueue would need to be:
```yaml
...
  - coveredResources: ["cpu", "memory", "kueue.x-k8s.io/accelerator-memory"]
    flavors:
    - name: default-flavor
      resources:
      - name: "cpu"
        nominalQuota: ...
      - name: "memory"
        nominalQuota: ...
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

#### Story 1B - Using Retain with MIG

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
would yield a Workload requesting:
```yaml
  kueue.x-k8s.io/accelerator-memory: 20G
  nvidia.com/mig-1g.5gb: 2
  nvidia.com/mig-2g.10gb: 1
  memory: 100M
  cpu: 1
```

Below is an example resource group that the cluster admin could define that
prevents the usage of `7g.40gb` paritions and puts a tighter cap on `4g.20gb`
partitions than would be implied by the `accelerator-memory` quota:
```yaml
...
  - coveredResources: ["cpu", "memory", "kueue.x-k8s.io/accelerator-memory"]
    flavors:
    - name: default-flavor
      resources:
      - name: "cpu"
        nominalQuota: ...
      - name: "memory"
        nominalQuota: ...
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
[instaslice] https://github.com/openshift/instaslice-operator

#### Story 2

This KEP would allow Kueue to augment a Job's resource requests
for quota purposes with additional resources that are not actually 
available in the Nodes of the cluster. For example, it could be used to 
define quotas in terms of a fictional currency `kueue.x-k8s.io/credits`
to give "costs" to various resources. This could be used to approximate
the actual monetary cost of a cloud resource (for example a larger or more powerful GPU
could cost twice as many `credits` as a smaller or previous generation GPU).
We'd expect in this usage pattern that `Replace` would be `false` so that
resource list is just augmented with the computed `credits` resource.  We'd also
expect that a ClusterQueue's `borrowingLimit` for `credits` would be set to `0`
to enforce the intended fiscal constraint.

## Design Details

The only non-trivial design question is how best to specify the configuration.

### Option 1: Singleton Matrix Input Focused
The entire transformation matrix is defined in one object.  Most likely
this object would be embedded inside Kueue's `Configuration` API, but
it would also be possible to make it a separate top-level config API or 
use a config map containing a single yaml field that would be deserialized
into the type defined below.

```go
type ResourceTransformationStrategy string
const Retain ResourceTransformationStrategy = "Retain"
const Replace ResourceTransformationStrategy = "Replace"

type ResourceTransformation struct {
  // key is the output resource name and value is the quantity of the output resource 
  // that corresponds to 1 unit of the input resource
  Functions map[corev1.ResourceName ]resource.Quantity `json:"functions"`

  // should the input resource be replaced (true) or retained (false)
  Strategy ResourceTransformationStrategy `json:"strategy"`
}

type ResourceTransformationMatrix struct {
  // The transformations to apply where the map key is the input resource name
  Transformations map[corev1.ResourceName]ResourceTransformation `json:"transformations"`
}
```
A key advantage of defining a single object is that it gives a cluster admin
a single source of truth.  By using maps instead of arrays, we avoid needing
to define the semantics of multiple entries having the same input or output
resource name.  Using the input resource name as the key to the outer map
avoids conflicting definitions of `Replace` for an input resource.

### Option 2: Singleton Matrix Output Focused
This is similar to Option 1, but makes the output resource the
primary key.  
```go
type ResourceTransformationStrategy string
const Retain ResourceTransformationStrategy = "Retain"
const Replace ResourceTransformationStrategy = "Replace"

type ResourceTransformationMatrix struct {
  // The transformations to apply where the first map key is the output resource name
  // and the inner map key is the input resource name
  Transformations map[corev1.ResourceName]map[corev1.ResourceName]resource.Quantity `json:"transformations"`

  // The map key is the input resource name.
  Strategy map[corev1.ResourceName]ResourceTransformationStrategy `json:"strategy"`
}
```

Grouping by output makes it easier for the cluster admin to see/specify
all the resources that map into a given output as a unit.  However it
comes at the cost of a separate map to specify strategy.
It may be confusing that the `Transformations` map is keyed by
output name as the primary key while `Strategy` is keyed by input name.

### Comparison

Consider the following list of 4-tuples

* (`nvidia.com/mig-1g.5gb`, `kueue.x-k8s.io/accelerator-memory`, `5G`, `replace`)
* (`nvidia.com/mig-2g.10gb`, `kueue.x-k8s.io/accelerator-memory`, `10G`, `replace`)
* (`nvidia.com/mig-4g.20gb`, `kueue.x-k8s.io/accelerator-memory`, `20G`, `replace`)
* (`nvidia.com/mig-1g.5gb`, `kueue.x-k8s.io/credits`, `10`, `replace`)
* (`nvidia.com/mig-2g.10gb`, `kueue.x-k8s.io/credits`, `15`, `replace`)
* (`nvidia.com/mig-4g.20gb`, `kueue.x-k8s.io/credits`, `30`, `replace`)
* (`cpu`, `kueue.x-k8s.io/credits`, `1`, `retain`)

Option 1
```yaml
resourceTransformationMatrix:
  transformations:
    nvidia.com/mig-1g.5gb:
      strategy: Replace
      functions:
        kueue.x-k8s.io/accelerator-memory: 5G
        kueue.x-k8s.io/credits: 10
    nvidia.com/mig-2g.10gb:
      strategy: Replace
      functions:
        kueue.x-k8s.io/accelerator-memory: 10G
        kueue.x-k8s.io/credits: 15
    nvidia.com/mig-4g.20gb:
      strategy: Replace
      functions:
        kueue.x-k8s.io/accelerator-memory: 20G
        kueue.x-k8s.io/credits: 30
    cpu:
      strategy: Retain
      functions:
        kueue.x-k8s.io/credits: 1
```

Option 2
```yaml
resourceTransformationMatrix:
  transformations:
    kueue.x-k8s.io/accelerator-memory:
      nvidia.com/mig-1g.5gb: 5G
      nvidia.com/mig-2g.10gb: 10G
      nvidia.com/mig-4g.20gb: 20G
    kueue.x-k8s.io/credits:
      nvidia.com/mig-1g.5gb: 10
      nvidia.com/mig-2g.10gb: 15
      nvidia.com/mig-4g.20gb: 30
      cpu: 1
  strategy:
    nvidia.com/mig-1g.5gb: Replace
    nvidia.com/mig-2g.10gb: Replace
    nvidia.com/mig-4g.20gb: Replace
    cpu: Retain
```

Option 2 is likely to be terser, but I find Option 1 slightly more intutive because
I tend to think of functions in the "forward direction" (mapping inputs to outputs).
Either is viable and has similar implementation complexity.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

##### Prerequisite testing updates

None

#### Unit Tests

Unit tests of the helper function invoked from `workload.AdjustResources` will be added.

Unit tests of loading the `ResourceTransformationMatrix` from a `Configuration` will be added.

#### Integration tests

Need to pick a few existing integration tests and run similar scenarios
on a kueue controller configured with resource transformations defined.
Probably the simplest approach would be to add the `credits` resource in
the second user story and submit a sequenece of jobs to a ClusterQueue
configured to have fewer `credits` than its corresponding cpu/memory resources
to verify that jobs are being queued based on `credits`.

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

We could define the functions on on a per-ClusterQueue basis, by
embedding a ResourceTransformationMatrix into the ResourceGroups specification.  This alternative
would provide additional flexibility, but in many cases would result in duplicated specification with
all the inherent risks of inconsistent specifications and increased admin burden.
Additionally, it would mean that the implementation would need to defer applying the transformation
until the Workload was being considered for admission by a specific ClusterQueue
as opposed to applying them once during Workload initialization.
