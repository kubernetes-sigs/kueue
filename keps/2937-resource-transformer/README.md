# KEP-2937: Configurable Resource Transformers

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
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

In the user stories below, we use a 4-tuple (InputResourceName, OutputResourceName, OutputQuantity, Replace)
to express a resource transformation operation. In the actual implementation, this transformation would
be encoded as an entry in the  `ResourceTransformation` matrix which would be created by
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
these into a single extended resource `kueue.x-k8s.io/accelerator-memory` and completely elide
the MIG extended resource from the quota calculation.

Concretely, the cluster admin would register the following transformations:
* (`nvidia.com/mig-1g.5gb`, `kueue.x-k8s.io/accelerator-memory`, `5G`, `true`)
* (`nvidia.com/mig-2g.10gb`, `kueue.x-k8s.io/accelerator-memory`, `10G`, `true`)
* (`nvidia.com/mig-4g.20gb`, `kueue.x-k8s.io/accelerator-memory`, `20G`, `true`)

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

The covered resources in a ClusterQueue would only need to include `kueue.x-k8s.io/accelerator-memory`
and would not need to list every MIG variant resource. 

[mig]: https://docs.nvidia.com/datacenter/cloud-native/kubernetes/latest/index.html#the-mixed-strategy

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
type ResourceTransformation struct {
  // key is the output resource name and value is the quantity of the output resource 
  // that corresponds to 1 unit of the input resource
  Functions map[corev1.ResourceName ]resource.Quantity `json:"functions"`

  // should the input resource be replaced (true) or retained (false)
  Replace bool `json:"replace"`
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
type ResourceTransformationMatrix struct {
  // The transformations to apply where the first map key is the output resource name
  // and the inner map key is the input resource name
  Transformations map[corev1.ResourceName]map[corev1.ResourceName]resource.Quantity `json:"transformations"`

  // The map key is the input resource name. True means it should be replaced after it is transformed
  ReplaceInput map[corev1.ResourceName]bool `json:"replaceInput"`
}
```

Grouping by output makes it easier for the cluster admin to see/specify
all the resources that map into a given output as a unit.  However it
comes at the cost of a separate map to specify the replace boolean. 
It may also be confusing that the `Transformations` map is keyed by
output name as the primary key while `ReplaceInput` is keyed by input name.

### Comparison

Consider the following list of 4-tuples

* (`nvidia.com/mig-1g.5gb`, `kueue.x-k8s.io/accelerator-memory`, `5G`, `true`)
* (`nvidia.com/mig-2g.10gb`, `kueue.x-k8s.io/accelerator-memory`, `10G`, `true`)
* (`nvidia.com/mig-4g.20gb`, `kueue.x-k8s.io/accelerator-memory`, `20G`, `true`)
* (`nvidia.com/mig-1g.5gb`, `kueue.x-k8s.io/credits`, `10`, `true`)
* (`nvidia.com/mig-2g.10gb`, `kueue.x-k8s.io/credits`, `15`, `true`)
* (`nvidia.com/mig-4g.20gb`, `kueue.x-k8s.io/credits`, `30`, `true`)
* (`cpu`, `kueue.x-k8s.io/credits`, `1`, `false`)

Option 1
```yaml
resourceTransformationMatrix:
  transformations:
    nvidia.com/mig-1g.5gb:
      replace: true
      functions:
        kueue.x-k8s.io/accelerator-memory: 5G
        kueue.x-k8s.io/credits: 10
    nvidia.com/mig-2g.10gb:
      replace: true
      functions:
        kueue.x-k8s.io/accelerator-memory: 10G
        kueue.x-k8s.io/credits: 15
    nvidia.com/mig-4g.20gb:
      replace: true
      functions:
        kueue.x-k8s.io/accelerator-memory: 20G
        kueue.x-k8s.io/credits: 30
    cpu:
      replace: false
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
  replaceInput:
    nvidia.com/mig-1g.5gb: true
    nvidia.com/mig-2g.10gb: true
    nvidia.com/mig-4g.20gb: true
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

Unit tests of the helper function invoked from `workload.AdjustResources`
will be added.

- `pkg/workload`: `2024-09-07` - `2.8%`

#### Integration tests

Unclear what is necessary.  Please advise.

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
