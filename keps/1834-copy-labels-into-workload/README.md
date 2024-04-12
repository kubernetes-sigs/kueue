# KEP-1834: Copy some labels from Pods/Jobs into the Workload object 

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
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

When creating a kueue workload, copy the job (or pod) labels into the workload object (see https://github.com/kubernetes-sigs/kueue/issues/1834). Allow to configure which labels should be copied.

## Motivation

Currently the workloads do not "inherit" any of the labels of the jobs based on which they are created. Since workloads in Kueue are internal representations of the Kubernetes jobs, allowing to easily copy the labels of the job object into the workload object is natural and useful. For instance it would facilitate selection/filtering of the workloads by the administrators. 

### Goals

* Establish a mechanism to configure which set of labels should be copied.
* Copy the selected labels when creating the workload based on a job.

### Non-Goals
* This proposal does not contain any form of validation or analysis of the labels as they are going to be copied from one object to another.
* This proposal only concerns copying of labels into newly created workloads. Updating the labels of an existing workload (for example if the label of the underlying job is changed) is out of scope of this proposal.

## Proposal

We want to do the following API change. The proposal is to add a field named `labelKeysToCopy` into the configuration API (under `Integrations`). This field will hold a list of keys of labels that should be copied. This configuration will be global in the sense that it will apply to all the job frameworks. Since the list `labelKeysToCopy` will be empty by default, this change will not affect the existing functionality. 

We will not require for all the labels with keys from `labelKeysToCopy` to be present at the job object. When some of the labels will not be present at the job object, this label will not be assigned to the created workload object.

A case that requires more attention is creating workloads from pod groups because in that case a single workload is based on multiple pods (each of which might have labels). We propose:
 * When a label from the `labelKeysToCopy` list will be present at some of the pods from the group and the value of this label on all these pods will be identical then this label will be copied into the workload. Note that we do not require that all the pods have the label but all that do have must have the same value.
 * When multiple pods from the group will have the label with different values, we will raise an exception. The exception will be raised during the workload creation.


### Risks and Mitigations

None.

## Design Details


The proposal contains a single modification to the API. We propose to add a new field `LabelKeysToCopy` to `Integrations` struct in the `Configuration` API. 
``` go
type Integrations struct {
    ...
	// labelKeysToCopy is a list of label keys that should be copied from the job into the 
	// workload object. It is not required for the job to have all the labels from this 
	// list. If a job does not have some label with the given key from this list, the
	// constructed workload object will be created without this label. In the case
	// of creating a workload from a composable job (pod group), if multiple objects
	// have labels with some key from the list, the values of these labels must
	// match or otherwise the workload creation would fail. The labels are copied only
	// during the workload creation and are not updated even if the labels of the
	// underlying job are changed.
	LabelKeysToCopy []string `json:"labelKeysToCopy,omitempty"`
    ...
}
```
### Test Plan

<!--
**Note:** *Not required until targeted at a release.*
The goal is to ensure that we don't accept enhancements with inadequate testing.

All code is expected to have adequate tests (eventually with coverage
expectations). Please adhere to the [Kubernetes testing guidelines][testing-guidelines]
when drafting this test plan.

[testing-guidelines]: https://git.k8s.io/community/contributors/devel/sig-testing/testing.md
-->

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

##### Prerequisite testing updates

<!--
Based on reviewers feedback describe what additional tests need to be added prior
implementing this enhancement to ensure the enhancements have also solid foundations.
-->

#### Unit Tests

New unit tests should be added testing the functionality for jobs and pods.

#### Integration tests

The idea is to enhance the existing integrations tests to check if workload objects are created with correct labels.

### Graduation Criteria

## Implementation History

* 2024-04-08 First draft

## Drawbacks

With this KEP some workload objects that previously succeeded could fail to be created if such workload is based on a pod group with mismatched labels  (i.e., pods in the same pod group having different label values for some label key that should be copied). This will only happen if a user explicitly configures this label key to be copied.

This proposal introduces the label copying only during the workload creation. If a user modifies labels on a running job, the modification will not be reflected on the workload object, which might be confusing.

## Alternatives

An alternative way of handling mismatched labels would be to copy an arbitrary value among the values on the individual pods. The downside of this approach is that such a case is most likely due to an error and should not be silently accepted.

Another alternative could be to copy all the labels by default and have a list of excluded keys. 