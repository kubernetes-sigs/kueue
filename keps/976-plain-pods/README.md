# KEP-976: Plain Pods

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
    - [Story 3](#story-3)
  - [Story 4](#story-4)
  - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
    - [Skipping Pods belonging to queued objects](#skipping-pods-belonging-to-queued-objects)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [Increased memory usage](#increased-memory-usage)
- [Design Details](#design-details)
  - [Simplifying the Workload object](#simplifying-the-workload-object)
  - [Gating Pod Scheduling](#gating-pod-scheduling)
    - [Pods subject to queueing](#pods-subject-to-queueing)
  - [Constructing Workload objects](#constructing-workload-objects)
    - [Single Pods](#single-pods)
    - [Groups of Pods with the same shape](#groups-of-pods-with-the-same-shape)
    - [Groups of pods with multiple shapes](#groups-of-pods-with-multiple-shapes)
    - [Groups of pods where driver generates workers](#groups-of-pods-where-driver-generates-workers)
  - [Tracking admitted and finished Pods](#tracking-admitted-and-finished-pods)
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

Some batch applications create plain Pods directly, as opposed to managing the Pods through the Job
API or a CRD that supports [suspend](https://kubernetes.io/docs/concepts/workloads/controllers/job/#suspending-a-job) semantics.
This KEP proposes mechanisms to queue plain Pods through Kueue, individually or in groups,
leveraging [pod scheduling gates](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-scheduling-readiness/).

## Motivation

Some batch systems or AI/ML frameworks create plain Pods to represent jobs or tasks of a job.
Currently, Kueue relies on the Job API or CRDs that support suspend semantics
to control whether the Pods of a job can exist and can be scheduled to Nodes.

While it is sometimes possible to wrap Pods on a CRD or migrate to the Job API, it could be
costly for framework or platform developers to do so.
In some scenarios, the framework doesn't know how many Pods belong to a single job. In more extreme
cases, Pods are created dynamically once the first Pod starts running. These are sometimes known
as elastic jobs.

A recent enhancement to Kubernetes Pods, scheduling gates, introduced in 1.26 as Alpha, and 1.27 as
Beta, allows an external controller to prevent kube-scheduler from scheduling Pods. Kueue can make
use of this API to implement queuing semantics for Pods.

### Goals

- Support queueing of individual Pods
- Support queueing of groups of Pods of fixed size, identified by a common label or annotation.
- Opt-in or opt-out Pods from specific namespaces from queuing.

### Non-Goals

- Support for partial-admission.

  Since all pods are already created, an implementation of partial admission would imply the
  deletion of some pods. It is not clear if this matches users expectations, as opposed to support
  for elastic groups.

- Support elastic groups of Pods, where the number of Pods changes after the job started.

  While these jobs are one of the motivations for this KEP, the current proposal doesn't support
  them. These jobs can be addressed in follow up KEPs.

## Proposal

<!--
This is where we get down to the specifics of what the proposal actually is.
This should have enough detail that reviewers can understand exactly what
you're proposing, but should not include things like API designs or
implementation. What is the desired outcome and how do we measure success?.
The "Design Details" section below is for the real
nitty-gritty.
-->

### User Stories (Optional)

#### Story 1

As an platform developer, I can queue plain Pods. I just add a queue name to the Pods through a
label.

<<[UNRESOLVED configurable labels ]>>
The label key name is configurable and it can also be an annotation.
<<[/UNRESOLVED]>>

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: foo
  namespace: pod-namespace
  labels:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  containers:
  - name: job
    image: hello-world
    resources:
      requests:
        cpu: 1m
```

#### Story 2

As a platform developer, I can queue groups of Pods that share the same shape (Pod specs).
In addition to the queue name, I can specify how many Pods belong to the group.

<<[UNRESOLVED usability of labels and annotations ]>>
Could the split among labels and annotations cause user mistakes?
Note that integers cannot be used as label values.
<<[/UNRESOLVED]>>

<<[UNRESOLVED configurable labels ]>>
The label keys are configurable and can also be annotations.
<<[/UNRESOLVED]>>

The pods of a job with a single spec (similar to Pods in an Indexed Job), look like follows:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: pod-index-0
  namespace: pod-namespace
  labels:
    kueue.x-k8s.io/queue-name: user-queue
    kueue.x-k8s.io/pod-group-name: pod-group
  annotations:
    kueue.x-k8s.io/pod-group-count: "10"
spec:
  containers:
  - name: job
    image: hello-world
    resources:
      requests:
        cpu: 1m
```

#### Story 3

As a platform developer, I can queue groups of Pods that have multiple shapes.
In addition to the queue name, I can specify how many shapes to expect for the group and how
many Pods are expected for the shape.

The pods of a job following a driver-workers paradigm look like follows:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: job-driver
  labels:
    kueue.x-k8s.io/queue-name: user-queue
    kueue.x-k8s.io/pod-group-name: pod-group
    kueue.x-k8s.io/pod-group-role: driver
  annotations:
    kueue.x-k8s.io/pod-group-roles: "2"
    kueue.x-k8s.io/pod-group-role-count: "1"
spec:
  containers:
  - name: job
    image: hello-world
    resources:
      requests:
        cpu: 1m
---
apiVersion: v1
kind: Pod
metadata:
  name: job-worker-0
  labels:
    kueue.x-k8s.io/queue-name: user-queue
    kueue.x-k8s.io/pod-group-name: pod-group
    kueue.x-k8s.io/pod-group-role: worker
  annotations:
    kueue.x-k8s.io/pod-group-roles: "2"
    kueue.x-k8s.io/pod-group-role-count: "10"
spec:
  containers:
  - name: job
    image: hello-world
    resources:
      requests:
        cpu: 1m
```

### Story 4

Motivation: In frameworks like Spark, worker Pods are only created after by the driver Pod. As
such, the worker Pods specs cannot be predicted beforehand. Even though the job could be considered
elastic, generally users wouldn't want to start a spark driver to run if no workers would fit.

As a Spark user, I can queue the driver Pod while providing the expected shape of the worker Pods.

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: job-driver
  labels:
    kueue.x-k8s.io/queue-name: user-queue
    kueue.x-k8s.io/pod-group-name: pod-group
    kueue.x-k8s.io/pod-group-role: driver
  annotations:
    kueue.x-k8s.io/pod-group-role-count: "1" # optional
    # The template in the driver group can be left empty. Kueue will populate it from the Pod.
    kueue.x-k8s.io/pod-group-sets: |-
      [
        {
          name: driver,
          count: 1,
        },
        {
          name: workers,
          count: 10,
          template:
            spec:
              containers:
                - name: worker
                  requests:
                    cpu: 1m
        }
      ]
spec:
  containers:
  - name: job
    image: hello-world
    resources:
      requests:
        cpu: 1m
---
apiVersion: v1
kind: Pod
metadata:
  name: job-worker-1
  labels:
    kueue.x-k8s.io/queue-name: user-queue
    kueue.x-k8s.io/pod-group-name: pod-group
    kueue.x-k8s.io/pod-group-role: worker
spec:
  containers:
  - name: job
    image: hello-world
    resources:
      requests:
        cpu: 1m
```

### Notes/Constraints/Caveats (Optional)

#### Skipping Pods belonging to queued objects

Pods owned by jobs managed by Kueue should not be subject to extra management.
These Pods can be identified based on the ownerReference. For these pods:
- the webhook should not add a scheduling gate
- the pod reconciler should not create a corresponding Workload object.

### Risks and Mitigations

#### Increased memory usage

In order to support plain Pods, we need to start watching all Pods, even if they are not supposed
to be managed by Kueue. This will increase the memory usage of Kueue just to maintain the
informers.

We can use the following mitigations:

1. Drop the unused managedFields field from the Pod spec, like kube-scheduler is doing
   https://github.com/kubernetes/kubernetes/pull/119556
2. Filter out terminal Pods from informers, as they no longer influence quota usage
   https://github.com/kubernetes/kubernetes/blob/99190634ab252604a4496882912ac328542d649d/pkg/scheduler/scheduler.go#L496

## Design Details

### Simplifying the Workload object

When a Workload just represents a single Pod, it's wasteful to duplicate the pod spec on the
Workload.
Instead, the Workload podset can refer back to the Pod itself.

Note that, for other objects, such as Job, the spec in the Workload serves as a snapshot of the
original Job, so that it is possible to modify a Job during admission (to inject affinities) and
revert the change on preemption. This is not a concern for Pods, because Pods can't be suspended.
They terminate as Failed if preempted. As a result, there is no need for a snapshot of the
original spec.

The Workload's PodSet will look as follows:

```golang
type PodSet struct {
  Name string
  Count int32
  Template *corev1.PodTemplateSpec
  PodRef *string
}
```

The value of PodRef is the name of the Pod object, in the same namespace.

### Gating Pod Scheduling

Pods subject to queueing should be prevented from scheduling until Kueue has admitted them in a
specific flavor.

Kubernetes 1.27 and newer provide the mechanism of [scheduling readiness](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-scheduling-readiness/)
to prevent kube-scheduler from assigning Nodes to Pods.

A Kueue webhook will inject to [Pods subject to queueing](#pods-subject-to-queueing):
- A scheduling gate `kueue.x-k8s.io/admission` to prevent the Pod from scheduling.
- A label `kueue.x-k8s.io/managed: true` so that users can easily identify pods that are/were
  managed by Kueue.
- A finalizer `kueue.x-k8s.io/managed` in order to reliably track pod terminations.

#### Pods subject to queueing

Not all Pods in a cluster should be subject to queueing.
In particular the following pods should be excluded from getting the scheduling gate or label.

1. Pods owned by other job APIs managed by kueue.

They can be identified by the ownerReference, based on the list of enabled integrations.

2. Pods belonging to specific namespaces (such as kube-system).

The set of namespaces is defined in Configuration.Integrations

```golang
type Integrations struct {
  Frameworks []string
  PodOptions *PodIntegrationPolicy
}

type PodIntegrationOptions struct {
  NamespaceSelector *metav1.LabelSelector
}
```

When empty, Kueue uses the following NamespaceSelector internally:

```yaml
matchExpressions:
- key: kubernetes.io/metadata.name
  operator: NotIn
  values: [kube-system]
```

### Constructing Workload objects

Once the webhook has marked Pods subject to queuing with the `kueue.x-k8s.io/managed: true` label,
the Pod reconciler can create the corresponding Workload object to feed the Kueue admission logic.

Note that the Workload cannot be owned by the Pod. Otherwise any cascade deletion of the Pod would
delete the Workload object, even before the Pod terminates (if it has a grace period).
This means that we need to manually delete the Workload object once we have determined that all pods
have finished.

#### Single Pods

The simplest case we want to support is single Pod jobs. These Pods only have the label
`kueue.x-k8s.io/queue-name`, indicating the local queue where they will be queued.

When constructing the Workload object, kueue only populates a single podset using the podRef field.
The Workload for the Pod in [story 1](#story-1) would look as follows:

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: Workload
metadata:
  name: pod-foo
  namespace: pod-namespace
spec:
  queueName: queue-name
  podSets:
  - count: 1
    name: main # this name is irrelevant.
    podRef: foo
```

<<[UNRESOLVED creating a Workload beforehand]>>
Could users create a Workload object before hand for groups of Pods?
This way, Pods would only need to have a label for the group name and the role. This would
simplify validation.
Should we make this a supported mode in addition to pure labels/annotations?
<<[/UNRESOLVED]>>

#### Groups of Pods with the same shape

When multiple pods belong to the same group and have the same shape, we need to know how many pods
belong to the group.

These groups of Pods can be identified when they have:
- the label `kueue.x-k8s.io/pod-group-name`, as a unique identifier for the group.
- the annotation `kueue.x-k8s.io/pod-group-count`, the number of pods to expect in the group

<<[UNRESOLVED expectations]>>

1. Can we assume that when a Pod fails it won't be recreated?
2. Alternatively, is there be a pod controller that handles recreation? If so, how do we know
   if the job finished, so that there wouldn't be more recreations?

Most likely, we should assume that the Pods are just plain Pods not controlled by a custom resource.
Otherwise, users should prefer to integrate directly with the Workload API, which is not a
significant effort, but would be more reliable.

3. Can we assume that the workloads are thrustworthy (no more pods are sent than the annotation states).

Annotations across multiple objects cannot easily be validated, as such, we should probably guard
against misuse.

<<[/UNRESOLVED]>>

<<[UNRESOLVED finalizers and Job API]>>

As pods terminate (success or failure), we need to free quota.
In order to reliably track terminated Pods, we need to add finalizers to pods. When the pods finish,
we increment a counter in the Workload status and remove the finalizer.

This problem is already solved by the Job API, so the natural question is: why not just use a Job?

<<[/UNRESOLVED]>>

The Workload object can be generated after observing the first Pod.
The Workload for the Pod in [story 2](#story-2) would look as follows:

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: Workload
metadata:
  name: pod-group
  namespace: pod-namespace
spec:
  queueName: queue-name
  podSets:
  - count: 10
    name: main # this name is irrelevant.
    podRef: pod-index-0 # any pod would do.
```

#### Groups of pods with multiple shapes or roles

When a group has multiple shapes, sometimes known as roles, we need to know how many shapes there
are. The Pods declare their role with the label `kueue.x-k8s.io/pod-group-role`. The annotation
`kueue.k8s.io/pod-group-role-count` contains how many pods belong to the role.

We can only build the Workload object once we observe at least one Pod per role.

The Workload for the Pod in [story 3](#story-3) would look as follows:

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: Workload
metadata:
  name: pod-group
  namespace: pod-namespace
spec:
  queueName: queue-name
  podSets:
  - count: 1
    name: driver 
    podRef: job-driver
  - count: 10
    name: worker
    podRef: job-worker-0 # any pod of the role would do
```

#### Groups of pods where driver generates workers

When most Pods of a group are only created after a subset of them start running, users need to
provide the shapes of the following pods before hand.

Users can provide the shapes of the remaining roles in an annotation
`kueue.x-k8s.io/pod-group-sets`, taking a yaml/json with the same structure as the Workload PodSets.
The template for the initial pods can be left empty, as it can be populated by Kueue.

The Workload for the Pod in [story 4](#story-4) would look as follows:

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: Workload
metadata:
  name: pod-group
  namespace: pod-namespace
spec:
  queueName: queue-name
  podSets:
  - count: 1
    name: driver 
    podRef: job-driver
  - count: 10
    name: worker
    podRef: job-worker-0 # any pod of the role would do
```

### Tracking admitted and finished Pods

Pods need to have finalizers so that we can reliably track how many of them run to completion and be
able to:
- Communicate reclaimable quota [#78](https://github.com/kubernetes-sigs/kueue/issues/78)
- Determine when the Workload is Finished.

#### On Admission

When a Workload is admitted, a new Pod reconciler would keep an in-memory cache of expected
admissions: the number of admitted pods that are not reflected in the informers yet.

In the Pod event handler, we decrement the counter when we see a transition from having
the scheduling gate `kueue.x-k8s.io/admission` to not having it.

In the Workload reconciler:
1. admitted_pods = admitted_pods_in_informer + expected_admissions. Note that this might temporarily
   lead to double counting.
2. For gated pods:
  - If admitted_pods < admission.count, remove the gate, set nodeSelector, an increase expected_admissions
  - Else,
    - If admitted_pods_in_informer < admission.count, we can't admit this Pod now to prevent
      overbooking, but requeue this Pod for retry.
    - Else, remove finalizer and delete the Pod, as it's beyond the allowed admission.
3. If the number of terminated pods with a finalizer is greater than or equal to the admission
  count, mark the Workload as Finished and remove the finalizers from the Pods.

In the Pod reconciler:
0. If the Pod is not terminated,
  create a Workload for the pod group if one does not exist.
1. If the Pod is terminated,
   - If the Workloald doesn't exist or the workload is finished, remove the finalizer.

Note that we are only removing Pod finalizers once the Workload is finished. This is a simple way
of managing finalizers, but it might lead to too many Pods lingering in etcd for a long time after
terminated. In a future version, we can consider a better scheme similar to Pod tracking in Jobs.

### Test Plan

<!--
**Note:** *Not required until targeted at a release.*
The goal is to ensure that we don't accept enhancements with inadequate testing.

All code is expected to have adequate tests (eventually with coverage
expectations). Please adhere to the [Kubernetes testing guidelines][testing-guidelines]
when drafting this test plan.

[testing-guidelines]: https://git.k8s.io/community/contributors/devel/sig-testing/testing.md
-->

[ ] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

##### Prerequisite testing updates

<!--
Based on reviewers feedback describe what additional tests need to be added prior
implementing this enhancement to ensure the enhancements have also solid foundations.
-->

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

- `<package>`: `<date>` - `<test coverage>`

#### Integration tests

<!--
Describe what tests will be added to ensure proper quality of the enhancement.

After the implementation PR is merged, add the names of the tests here.
-->

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

<!--
What other approaches did you consider, and why did you rule them out? These do
not need to be as detailed as the proposal, but should include enough
information to express the idea and why it was not acceptable.
-->
