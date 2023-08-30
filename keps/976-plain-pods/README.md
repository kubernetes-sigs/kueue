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
  - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
    - [Skipping Pods belonging to queued objects](#skipping-pods-belonging-to-queued-objects)
    - [Pods replaced on failure](#pods-replaced-on-failure)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [Increased memory usage](#increased-memory-usage)
    - [Limited size for annotation values](#limited-size-for-annotation-values)
- [Design Details](#design-details)
  - [Gating Pod Scheduling](#gating-pod-scheduling)
    - [Pods subject to queueing](#pods-subject-to-queueing)
  - [Constructing Workload objects](#constructing-workload-objects)
    - [Single Pods](#single-pods)
    - [Groups of Pods created beforehand](#groups-of-pods-created-beforehand)
    - [Groups of pods where driver generates workers](#groups-of-pods-where-driver-generates-workers)
  - [Tracking admitted and finished Pods](#tracking-admitted-and-finished-pods)
  - [Metrics](#metrics)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Beta](#beta)
    - [GA](#ga)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Users create a Workload object beforehand](#users-create-a-workload-object-beforehand)
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

- Support queueing of individual Pods.
- Support queueing of groups of Pods of fixed size, identified by a common label or annotation.
- Opt-in or opt-out Pods from specific namespaces from queuing.

### Non-Goals

- Support for [dynamic reclaiming quota](https://github.com/kubernetes-sigs/kueue/issues/78)

  This feature is incompatible with supporting Pod replacements without knowing the behavior of a
  parent controller for the Pods.

- Support for [partial-admission](https://github.com/kubernetes-sigs/kueue/issues/420).

  Since all pods are already created, an implementation of partial admission would imply the
  deletion of some pods. It is not clear if this matches users expectations, as opposed to support
  for elastic groups.

- Support elastic groups of Pods, where the number of Pods changes after the job started.

  While these jobs are one of the motivations for this KEP, the current proposal doesn't support
  them. These jobs can be addressed in follow up KEPs.

- Support for advanced Pod retry policies

  Kueue shouldn't re-implement core functionalities that are already available in the Job API.

- Tracking usage of Pods that were not queued through Kueue.

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

As a platform developer, I can queue groups of Pods that might or might not have the same shape
(Pod specs).
In addition to the queue name, I can specify how many Pods belong to the group.

The pods of a job following a driver-workers paradigm would look like follows:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: job-driver
  labels:
    kueue.x-k8s.io/queue-name: user-queue
    kueue.x-k8s.io/pod-group-name: pod-group
  annotations:
    kueue.x-k8s.io/pod-group-total-count: "3"
spec:
  containers:
  - name: job
    image: driver
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
  annotations:
    kueue.x-k8s.io/pod-group-total-count: "3"
spec:
  containers:
  - name: job
    image: worker
    args: ["--index", "0"]
    resources:
      requests:
        cpu: 1m
        vendor.com/gpu: 1
---
apiVersion: v1
kind: Pod
metadata:
  name: job-worker-1
  labels:
    kueue.x-k8s.io/queue-name: user-queue
    kueue.x-k8s.io/pod-group-name: pod-group
  annotations:
    kueue.x-k8s.io/pod-group-total-count: "3"
spec:
  containers:
  - name: job
    image: worker
    args: ["--index", "1"]
    resources:
      requests:
        cpu: 1m
        vendor.com/gpu: 1
```

### Story 3

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
  annotations:
    # If the template is left empty, it means that it will match the spec of this pod.
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
- The webhook should not add a scheduling gate.
- The pod reconciler should not create a corresponding Workload object.

Note that sometimes the Pods might not be directly owned by a known job object. Here are some
special cases:
- MPIJob: The launcher Pod is created through a batch/Job, which is also an known to Kueue, so
  it's not an issue.
- JobSet: Also creates Jobs, so not problematic.
- RayJob: Pods are owned by a RayCluster object, which we don't currently support. This could be
  hardcoded a known parent, or we could use label selectors for:
  ```yaml
  app.kubernetes.io/created-by: kuberay-operator
  app.kubernetes.io/name: kuberay
  ```

#### Pods replaced on failure

It is possible that users of plain Pods have a controller for them to handle failures and
re-creations. These Pods should be able to use the quota that was already assigned to the Workload.

Because Kueue can't know if Pods will be recreated or not, it will hold the entirety of the
quota until it can determine that the whole Workload finished (all pods are terminated).
In other words, Kueue won't support [dynamically reclaiming quota](https://github.com/kubernetes-sigs/kueue/issues/78)
for plain Pods.

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

#### Limited size for annotation values

[Story 3](#story-3) can be limited by the annotation size limit (256kB across all annotation values).
There isn't much we can do other than documenting the limitation. We can also suggest users to
only list the fields relevant to scheduling, as documented for [Groups of Pods created beforehand](#groups-of-pods-created-beforehand).
- node affinity and selectors
- pod affinity
- tolerations
- topology spread constraints
- container requests
- pod overhead

## Design Details

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

A Pod reconciler will be responsible for removing the `kueue.x-k8s.io/admission` gate. If the Pods
have other gates, they will remain Pending, but would be considered active from Kueue's perspective.

#### Pods subject to queueing

Not all Pods in a cluster should be subject to queueing.
In particular the following pods should be excluded from getting the scheduling gate or label.

1. Pods owned by other job APIs managed by kueue.

They can be identified by the ownerReference, based on the list of enabled integrations.

In some scenarios, users might have custom job objects that own Pods through an indirect object.
In these cases, it might be simpler to identify the pods through a label selector.

2. Pods belonging to specific namespaces (such as kube-system or kueue-system).

The namespaces and pod selectors are defined in Configuration.Integrations.
For a Pod to qualify for queueing by Kueue, it needs to satisfy both the namespace and pod selector.

```golang
type Integrations struct {
  Frameworks []string
  PodOptions *PodIntegrationOptions
}

type PodIntegrationOptions struct {
  NamespaceSelector *metav1.LabelSelector
  PodSelector *metav1.LabelSelector
}
```

When empty, Kueue uses the following NamespaceSelector internally:

```yaml
matchExpressions:
- key: kubernetes.io/metadata.name
  operator: NotIn
  values: [kube-system, kueue-system]
```

### Constructing Workload objects

Once the webhook has marked Pods subject to queuing with the `kueue.x-k8s.io/managed: true` label,
the Pod reconciler can create the corresponding Workload object to feed the Kueue admission logic.

Note that the Workload cannot be owned by the Pod. Otherwise any cascade deletion of the Pod would
delete the Workload object, even before the Pod terminates (if it has a grace period).
This means that the controller needs to manually delete the Workload object once it has the
Finished condition (after we have determined that all pods have finished).

A possible extension here is to add a TTL, which we can consider based on user feedback.

#### Single Pods

The simplest case we want to support is single Pod jobs. These Pods only have the label
`kueue.x-k8s.io/queue-name`, indicating the local queue where they will be queued.

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
    template:
      spec:
        containers:
        - name: job
          image: hello-world
          resources:
            requests:
              cpu: 1m
```

#### Groups of Pods created beforehand

When a group of pods have different shapes, we need to group them into buckets of similar specs in
order to create a Workload object.

To fully identify the group of pods, the pods need the following:
- the label `kueue.x-k8s.io/pod-group-name`, as a unique identifier for the group. This should
  be a valid CRD name.
- The annotation `kueue.x-k8s.io/pod-group-total-count` to indicate how many pods to expect in
  the group.

The Pod reconciler would group the pods into similar buckets by only looking at the fields that are
relevant to admission, scheduling and/or autoscaling.
This list might need to be updated for Kubernetes versions that add new fields relevant to 
scheduling. The list of fields to keep are:
- In `metadata`: `labels` (ignoring labels with the `kueue.x-k8s.io/` prefix)
- In `spec`:
  - In `initContainers` and `containers`: `image`, `requests` and `ports`.
  - `nodeSelector`
  - `affinity`
  - `tolerations`
  - `runtimeClassName`
  - `priority`
  - `preemptionPolicy`
  - `topologySpreadConstraints`
  - `overhead`
  - `volumes`

A sha256 of the reamining Pod spec will be used as a name for a Workload podSet. The count for the
podSet will be the number of Pods that match the same sha256.

We can only build the Workload object once we observe the number of Pods defined by the
`kueue.x-k8s.io/pod-group-total-count` annotation.

If Pods with the same `pod-group-name` have different values for the `pod-group-total-count`
annotation, or if Kueue observes a different amount of Pods than the count, it will not create a
Workload object and it will emit an event for the Pod indicating the reason.

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
  - count: 1
    name: driver 
    template:
      spec:
        containers:
        - name: job
          image: driver
          resources:
            requests:
              cpu: 1m
  - count: 2
    name: worker
    template:
      spec:
        containers:
        - name: job
          image: worker
          resources:
            requests:
              cpu: 1m
              vendor.com/gpu: 1
```

**Caveats:**

If the number of different sha256s obtained from the groups of Pods, Workload creation will fail.
This generally shouldn't be a problem, unless multiple Pods (that should be considered the same
from an admission perspective) have different label values or reference different volume claims.

Based on user feedback, we can consider excluding certaing labels and volumes, or make it
configurable.

#### Groups of pods where driver generates workers

When most Pods of a group are only created after a subset of them start running, users need to
provide the shapes of the following pods before hand.

Users can provide the shapes of the remaining roles in an annotation
`kueue.x-k8s.io/pod-group-sets`, taking a yaml/json with the same structure as the Workload PodSets.
The template for the initial pods can be left empty, as it can be populated by Kueue.

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
    template:
      spec:
        containers:
        - name: job
          image: hello-world
          resources:
            requests:
              cpu: 1m
  - count: 10
    name: worker
    template:
      spec:
        containers:
        - name: job
          image: hello-world
          resources:
            requests:
              cpu: 1m
```

### Tracking admitted and finished Pods

Pods need to have finalizers so that we can reliably track how many of them run to completion and be
able to determine when the Workload is Finished.

A Pod-group reconciler would keep track of the groups of Pods and their respective Workload object,
based on the `jobframework.Reconciler`.
After a Workload is admitted, as the Pod-group reconciler ungates pods, it would keep an in-memory
cache of expected ungated pods: the number of ungated pods that are not reflected in the informers
yet, per Pod-group. This number decreases as the event handler observes Pods transition from being
gated to ungated.

In the Pod event handler, we decrement the counter when we see a transition from having
the scheduling gate `kueue.x-k8s.io/admission` to not having it.

In the Pod-group reconciler:
1. If the Pod is not terminated,
  create a Workload for the pod group if one does not exist.
2. If the Pod is terminated,
   - If the Workloald doesn't exist or the workload is finished, remove the finalizer.
3. ungated_pods_in_client: the number of non-terminated pods in the client that are admitted.
   We only look at non-terminated pods to allow for terminated pods to be replaced.
4. ungated_pods = ungated_pods_in_client + expected_ungated_pods. Note that this might temporarily
   lead to double counting.
2. For gated pods:
  - If ungated_pods < admission.count, remove the gate, set nodeSelector, an increase
    expected_ungated_pods
  - Else,
    - If ungated_pods_in_informer < admission.count, we can't admit this Pod now to prevent
      overbooking, but requeue this Pod for retry.
    - Else, remove finalizer and delete the Pod, as it's beyond the allowed admission.
5. If the number of terminated pods with a finalizer is greater than or equal to the admission
  count, and there are no non-terminated Pods, mark the Workload as Finished and remove the
  finalizers from the Pods.

Note that we are only removing Pod finalizers once the Workload is finished. This is a simple way
of managing finalizers, but it might lead to too many Pods lingering in etcd for a long time after
terminated. In a future version, we can consider a better scheme similar to [Pod tracking in Jobs](https://kubernetes.io/blog/2022/12/29/scalable-job-tracking-ga/).

### Metrics

In addition to the existing metrics for workloads, it could be beneficial to track gated and
unsuspended pods.

- `pods_gated_total`: Tracks the number of pods that get the scheduling gate.
- `pods_ungated_total`: Tracks the number of pods that get the scheduling gate removed.
- `pods_rejected_total`: Tracks the number of pods that were rejected because there was an excess
  number of pods compared to the annotations.

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

The unit coverage of `workload_controller.go` needs significant improvement.

#### Unit Tests

Current coverage of packages that will be affected

- `pkg/controller/jobframework/reconciler.go`: `2023-08-14` - `60.9%`
- `pkg/controller/core/workload_controller.go`: `2023-08-14` - `7%`
- `pkg/metrics`: `2023-08-14` - `97%`
- `main.go`: `2023-08-14` - `16.4%`

#### Integration tests

The integration tests should cover the following scenarios:

- Basic webhook test
- Single Pod queued, admitted and finished.
- Multiple Pods with one shape:
  - queued and admitted
  - failed pods recreated can use the same quota
  - Workload finished when all pods finish (failed or succeeded)
- Driver Pod creates workers:
  - queued and admitted.
  - worker pods beyond the count are rejected (deleted)
  - workload finished when all pods finish
- Preemption deletes all pods for the workload.

### Graduation Criteria

#### Beta

The feature will be first released with a Beta maturity level. The feature will not be guarded by a
feature gate. However, as opposed to the rest of the integrations, it will not be enabled by
default: users have to explicitly enable Pod integration through the configuration API.

#### GA

The feature can graduate to GA after addressing feedback for at least 3 consecutive releases.

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

The proposed labels and annotations for groups of pods can be complex to build manually.
However, we expect that a job dispatcher or client would create the Pods, not end-users directly.

For more complex scenarios, users should consider using a CRD to manage their Pods and integrate
the CRD with Kueue.

## Alternatives

### Users create a Workload object beforehand

An alternative to the multiple annotations in the Pods would be for users to create a Workload
object before creating the Pods. The Pods would just have one annotation referencing the Workload
name.

While this would be a clean approach, this proposal is targetting users that don't have a CRD
wrapping their Pods, and adding one would be a bigger effort than adding annotations. Such amount
of effort could be similar to migrating from plain Pods to the Job API, which is already supported.

We could reconsider this based on user feedback.