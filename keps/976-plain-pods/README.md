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
  name: pod-job
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
  name: job-worker-1
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

A Kueue webhook will inject to Pods subject to queueing:
- A scheduling gate `kueue.x-k8s.io/admission` to prevent the Pod from scheduling.
- A label `kueue.x-k8s.io/managed: true` so that users can easily identify pods that are/were
  managed by Kueue.

#### Pods subject to queueing

Not all Pods in a cluster should be subject to queueing. In particular the following pods should
be excluded from getting the scheduling gate or label.

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

When empty, the default NamespaceSelector is:

```yaml
matchExpressions:
- key: kubernetes.io/metadata.name
  operator: NotIn
  values: [kube-system]
```

### Constructing Workload objects

Once the webhook has marked Pods subject to queuing with the `kueue.x-k8s.io/managed: true` label,
the Pod reconciler can create the corresponding Workload object to feed the Kueue admission logic.

#### Single Pods

The simplest case we want to support is single Pod jobs. These Pods only have the label
`kueue.x-k8s.io/queue-name`, indicating the local queue where they will be queued.

When constructing the Workload object, kueue only populates a single podset using the podRef field.

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: Workload
metadata:
  name: workload-name
  namespace: pod-namespace
  ownerReferences:
  - apiVersion: v1
    kind: Pod
    name: pod-name
spec:
  queueName: queue-name
  podSets:
  - count: 1
    name: main
    podRef: pod-name
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
