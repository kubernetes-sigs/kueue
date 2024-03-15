# KEP-77: Dynamically Sized JObs

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1 - RayCluster w/ autoscaling](#story-1---raycluster-w-autoscaling)
- [Design Details](#design-details)
  - [Workload Slices](#workload-slices)
  - [Creating Workload Slices](#creating-workload-slices)
  - [Pod Scheduling Gates](#pod-scheduling-gates)
  - [Garbage Collecting Workload Slices](#garbage-collecting-workload-slices)
- [Phases for MVP (alpha)](#phases-for-mvp-alpha)
  - [Phase 1 - Scale Down](#phase-1---scale-down)
    - [Job controller](#job-controller)
  - [Phase 2 - Aggregating Workload Slices](#phase-2---aggregating-workload-slices)
  - [Phase 3 - Scale up with Workload Slices and Scheduling Gates](#phase-3---scale-up-with-workload-slices-and-scheduling-gates)
    - [Scheduler](#scheduler)
- [Additional Details](#additional-details)
  - [Feature Gate](#feature-gate)
  - [Locking Flavor Assignments for Workload Slices](#locking-flavor-assignments-for-workload-slices)
  - [Webhook changes](#webhook-changes)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Ignore Resize from Kuberay](#ignore-resize-from-kuberay)
<!-- /toc -->

## Summary

Enable dynamic sizing of Kueue jobs. For the MVP, we will only focus on supporting RayClusters with autoscaling enabled, but other resources that can benefit from dynamic sizing should be supported eventually.

See: [Support dynamically sized (elastic) jobs #77](https://github.com/kubernetes-sigs/kueue/issues/77)

## Motivation

Kueue currently lacks support for resizing jobs. When a job is resized, Kueue will recreate the Workload representation of the job, leading to a disruptive suspend and requeue process. This limitation hinders the usability of Kueue for resources like RayCluster that sometimes have autoscaling capabilities enabled.

To properly support RayCluster, Kueue needs to gracefully handle the scale up and scale down of RayCluster nodes. Concretely this means that scaling the resources used by a RayCluster is appropriately reflected in the respective ClusterQueue without needing to suspend the entire cluster.

### Goals

- Gracefully handle resize operations for Kueue jobs (i.e. update quota usage without suspend, enqueue scale ups)
- Autoscaling RayCluster works with Kueue (MVP)

### Non-Goals

- Vertical scaling of workloads – Kueue will only handle resize operations that scale Pods horizontally 
- Support resize for other Kueue jobs such as QueuedWorkload, JobSet, etc (future)
- Resizing of RayJobs
- Partial Preemption

## Proposal

Update the Job framework reconciler and introduce new controllers to orchestrate dynamic resizing of jobs. We are only interested in horizontal scaling of jobs (e.g. scaling more replicas). At a high level, this will be accomplished by:
- Creating Workload Slice objects that represent incremental scale up of jobs. This gives us per-replica control of workload admission. Workload Slices will be garbage collected and consolidated with their parent workloads after successful admission.
- Adding default scheduling gates to control the scheduling of new pods based on their admission status.
- Dynamically adjust quotas in ClusterQueue based on scaling events.

For the MVP, we will only focus on admission of **RayClusters** with autoscaling enabled.

### User Stories

#### Story 1 - RayCluster w/ autoscaling

1. The user creates a RayCluster.
2. Kueue admits the RayCluster based on the requested resources in the head pod and worker pod. 
3. User updates RayCluster to enable autoscaling
4. Kueue will not suspend and requeue the RayCluster, instead it will dynamically update ClusterQueue usage based on the scaling event. 

## Design Details

### Workload Slices
To support horizontal scaling of jobs, we will introduce the concept of a "Workload Slice”. A Workload Slice is a Workload object with an owner reference to the original Workload for a job. Workload Slices represent per-replica changes to a job that were not initially accounted for when the job was created.

The benefit of Workload Slices is that Kueue can evaluate admission on a per-replica basis without changing the existing semantics of the Workload API. Once a Workload Slice is admitted, it will be garbage collected and its resources will be aggregated into the admission status of the parent workload.


### Creating Workload Slices

The [GenericJob interface](https://github.com/kubernetes-sigs/kueue/blob/main/pkg/controller/jobframework/interface.go#L30-L55) will be updated to handle resize operations of jobs.

```golang
type GenericJob interface {
  ...
  ...
  ResizeJob(wl *kueue.Workload) error
}
```
Jobs implementing the ResizeJob method will create a Workload Slice for every new replica of a job. 

### Pod Scheduling Gates

Inside **raycluster_webhook** implement schedulingGate injection for pods on RayCluster creation time. Which will then be ungated following a similar behavior as to how a job is suspended and then unsuspended in the beginning. When we have a scale up, the new pods will be gated due to the schedulingGates injection in the webhook.

After the creation of each individual Workload Slice and admission of a Workload Slice, the **workload_scheduling_gates_controller** should be in charge of removing the scheduling gates from each pod. We only need to ungate the number of pods to match the number of admitted pods, this should be a counter. We don’t want to accidentally ungate too many pods since race conditions could happen and we also don’t want to double count.

### Garbage Collecting Workload Slices

The logic of folding and deleting would be isolated in this controller that takes a look at the number of pods that were ungated. We don’t necessarily have to say that this Workload Slice belongs to a specific pod. 

1. You increment the ungated counter and pass the UID of the workload you are folding to the parent workload. If we still don’t see the workload being deleted, we at least know it has been counted towards the parent workload and we cannot count it again.  
2. This UID can be seen in the parent’s workload spec. 


## Phases for MVP (alpha)

### Phase 1 - Scale Down
Scaling down will be the first phase towards MVP because it can be implemented without introducing the Workload Slices. 

Scaling down a RayCluster won’t involve the creation of Workload Slices, instead it’ll involve an update to the current workload, no requeuing.

1. Compare job's PodSet.Count vs Workload.Spec.PodSets[1].Count (worker group) inside the jobframework generic reconciler. 
2. Call *updateWorkloadToMatchJob()*, this will construct and update the workload and in turn update the PodSet Count field.
3. Inside the *Update()* method from the *workload_controller* update the workload in cache and in queue. By updating the workload in cache this will update the cluster queue resource usage and by updating the workload in queue this will trigger the scheduler so that it re-assigns the flavors to the already assumed workload and in this way PodSetAssignments will be updated by applying admission based on the new assignments. 
4. Inside the schedule logic in the scheduler, since the workload is already assumed in the cache we need to specify if the feature is enabled so that we can apply admission to the workload and update its PodSetAssignments. 

#### Job controller

Rework *equivalentToWorkload()* and *reconcile()* to account for potential differences between the job’s number of replicas and the running workload’s PodSet.Count. 

Given these changes, check if the delta is positive or negative indicating a scaleup/scaledown. If it’s a scaledown the reconciler should trigger an update on the workload to match the new job’s spec and no quota needs to be checked or accounted for, the cluster queue should update the workload resource usage. 

### Phase 2 - Aggregating Workload Slices

In Phase 2, aggregating Workload Slices into the parent workload will be implemented.

### Phase 3 - Scale up with Workload Slices and Scheduling Gates

In Phase 3, scale up will be implemented by introducing Workload Slices and adding Pod scheduling gates as part of Kueue’s mutating admission for RayCluster. 

When the RayCluster scales, the RayCluster webhook would be modified to intercept and "gate" all pods. Every time there’s a resize, you create a dependable (child) workload slice and once it's admitted, you increase the count in the original workload, delete the old workload and remove the schedulingGates. 
- Pros: You are able to hold the pods added by Kuberay
- Cons: The fact of having schedulingGates, means we need an API call for every pod, because all pods that are created by the RayCluster are going to have schedulingGates. We need to remove those gates and for every pod you need to make API calls.

#### Scheduler
Since every scale up will have its own individual workload they should be proposed to the current scheduling cycle and continue the normal admission process. We should lock flavor assignments to Workload Slices we need to ensure that the Workload Slice is assigned the same resource flavor as the parent workload. 

The *nominate()* returns the workloads with their requirements (resource flavors, borrowing) if they were admitted by the clusterQueues in the snapshot, so we need to return the original workload that was already admitted with the resize information inside *TotalRequests* so that *PodSetAssignments* is also updated.

## Additional Details

### Feature Gate
In kube_features add Elastic/Dynamic size jobs feature gate.

### Locking Flavor Assignments for Workload Slices

We can extract the flavor(s) that the parent workload is using through wl.Status.Admission.PodSetAssigments[1].Flavors

We add a new field to the Workload Info object to know which parent flavor(s) were used.

```golang
// Info holds a Workload object and some pre-processing.
type Info struct {
   Obj *kueue.Workload
   // list of total resources requested by the podsets.
   TotalRequests []PodSetResources
   // Populated from the queue during admission or from the admission field if
   // already admitted.
   ClusterQueue   string
   LastAssignment *AssignmentClusterQueueState
 // Parent Flavors
 ParentFlavor []string
}
```

Which can be passed on as an extra parameter in *getAssignments()* in the scheduler to flavorassigner.go when it assigns a flavor through *assignFlavors()*

```golang
func (a *FlavorAssigner) assignFlavors(log logr.Logger, requests []workload.PodSetResources, ParentFlavor []string) Assignment {}
```

Which then calls *findFlavorForPodSetResource()* and we can use the parent flavor(s) value to check if this flavor can fit to the pod. If it doesn’t we don’t try to find another flavor for it.

### Webhook changes
In **workload_webhook** modify *validateWorkloadUpdate()* it to make PodSets and PodSetAssignments mutable for a running job. 

```diff
if workload.HasQuotaReservation(oldObj) {
-        allErrs = append(allErrs, apivalidation.ValidateImmutableField(newObj.Spec.PodSets, oldObj.Spec.PodSets, specPath.Child("podSets"))...)
        allErrs = append(allErrs, apivalidation.ValidateImmutableField(newObj.Spec.PriorityClassSource, oldObj.Spec.PriorityClassSource, specPath.Child("priorityClassSource"))...)
        allErrs = append(allErrs, apivalidation.ValidateImmutableField(newObj.Spec.PriorityClassName, oldObj.Spec.PriorityClassName, specPath.Child("priorityClassName"))...)
    }
...
- allErrs = append(allErrs, validateAdmissionUpdate(newObj.Status.Admission, oldObj.Status.Admission, field.NewPath("status", "admission"))...)

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

#### Unit Tests

<!--
In principle every added code should have complete unit test coverage, so providing
the exact set of tests will not bring additional value.
However, if complete unit test coverage is not possible, explain the reason of it
together with explanation why this is acceptable.
-->

<!--
Additionally, try to enumerate the core package you will be touching
to implement this enhancement and provide the current unit coverage for those
in the form of:
- <package>: <date> - <current test coverage>

This can inform certain test coverage improvements that we want to do before
extending the production code to implement this enhancement.
-->

#### Integration tests

### Graduation Criteria
<!--

Clearly define what it means for the feature to be implemented and
considered stable.

If the feature you are introducing has high complexity, consider adding graduation
milestones with these graduation criteria:
- [Maturity levels (`alpha`, `beta`, `stable`)][maturity-levels]
- [Feature gate][feature gate] lifecycle
- [Deprecation policy][deprecation-policy]

[feature gate]: https://git.k8s.io/community/contributors/devel/sig-architecture/feature-gates.md
[maturity-levels]: https://git.k8s.io/community/contributors/devel/sig-architecture/api_changes.md#alpha-beta-and-stable-versions
[deprecation-policy]: https://kubernetes.io/docs/reference/using-api/deprecation-policy/
-->

The feature starts at the alpha level, with a feature gate.

In the Alpha version, Dynamically Sized Jobs will support RayCluster resizing. 
- Scale down/up of replica workers


## Implementation History

<!--
Major milestones in the lifecycle of a KEP should be tracked in this section.
Major milestones might include:
- the `Summary` and `Motivation` sections being merged, signaling SIG acceptance
- the `Proposal` section being merged, signaling agreement on a proposed design
- the date implementation started
- the first Kubernetes release where an initial version of the KEP was available
- the version of Kubernetes where the KEP graduated to general availability
- when the KEP was retired or superseded
-->

## Drawbacks

<!--
Why should this KEP _not_ be implemented?
-->


## Alternatives


### Ignore Resize from Kuberay

- Idea: Ignoring the scale up or rejecting the scale up from the auto scaler and storing the value in an annotation so that Kueue takes a decision based on the annotation. We’d need a signal from Kueue to hold this in the raycluster_webhook and identify when the resize comes from Kueue, it has to be accepted.
- Pros: No need to intercept the pods and no need of using schedulingGates
- Cons: There would be a permanent race bewteen Kueue and Kuberay autoscaler to change the counter in the RayCluster replica number for the worker group. 
- Exploration: See if autoscaler would indicate a desired size in the spec without altering the number of replicas directly. 
- Discarded: Higher complexity than gating/ungating pods via SchedulingGates
