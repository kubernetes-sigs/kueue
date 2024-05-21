# KEP-1834: List Admitted And Not Finished Workloads

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

Add a new visibility endpoint in Kueue for querying the list of Workloads that are still "running".

## Motivation

Jsonpath support in kubectl is limited, and we can not filter resources by condition in this way. By adding a new
visibility endpoint, users can list running workloads by `kubectl get`.

### Goals

* Support list workloads that are admitted and not finished.

### Non-Goals

## Proposal

We will add a new visibility endpoint in Kueue. 
``` go
// RunningWorkload is a user-facing representation of a running workload that summarizes the relevant information for
// assumed resources in the cluster queue.
type RunningWorkload struct {
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Priority indicates the workload's priority
	Priority int32 `json:"priority"`
	// AdmissionTime indecates the time workloads admitted
	AdmissionTime metav1.Time `json:"admissionTime"`
}

// +k8s:openapi-gen=true
// +kubebuilder:object:root=true

// RunningWorkloadsSummary contains a list of running workloads in the context
// of the query (within LocalQueue or ClusterQueue).
type RunningWorkloadsSummary struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Items []RunningWorkload `json:"items"`
}

// +kubebuilder:object:root=true
type RunningWorkloadsSummaryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []RunningWorkloadsSummary `json:"items"`
}

// +kubebuilder:object:root=true
// +k8s:openapi-gen=true
// +k8s:conversion-gen:explicit-from=net/url.Values
// +k8s:defaulter-gen=true

// RunningWorkloadOptions are query params used in the visibility queries
type RunningWorkloadOptions struct {
	metav1.TypeMeta `json:",inline"`

	// Offset indicates position of the first pending workload that should be fetched, starting from 0. 0 by default
	Offset int64 `json:"offset"`

	// Limit indicates max number of pending workloads that should be fetched. 1000 by default
	Limit int64 `json:"limit,omitempty"`
}
```

Users can list the running workloads by using
``` bash
kubectl get --raw "/apis/visibility.kueue.x-k8s.io/v1alpha1/clusterqueues/cluster-queue/runningworkloads"
```

We will show priority and localqueue information in response. Like this:
```
{"kind":"RunningWorkloadsSummary","apiVersion":"visibility.kueue.x-k8s.io/v1alpha1","metadata":{"creationTimestamp":null},"items":[{"metadata":{"name":"job-sample-job-jz228-ef938","namespace":"default","creationTimestamp":"2024-05-06T02:15:26Z","ownerReferences":[{"apiVersion":"batch/v1","kind":"Job","name":"sample-job-jz228","uid":"2de8a359-4c95-4159-b677-0279066149b6"}]},"priority":0,"admissionTime":"xxxx"}]}
```

### Risks and Mitigations

None.

## Design Details


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

New unit tests should be added testing the functionality for new api.

#### Integration tests

The idea is to enhance the existing integrations tests to check if workload objects are created with correct labels.

### Graduation Criteria

## Implementation History

* 2024-04-29 First draft

## Drawbacks

## Alternatives
