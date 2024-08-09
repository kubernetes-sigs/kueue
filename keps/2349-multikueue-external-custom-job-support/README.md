# KEP-2349: MultiKueue external custom Job support

<!--
This is the title of your KEP. Keep it short, simple, and descriptive. A good
title can help communicate what the KEP is and should be considered as part of
any review.
-->

<!--
A table of contents is helpful for quickly jumping to sections of a KEP and for
highlighting any additional information provided beyond the standard KEP
template.

Ensure the TOC is wrapped with
  <code>&lt;!-- toc --&rt;&lt;!-- /toc --&rt;</code>
tags, and then generate with `hack/update-toc.sh`.
-->

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1](#story-1)
  - [Constraints](#constraints)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
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

This KEP introduces the ability to create Multikueue Admission Check controllers with custom set of adapters.

## Motivation

Many Kueue adopters have company specific CustomResources.
Kueue should give a possibility to manage such CustomResources across multiple clusters.

### Goals

Give a possibility for external controllers to manage private CustomResources in a MultiKueue environment.

### Non-Goals

## Proposal

Make multiple instances of Multikueue admission check controller able to run simultaneously with different set of Multkiueue adapters in one cluster at the same time.
Provide a custom admission check controller name and adapters for Multikueue.
Specified controller is used by workload, cluster and admission check MultiKueue reconcilers.

### User Stories

#### Story 1

I would like to support the extension mechanism so that we can implement the MultiKueue controller for the custom / in-house Jobs.

### Constraints

Internally the MultiKueue uses a set of controller-runtime client, one for each MultiKueueCluster, the clients are used both by the MultiKueue internals and passed to the job adapters for the custom jobs synchronization.

The clients cannot be shared with an external controller and assuming the external controller can get the same connectivity to the worker clusters might be wrong, especially when we are taking about FS mounted kubeconfigs.

Designing additional APIs to communicate between the internal and external controller will bring extra complexity and latency at runtime. 

### Risks and Mitigations

## Design Details

Provide a way to integrate external controllers to manage Jobs in the Kueue.
MulikueueCluster API needs to be modified to ensure that one multikueue cluster is only managed by one admission check controller.

1. Turn Status of the `MultiKueueClusterStatus` into a map with ControllerName as a key and Conditions map as a value.

```golang
type MultiKueueClusterStatus struct {
 // +optional
 // +listType=map
 // +listMapKey=controllerName
 Status []MultiKueueClusterStatusConditions `json:"conditions,omitempty"`
}

type MultiKueueClusterStatusConditions struct {
 ControllerName string `json:"controllerName"`
 // +optional
 // +listType=map
 // +listMapKey=type
 // +patchStrategy=merge
 // +patchMergeKey=type
Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}
```

2. Make Controller Name used in Multikueue admission check configurable.
Extend MultiKueue Admission Check controller options to include controllerName.

```golang
type SetupOptions struct {
  ...
	controllerName    string
  ...
}

// WithControllerName - sets the controller name for which the multikueue
// admission check match.
func WithControllerName(controllerName string) SetupOption {
	return func(o *SetupOptions) {
		o.controllerName = controllerName
	}
}

func SetupControllers(mgr ctrl.Manager, namespace string, opts ...SetupOption) error {
...
}
```

3. Make the Adapters list for the MultiKueue Admission Check controller configurable.

```golang
type SetupOptions struct {
  ...
	adapters          map[string]jobframework.MultiKueueAdapter
  ...
}

// WithAdapters - sets all the MultiKueue adapters.
func WithAdapters(adapters map[string]jobframework.MultiKueueAdapter) SetupOption {
	return func(o *SetupOptions) {
		o.adapters = adapters
	}
}

// WithAdapter - sets or updates the adapter of the MultiKueue adapters.
func WithAdapter(adapter jobframework.MultiKueueAdapter) SetupOption {
	return func(o *SetupOptions) {
		o.adapters[adapter.GVK().String()] = adapter
	}
}
```

### Example configuration

2 admission checks for 2 custom controllers: `multikueue-controller-job` and `multikueue-controller-jobset`.
The `multikueue-controller-job` contains the adapter only for `batch/Jobs` and the `multikueue-controller-jobset` contains the adapter only for `jobSet`.

```yaml
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "cluster-queue"
spec:
  namespaceSelector: {} # match all.
  resourceGroups:
  - coveredResources: ["cpu", "memory"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "cpu"
        nominalQuota: 9
      - name: "memory"
        nominalQuota: 36Gi
  admissionChecks:
  - sample-multikueue-job
  - sample-multikueue-jobset
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: AdmissionCheck
metadata:
  name: sample-multikueue-job
spec:
  controllerName: kueue.x-k8s.io/multikueue-controller-job
  parameters:
    apiGroup: kueue.x-k8s.io
    kind: MultiKueueConfig
    name: multikueue-test
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: AdmissionCheck
metadata:
  name: sample-multikueue-jobset
spec:
  controllerName: kueue.x-k8s.io/multikueue-controller-jobset
  parameters:
    apiGroup: kueue.x-k8s.io
    kind: MultiKueueConfig
    name: multikueue-test
---

```


### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

##### Prerequisite testing updates

#### Unit Tests

Target is to keep same level or increase the coverage for all of the modified packages.
Cover the critical part of newly created code.

- `<package>`: `<date>` - `<test coverage>`

#### Integration tests

We are going to cover the following scenario:

Setup 2 distinct MultiKueue Admission Checks over the same multicluster.
Each Admission Check with different set of adapters and different controller names.
Prove that 2 different MultiKueue controllers can manage different job types runnning successfully in parallel.

### Graduation Criteria


## Implementation History

* 2024-06-20 First draft


## Drawbacks

The proposed solution requires that another custom MultiKueue Admission Check is created aside from the one running for built-in Jobs.

## Alternatives

1. Dedicated API for MultiKueue communication between admission check controller and abstraction layer controller.

2. Don't do any code changes, a custom controller can:
Import `multikueue` and `jobframework`, add adapters and run the MultKiueue admission check controller.
Will conflict with MultiKueue admission check controller in Kueue, which should be disabled (e.g. by command line parameter) in order to work.

3. Instead of having MultiKueue cluster associated with controller name, change the Multikueue cluster status to use a custom active condition for old controllers (controller name) that use it. 
