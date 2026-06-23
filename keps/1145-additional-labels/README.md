# KEP-1145: Additional labels, annotations, tolerations and selector

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
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

Allow (AdmissionCheck)[https://github.com/kubernetes-sigs/kueue/tree/main/keps/993-two-phase-admission] 
to set some additional location/admission information
on workload's pods, so that they land on the machines of the `AdmissionCheck`
choosing.

## Motivation

`AdmissionChecks` is a "plugin" mechanism for Kueue. Some of the plugins while 
admitting may either narrow down on which machines the workload should start (by adding
some extra node selector/tolerations) or add some tracking information
that may be needed by other system components outside of Kueue.

### Goals

* Establish a mechanism through which `AdmissionChecks` can add annotations/labels/node selector/tolerations
  to workload pods.

### Non-Goals

* Allow `AdmissionChecks` to change pod definition in other way, for instance change command or image name.
* Hard code any specific labels/annotations that are passed down.

## Proposal

* Add a struct in `Workload's` `AdmissionCheckStatus` that allows 
  to set annotations, labels, selectors and tolerations on PodSets.
* Expend PodSetInfo in Integration Framework to pass this information.
* Modify existing integrations to support setting more data on the pods.

### User Stories (Optional)

#### Story 1

My external controller managing an `AdmissionCheck` preallocates specific
nodes (tainted and labeled) for a Workload to run, before it is 
admitted (and creates any worker pods). Workload pods need to be modified
so that they end up on correct nodes.

#### Story 2

My `AdmissionCheck` does budget checking and would like to mark pods that are possibly crossing budget with a 
specific label for easier tracking and alerting.

### Risks and Mitigations

Some integrations may not support setting all of the desired information
into the pods due to their CRD limitations.

## Design Details

Modify WorkloadStatus API object:

```
// WorkloadStatus defines the observed state of Workload
type WorkloadStatus struct {
    [...]
    AdmissionChecks []metav1.AdmissionCheckState `json:"admissionChecks,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

type AdmissionCheckState struct {
    // name identifies the admission check.
    // +required
    // +kubebuilder:validation:Required
    // +kubebuilder:validation:MaxLength=316
    Name string `json:"name"`

    [...]

    // +optional
    // +listType=atomic
    PodSetUpdates []PodSetUpdate `json:"podSetUpdates,omitempty"`
}

// PodSetUpdate contains a list of pod set modifications suggested by AdmissionChecks.
// The modifications should be additive only - modifications of already existing keys
// are not allowed and will result in failure during workload admission.
type PodSetUpdate struct {
    // Name of the PodSet to modify. Should match to one of the Workload's PodSets.
    Name string `json:"name"`

    // +optional
    Labels map[string]string `json:"labels,omitempty"

    // +optional
    Annotations map[string]string `json:"annotations,omitempty" 

    // +optional
    NodeSelector map[string]string `json:"nodeSelector,omitempty"

    // +optional
    Tolerations []corev1.Toleration `json:"tolerations,omitempty"
}
```

Each of the `AdmissionChecks` controllers should populate the needed fields before 
(or at the same time) flipping the corresponding `AdmissionCheck` to `True`. The values
from `PodSetUpdates` are combined with `ResourceFlavors` into integration framework
`PodSetInfo` (that will get the missing labels, tolerations and annotations fields) 
and then passed to the framework controller for application on the CRD.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

##### Prerequisite testing updates

None.

#### Unit Tests

Existing unit test should be updated to tests whether the new data are correctly
passed and applied on CRD. Changes may be needed in all framework integrations.

#### Integration tests

Existing integration test should be updated to tests whether the new data are correctly
passed and applied on CRD. Changes may be needed in all framework integrations.

### Graduation Criteria

We will graduate this feature to stable together with the whole AdmissionCheck/two
phase admission process.

## Implementation History

2023-09-22 KEP 

## Drawbacks

* `Workload` status becomes even more loaded. 
* Additional requirements for framework integrations.

## Alternatives

* `AdmissionCheck` could try to modify Workload's Spec, but then the spec would be reconciled
  with the Job and the changes would be discarded and Workload recreated with the matching Spec.
  
* `AdmissionCheck` could try to catch Job's pods after they are created and modify them on the fly
  via WebHook. This would however put an additional requirements on the `AdmissionCheck` to understand
  how framework integrations work and what Pods are created by them.
