# KEP-993: Two Stage Admission Process

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
  - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Deprecation](#deprecation)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This KEP defines the extension point to plug in arbitrary, additional checks
for workloads before they are admitted for execution. These external checks may
be implemented inside or outside of Kueue and may provide functionality like
budgeting, capacity provisioning or some elements of multicluster dispatching.

## Motivation

With the growth of Kueue, not all desired elements may land inside of the core
Kueue. Some users may even not want to publish their custom admission logic in
a public repository. At the same time, we don’t want to force users to
fork Kueue. Thus, an pluggable mechanism for workload admission needs to
be established.

### Goals

Define mechanism to allow external controllers:

* to give green light to admit a workload.
* to temporarily hold workload admission.
* to stop a previously admitted workload and move it back to the
suspended state.

### Non-Goals

Provide a specific design or implementation for any external controller.

## Proposal

Extend ClusterQueue definition with a list of external controller names that
need to give green light to admit a workload. Until all of the controllers, which
identify with these names, don’t give a green light, the workload will not
be admitted. 

Each workload that goes to such a queue, will get additional conditions, 
in a dedicated field in the status, each reflecting the go/no-go 
decision of each of the external controllers. Once
a controller is confident that the workload can be admitted, it flips the
status of the corresponding condition from "Unknown" to "True". 

If a controller changes its mind about a workload, it can switch the condition
back to false and the workload will be immediately suspended.

### User Stories (Optional)

#### Story 1

I want to use the [Cluster Autoscaler ProvisioningRequest API](https://github.com/kubernetes/autoscaler/pull/5848) 
to ensure that the resources can be provided in full before starting the workload.  

#### Story 2

I want to base workload admission on budget. I have the actual consumption metrics
in my Prometheus/Datadog/Stackdriver/whatever monitoring system. And weekly
budget in a CRD, per namespace. I’m happy to modify some small existing sample
controller, but I don’t want to fork the whole Kueue to adjust some minor details.
 
### Notes/Constraints/Caveats (Optional)

Each of the go/no-go decisions is best-effort and can be reversed at any time. 

### Risks and Mitigations

## Design Details

TL;DR; 
* extend ClusterQueueSpec with the list of checks.
* add extra conditions to Workload status.
* introduce new CRD, AdmissionCheck.

Details:

We will extend ClusterQueueSpec definition by adding a reference to
AdmissionChecks that the queue needs to perform before admitting a
workload. The design of AdmissionChecks is similar to IngressClass
- it allows passing additional parameters, specific to particular checks
that need to be performed. 

```
// ClusterQueueSpec defines the desired state of ClusterQueue.
type ClusterQueueSpec struct {
[...]
	// List of AdmissionCheck names that needs to be passed
	// to admit a workload. The order of the checks doesn't matter,
	// all of them will be started at the same time.
	AdmissionChecks []string `json:"admissionChecks"`
}

// WorkloadStatus defines the observed state of Workload.
type WorkloadStatus struct {
[...]
	// Status of admission checks, if there are any specified 
	// for the ClusterQueue in which the workload is queued.
	AdmissionChecks []metav1.Condition `json:"admissionChecksConditions,omitempty"`
}

// Condition names used in WorkloadStatus
const (
	AdmissionPrecheck string = "AdmissionPrecheck"
)

// Cluster-scoped top-level object defining a check that 
// can be referenced in ClusterQueue.
type AdmissionCheck struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec AdmissionCheckSpec `json:"spec,omitempty"`
}

// Condition Reasons used by AdmissionChecks.
const (
	// The check cannot pass at this moment, back off (possibly 
	// allowing other to try, unblock quota) and retry. Can be set only
	// when Condition status is set to "False"
	Retry string = "Retry"
	// The check will not pass in the near future. It is not worth
	// to retry. Can be set together with Condition status "False".
	Reject string = "Reject"
	// The check might pass if there was fewer workloads. Proceed
	// with any pending workload preemption. Should be set 
	// when condition status is still "Unknown" 
	PreemptionRequired status = "PreemptionRequired"
)

// AdmissionCheckSpec defines the desired state of AdmissionCheck.
type AdmissionCheckSpec struct {
	// Name of the controller which will actually perform
	// the checks. This is the name with which controller identifies with,
	// not a K8S pod or deployment name. Cannot be empty. 
	ControllerName string `json:"controllerName"`

	// A reference to the additional parameters for the check. 
	Parameters *AdmissionCheckParametersReference `json:"parameters,omitempty"`

	// preemptionPolicy determines when to issue preemptions for the Workload,
	// if necessary, in relationship to the status of the admission check.
	// The possible values are:
	// - `Anytime`: No need to wait for this check to pass before issuing preemptions.
	//   Preemptions might be blocked on the preemptionPolicy of other AdmissionChecks.
	// - `AfterCheckPassedOrOnDemand`: Wait for this check to pass pass before issuing preemptions,
	//   unless this or other checks requests preemptions through the Workload's admissionChecks.
	// Defaults to `Anytime`.
	PreemptionPolicy *PreemptionPolicy `json:"preemptionPolicy,omitempty"`
}

const (
	Anytime PreemptionPolicy = "Anytime"
	AfterCheckPassedOrOnDemand PreemptionPolicy = "AfterCheckPassedOrOnDemand"
)

// Points to where the parameters for the checks are. 
// As clusterqueue are in cluster scope - this should be
// a dedicated CRD specific for the Controller.
type AdmissionCheckParametersReference struct {
	// ApiGroup is the group for the resource being referenced. 
	APIGroup string `json:"apiGroup"`

	// Kind is the type of the resource being referenced.
	Kind string `json:"kind"`

	// Name is the name of the resource being referenced.
	Name string `json:"name"`
}
```

For every workload that is put to a ClusterQueue that has AdmissionChecks
configured Kueue will add:

* "QuotaReserved" set `False` in Conditions.
* "<checkName>" set to `Unknown` for each of the AdmissionChecks to AdmissionCheckConditions.

Kueue will perform the very same checks that it does today, 
before admitting a workload. However, once the basic checks 
pass AND there are some AdmissionChecks configured, AND the
workload is not in on-hold retry state from some check, it will:

1. Fill the Admission field in workload, with the desired flavor assignment. 
2. Not do any preemptions yet (unless BookCapacity is set to true).
3. Set "QuotaReserved" to true.

Kueue will only pass as many pods into "QuotaReserved" as there would
fit in the quota together, assuming that necessary preemptions will happen.
That would violate a bit BestEffort logic of queues - if there is 1000 tasks
in a queue that doesn’t pass quota and 1001st task passes, the best effort
queue would let it in. BestEffort queue with 1000 tasks that don’t pass
AdmissionChecks would not let 1001st task in (it will not reach the
checks). Without this limitation, a large number of tasks could be switched
back and forth from "QuotaReserved" to suspended state. 

Preemptions might happen at this point or later, depending on the `preemptionPolicy` of each
AdmissionCheck. Preemption can happen immediately if all AdmissionChecks have a `preemptionPolicy`
of `Anytime`. Otherwise, it will happen as soon as:
- An AdmissionCheck requests preemptions using the reason `PreemptionRequired` in the check
  status posted to the Workload's `.status.admissionChecks`, or
- All AdmissionChecks with a `preemptionPolicy` of `AfterCheckPassedOrOnDemand` are reported as
  `True` in the Workload's `.status.admissionChecks`.

Note that all Workloads that have a condition `QuotaReserved` are candidates for preemption. If
any of these Workloads needs to be preempted:
1. the `QuotaReserved` condition is set to `False`
2. `.status.admissionChecks` is cleared.
3. Controllers for the admission checks should stop any operations started for this Workload.

Once all admission checks are satisfied (set to `True`), Kueue will recheck that Admission settings
are still valid (check quota/preemptions/reclamation) and admit the workload (doing any pending or
newly identified preemptions, if needed), setting the `Admitted`
condition to True.

If any check is switched to "False", with reason "Reject" or "Retry", the workload is either
completely rejected or switched back to suspend state (with retry delay).
Setting reason to BookQuotaRequired (while keeping condition as "Unknown")
means that some other, external quota is reached
and Kueue should try to preempt some workloads, before the check can
succeed.

The controller implementing a particular check should:
 
* Watch all AdmissionCheck objects to know which one should be handled by it. 
* Watch all controller specific parameter objects, potentially referenced from AdmissionCheck.
* Watch all workloads and process those that have AdmissionCheck for this
  particular controller and are past AdmissionPrecheck.
* After approving the workload, keep an eye on the check if it starts failing,
  fail the check and cause the workload to move back to the suspended state.

### Test Plan

[ x ] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

##### Prerequisite testing updates

#### Unit Tests

#### Integration tests

The tests should cover:

* Transition from `Suspended` to `Prechecked`, with `AdmissionChecks` added.
* Transition from `Prechecked` and `AdmissionCheck` passed back to `Admitted`
* Transition from `Prechecked` back to `Suspended` because of quota change
* Transition from `Prechecked` and AdmissionCheck passed back to `Suspended` because of different resource flavors available. 
* Transition from `Prechecked` to `Suspended` due to Retry with 1 and 2 AdmissionChecks.
* Transition from `Prechecked` to `Rejected` with 1 and 2 AdmissionChecks (only one is required to reject a workload)
* Transition from `Prechecked` to `Admitted` with 0 AdmissionChecks.

### Graduation Criteria

#### Deprecation

We deprecate the RetryDelayMinutes, and going to remove it in feature versions of the API.

First iteration (0.8):
  - deprecate the RetryDelayMinutes field on API.

Second iteration (v1beta2):
  - remove the RetryDelayMinutes field from AdmissionCheckSpec object on bumping the API version.

## Implementation History

## Drawbacks

## Alternatives


