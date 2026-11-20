# KEP-16885: In-flight Pod Pacing

<!-- toc -->
- [Summary](#summary)
- [Terminology](#terminology)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: One large Job floods a slow kube-scheduler](#story-1-one-large-job-floods-a-slow-kube-scheduler)
    - [Story 2: Many small Workloads release their Pods together](#story-2-many-small-workloads-release-their-pods-together)
    - [Story 3: A distributed training job must start all its Pods together](#story-3-a-distributed-training-job-must-start-all-its-pods-together)
    - [Story 4: I do not want to run a controller to pace admission](#story-4-i-do-not-want-to-run-a-controller-to-pace-admission)
    - [Story 5: Capacity arrives slowly and thousands of Pods stay pending](#story-5-capacity-arrives-slowly-and-thousands-of-pods-stay-pending)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [A paced Job can be evicted by the readiness timeout](#a-paced-job-can-be-evicted-by-the-readiness-timeout)
    - [Pods that no node can fit hold budget](#pods-that-no-node-can-fit-hold-budget)
    - [A distributed job can be opted in by mistake](#a-distributed-job-can-be-opted-in-by-mistake)
    - [Pacing is skipped when the Job API rejects the gate](#pacing-is-skipped-when-the-job-api-rejects-the-gate)
    - [A low limit slows autoscaler scale-up](#a-low-limit-slows-autoscaler-scale-up)
    - [Held Pods add API server and ResourceQuota load](#held-pods-add-api-server-and-resourcequota-load)
    - [Disabling the feature can leave Pods held](#disabling-the-feature-can-leave-pods-held)
    - [BestEffortFIFO queues stop skipping a blocked head](#besteffortfifo-queues-stop-skipping-a-blocked-head)
  - [Open Questions](#open-questions)
- [Design Details](#design-details)
  - [Overview](#overview)
  - [Feature gate](#feature-gate)
  - [Kueue Configuration API](#kueue-configuration-api)
    - [API](#api)
    - [Validation](#validation)
    - [Handling](#handling)
  - [In-flight accounting](#in-flight-accounting)
  - [Workload-level limit](#workload-level-limit)
  - [Pod-level pacing](#pod-level-pacing)
  - [Interactions with other features](#interactions-with-other-features)
  - [Observability](#observability)
  - [Disabling the feature and version skew](#disabling-the-feature-and-version-skew)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
    - [Performance tests](#performance-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [Stable](#stable)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [An out-of-tree controller using AdmissionGatedBy](#an-out-of-tree-controller-using-admissiongatedby)
  - [A custom AdmissionCheck acting as a semaphore](#a-custom-admissioncheck-acting-as-a-semaphore)
  - [Extending blockAdmission](#extending-blockadmission)
  - [Lowering quota](#lowering-quota)
  - [A time-based admission rate limiter](#a-time-based-admission-rate-limiter)
  - [Per-ClusterQueue limit first](#per-clusterqueue-limit-first)
  - [Reusing the elastic job scheduling gate](#reusing-the-elastic-job-scheduling-gate)
- [Future Work](#future-work)
  - [Isolation between tenants](#isolation-between-tenants)
  - [Other follow-ups](#other-follow-ups)
<!-- /toc -->

## Summary

Add an optional cap on the number of in-flight Pods: Pods of admitted Workloads that are released to kube-scheduler but not yet bound to a node.
Kueue holds back Pods, or whole Workloads, so that the release rate follows the rate at which kube-scheduler can bind.
This protects a throughput-limited kube-scheduler from being flooded by a single admission.

## Terminology

- **In-flight Pod**: a Pod of an admitted Workload that is not held by Kueue, is not bound to a node, is not terminal and is not being deleted.
  The word "in-flight" is also used inside Kueue for queue entries that Kueue's scheduler has checked out.
  This KEP always says "in-flight Pod" for the new meaning.
- **In-flight limit**: the configured maximum number of in-flight Pods.
- **Kueue's scheduler**: the admission loop in the Kueue controller manager. It decides which Workloads get quota.
- **kube-scheduler**: the Kubernetes scheduler that binds Pods to nodes. A custom scheduler can take its place.
  "Scheduler" is never used alone in this KEP.
- **Gang Workload**: a Workload whose Pods must start together, such as a distributed training job.
  Kueue never releases such a Workload in pieces.
- **Paced Workload**: a non-gang Workload whose Job opted in to pacing, and whose Pods Kueue releases in batches after admission.
- **Held Pod**: a Pod of a paced Workload that stays behind the pacing scheduling gate.

## Motivation

Kueue admits a Workload when it fits the quota of its ClusterQueue.
It does not consider whether kube-scheduler can place the Pods quickly.
When quota is large, one admission can release hundreds or thousands of Pods at once.
A kube-scheduler that binds fewer Pods per second than Kueue releases then holds a large backlog of admitted but unscheduled Pods for a long time.
This happens whenever Kueue releases Pods faster than they can be bound.
Typical causes are:

- A large cluster, where kube-scheduler checks each Pod against many nodes.
- Pods with many scheduling constraints, such as node pool selectors and affinities, which make each placement more expensive.
- A scheduler shared by many tenants, so one tenant's burst delays the others.
- A slow or heavily customized scheduler.
- Nodes that arrive slowly through autoscaling.

The backlog keeps kube-scheduler busy, delays Pods of other queues that are within their own quota, and keeps the cluster autoscaler working on Pods that cannot be placed yet.

Existing mechanisms do not limit the release rate:

- Quota caps total concurrency, not how fast Pods are released.
  Lowering quota to N also lowers steady-state throughput to N.
- `waitForPodsReady` with `blockAdmission` admits one Workload at a time cluster-wide.
  It releases the whole Pod set of a Workload at once and stops all of Kueue's scheduler while it waits.
- `unscheduledTimeout` ([KEP-13502](../13502-unscheduled-pods-timeout/README.md)) evicts a Workload after a timeout.
  The requeued Workload releases all its Pods again and floods kube-scheduler again.
- The ProvisioningRequest AdmissionCheck needs a cluster autoscaler that handles ProvisioningRequests.
- An out-of-tree controller that uses AdmissionGatedBy ([KEP-6915](../6915-scheduling-gated-by-annotation/README.md)) works as a soft cap today.
  It works per Workload, so it cannot pace a single Workload with thousands of Pods.
  It also cannot isolate tenants, and every operator has to run and maintain it.

### Goals

- Limit the number of in-flight Pods with one global setting. Elastic Workloads are excluded in Alpha.
- Pace the Pods of a single large Workload after admission, when its Job opts in, so that one Workload cannot flood kube-scheduler.
- Keep gang Workloads together, and delay their admission while the limit is reached.
- Admit a Workload that is larger than the limit once nothing is in flight, and keep later Workloads of its own queue from overtaking it.
- Expose how many Pods are in flight and how many are held.

### Non-Goals

- Limiting the rate at which Workloads are admitted by time.
- Replacing kube-scheduler queue tuning or API Priority and Fairness.
- Limits per ClusterQueue or per Cohort in Alpha, and with them isolation between tenants (see [Isolation between tenants](#isolation-between-tenants)).
- Pod-level pacing for integrations other than `batch/v1` Job in Alpha.
- Limiting elastic Workload admissions and scale-ups in Alpha. Their unbound Pods still count as in flight.
- Changing quota accounting. A paced Workload keeps the quota of all its Pods from admission.
- Supporting MultiKueue in Alpha. Each worker cluster can apply its own limit.

## Proposal

Add an optional global in-flight limit to the Kueue Configuration.
Kueue counts in-flight Pods across all admitted Workloads.
The limit is applied in two ways, chosen by the Workload type:

- A gang Workload is admitted if its Pods fit under the limit together with the Pods already in flight.
  A gang Workload larger than the limit is admitted only when no Pod is in flight.
  Otherwise Kueue's scheduler skips it for the cycle and retries when the count drops.
- A paced Workload is admitted on quota alone.
  Kueue keeps its Pods behind a scheduling gate and removes the gate from batches of Pods as the in-flight count drops.

Kueue builds on the Pod tracking introduced by [KEP-13502](../13502-unscheduled-pods-timeout/README.md).
That tracking is enabled whenever the limit is set, not only when `unscheduledTimeout` is set.

### User Stories

Stories 2 to 5 use the configuration of Story 1, including the `MaxInFlightPods` feature gate and the `waitForPodsReady` timeout.

#### Story 1: One large Job floods a slow kube-scheduler

As a cluster operator, my kube-scheduler binds far fewer Pods per second than Kueue can release in one admission.
For example, it binds a few Pods per second, and a user submits a Job with 10,000 Pods that fits the quota.
Kueue admits it and 10,000 Pods become schedulable at once.
The backlog takes kube-scheduler a long time to drain, which delays everything else.

I want Kueue to release the Pods of that Job in batches, so that no more than the limit are waiting for kube-scheduler at a time, while their lease has not expired.
The Pods of this Job can start independently, so I opt the Job in to pacing with an annotation.

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
featureGates:
  MaxInFlightPods: true
inFlightPods:
  maxPods: 200
waitForPodsReady:
  timeout: 1h
```

The `waitForPodsReady` timeout is enabled by default and is 30 minutes.
This configuration raises it to 1 hour, because a paced Job can take longer than 30 minutes to start all its Pods.

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: large-job
  labels:
    kueue.x-k8s.io/queue-name: team-a
  annotations:
    kueue.x-k8s.io/pod-pacing: "true"
spec:
  parallelism: 10000
  completions: 10000
  suspend: true
  template:
    spec:
      containers:
        - name: worker
          image: registry.example/worker:latest
          resources:
            requests:
              cpu: "1"
      restartPolicy: Never
```

#### Story 2: Many small Workloads release their Pods together

As a cluster operator, I run many single-Pod Workloads from several queues.
Each Workload fits its own quota, so Kueue admits hundreds of them in a few cycles.
Their Pods flood kube-scheduler together, although each Workload is small.

I want Kueue to admit new Workloads only while the number of in-flight Pods is under the limit.
Admission resumes as Pods get bound.

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: small-job
  labels:
    kueue.x-k8s.io/queue-name: team-b
spec:
  parallelism: 1
  completions: 1
  suspend: true
  template:
    spec:
      containers:
        - name: worker
          image: registry.example/worker:latest
          resources:
            requests:
              cpu: "1"
      restartPolicy: Never
```

#### Story 3: A distributed training job must start all its Pods together

As an ML engineer, my training job needs all its Pods to start together.
If only some Pods start, they wait for the rest and waste resources.

I want Kueue never to release only part of this job.
When the limit is reached, I want the whole job to wait for admission.
A job that is larger than the limit must still run once nothing else is in flight.

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: training
  labels:
    kueue.x-k8s.io/queue-name: ml
spec:
  completionMode: Indexed
  parallelism: 64
  completions: 64
  suspend: true
  template:
    spec:
      containers:
        - name: worker
          image: registry.example/worker:latest
          resources:
            requests:
              cpu: "1"
      restartPolicy: Never
```

The Job does not carry the pacing annotation, so Kueue treats it as a gang Workload.

#### Story 4: I do not want to run a controller to pace admission

As a platform engineer, I protect kube-scheduler today with an out-of-tree controller.
Jobs are created with the AdmissionGatedBy annotation.
The controller counts unscheduled Pods and removes the annotation from Jobs while the count is low.

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: training
  labels:
    kueue.x-k8s.io/queue-name: team-a
  annotations:
    kueue.x-k8s.io/admission-gated-by: example.com/pod-semaphore
spec:
  parallelism: 64
  completions: 64
  suspend: true
  template:
    spec:
      containers:
        - name: worker
          image: registry.example/worker:latest
          resources:
            requests:
              cpu: "1"
      restartPolicy: Never
```

That controller is another component to deploy, upgrade and keep highly available.
If every feature like this needs its own controller, running Kueue in production means running many components.
It needs cluster-wide Pod watch permissions, and its counting must stay compatible with each Kueue version.
It gates Jobs before they are queued, so it cannot respect Kueue's ordering and fair sharing.
It releases a Job as a whole, so it cannot pace the Pods of one large Job.

I want Kueue to provide the same protection, so that I can delete the controller.
I set one limit in the Kueue configuration, and the Jobs that can start Pods independently carry the pacing annotation.

#### Story 5: Capacity arrives slowly and thousands of Pods stay pending

As an operator who runs on spot instances, nodes for a large Job arrive over several minutes, depending on availability in the region or zone.
Most Pods of a newly admitted Job cannot be placed yet, so thousands of them stay pending.
The kube-scheduler keeps retrying them, and the cluster autoscaler keeps evaluating them for the whole wait.

I want the number of pending Pods to stay bounded by the in-flight limit while their lease has not expired, so that kube-scheduler and autoscaler work does not grow with the size of the Job.
I accept that the cluster then grows in steps of about the limit, because the autoscaler only sees the Pods that Kueue has released.
I do not want the Job to be evicted and requeued while it waits, because it would release all its Pods again.
The Pods of this Job can start independently, so I opt the Job in to pacing with the `kueue.x-k8s.io/pod-pacing: "true"` annotation.

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: spot-job
  labels:
    kueue.x-k8s.io/queue-name: team-c
  annotations:
    kueue.x-k8s.io/pod-pacing: "true"
spec:
  parallelism: 5000
  completions: 5000
  suspend: true
  template:
    spec:
      containers:
        - name: worker
          image: registry.example/worker:latest
          resources:
            requests:
              cpu: "1"
      restartPolicy: Never
```

### Notes/Constraints/Caveats

- The limit is a pacing target.
  A gang Workload larger than the limit is released at once when no Pod is in flight, so the in-flight count can exceed the limit in that case.
- A paced Workload holds the quota of all its Pods from admission, including Pods that are still held.
- A Job that does not opt in is treated as a gang Workload.
  The Workload-level limit applies to it, but once admitted, all its Pods are released at once.
  A large Job that must not flood kube-scheduler therefore has to opt in to pacing.
- Pods held by Kueue do not count as in flight.
  The count only includes Pods that kube-scheduler can already see.
- Kueue counts the Pods of a gang Workload from admission, before the Pods exist.
  This prevents several Workloads admitted in the same cycle from exceeding the limit.
- The limit applies to the Pods of this Kueue instance.
- The limit works with quota-only clusters and with topology-aware scheduling.
  With topology-aware scheduling, Kueue assigns the nodes, but kube-scheduler still binds each Pod.
  Such Workloads are gang Workloads, so the Workload-level limit applies to them and their Pods count as in flight.

### Risks and Mitigations

#### A paced Job can be evicted by the readiness timeout

`waitForPodsReady` is enabled by default, and its timeout starts when the Workload is admitted.
A large Job that is paced on purpose can take longer than that timeout to start all its Pods.
The Job is then evicted and requeued, and it releases its Pods again.

In Alpha, the documentation tells administrators to set the timeout above the longest expected ramp.
This KEP amends KEP-13502: a Pod held by the pacing gate is not treated as unscheduled for `unscheduledTimeout`, so the timeout does not evict a Workload that Kueue is pacing on purpose.
Pods held by other Kueue gates stay unscheduled, as KEP-13502 defines.
A pacing-aware deadline is a Beta requirement (see [Graduation Criteria](#graduation-criteria)).

#### Pods that no node can fit hold budget

A Pod that no node can fit stays unbound, so it counts as in flight and uses budget.
The limit is global, so enough such Pods affect every queue, not only the queue of the Workload that owns them.
While they use up the budget, Kueue's scheduler skips every gang Workload that needs budget, and paced Workloads release no more Pods.
A skipped Workload stays at the head of its ClusterQueue, so the Workloads behind it in that queue wait as well.
This lasts until the stuck Pods get bound or deleted, or until their lease expires.
After the lease expires, those Pods stop counting and their share resets, so new Workloads can release Pods while the old Pods are still unbound.
Repeated expiry lets the number of unbound Pods exceed the limit.
The lease trades a hard bound for progress, and the metric and the alert below make it visible.

Mitigations:

- A single Workload counts for at most half of the limit.
  With a limit of 2 or more, a Workload whose Pods cannot be placed cannot use the whole budget, so other Workloads can still make progress.
  With a limit of 1, the share is one Pod, so one stuck Pod can use the whole budget until it binds or its lease expires.
- Kueue stops counting a Pod after it has been in flight for a fixed lease period.
  A metric reports how often this happens.
- Administrators can set `unscheduledTimeout` so that Workloads that never fit are evicted and release their budget.
- Administrators can alert when `kueue_inflight_pods` stays equal to `kueue_inflight_pods_limit` for longer than the time they expect Pods to take to bind.

Excluding Pods that kube-scheduler marked unschedulable is not done in Alpha, because it would release more Pods when the cluster is full, which is the failure this KEP exists to prevent.

#### A distributed job can be opted in by mistake

Pacing a job that needs all its Pods together breaks it.
Pacing is opt-in, so only a user who sets the annotation on a Job can cause this.
In Alpha, Kueue paces only a `batch/v1` Job with a single PodSet.
Jobs managed by topology-aware scheduling, ProvisioningRequest, elastic scaling or MultiKueue are never paced, even when annotated.
The documentation states that the annotation is for Jobs whose Pods can start independently.

#### Pacing is skipped when the Job API rejects the gate

Some Kubernetes versions and configurations do not allow Kueue to add the pacing gate to a suspended Job.
Kueue then starts the Job without pacing, and its Pods are released at once.
This keeps the Job running and never blocks it, but the Job loses the protection.
The event on the Job and the `kueue_pacing_skipped_total` metric make it visible, and the documentation states the supported Kubernetes versions.

#### A low limit slows autoscaler scale-up

The cluster autoscaler ignores Pods that are held behind a scheduling gate.
A low limit therefore makes scale-up proceed in steps of the limit.

The documentation recommends a limit that is not smaller than a typical scale-up step.

#### Held Pods add API server and ResourceQuota load

The Pods of a paced Job exist from the start and count toward namespace object-count and resource quotas.
Every release is one API request.

The documentation lists this cost.
Releases are sent in parallel batches.

#### Disabling the feature can leave Pods held

Pods of a paced Job that still carry the pacing gate would stay held forever if Kueue stopped releasing them.

Kueue keeps releasing existing held Pods when the feature is disabled or the limit is removed.
See [Disabling the feature and version skew](#disabling-the-feature-and-version-skew).

#### BestEffortFIFO queues stop skipping a blocked head

While a gang Workload is blocked by the limit, Kueue keeps it at the head of its ClusterQueue.
Without that, a stream of small Workloads would starve it.
A BestEffortFIFO queue therefore behaves like StrictFIFO until the head is admitted.
The block ends when Pods get bound, which normally takes seconds to minutes.

### Open Questions

- Is keeping a blocked gang Workload at the head acceptable for BestEffortFIFO queues?
- Is a `batch/v1` Job-only scope for pod-level pacing acceptable in Alpha?
- Should an administrator be able to enable pacing for all eligible Jobs, instead of requiring the annotation on each Job, and in which release?
- Should the lease for stuck Pods be configurable, what should its default be, and should it be longer than the typical time for new nodes to arrive?
- Is half of the limit the right maximum share for a single Workload, and should it be configurable?
- Should the readiness timeout be documentation-only in Alpha, or should Alpha already pause the timer for held Pods?
- Should pod-level pacing have its own feature gate so that it can graduate separately?
- Which Kubernetes versions, and which Job feature gate, allow changing the scheduling gates of a suspended Job's Pod template when the Job starts, including a Job that was already started once and is re-admitted?

## Design Details

### Overview

```
Workload admitted by quota
        |
        +-- gang Workload ----> in-flight Pods + Workload Pods > limit ?
        |                          yes: skipped for the cycle, retried when the count drops
        |                          no (or nothing in flight): Pods released together
        |
        +-- paced Workload ---> Pods created behind the pacing gate
                                   |
                                   v
                      Pacing controller removes the gate from the oldest
                      held Pods, up to (limit - in-flight Pods)
```

### Feature gate

The feature is behind the `MaxInFlightPods` feature gate, which is disabled by default in Alpha.
When the gate is disabled, the configuration field is rejected and Kueue behaves as before.

### Kueue Configuration API

#### API

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
featureGates:
  MaxInFlightPods: true
inFlightPods:
  maxPods: 200
```

- `inFlightPods` is an optional struct. When it is not set, the feature is off.
- `maxPods` is the maximum number of in-flight Pods.
- The field is grouped in a struct so that per-ClusterQueue limits and lease settings can be added later without renaming it.
- Kueue reads the Configuration when it starts, so changing `inFlightPods` or the feature gate requires restarting the Kueue controller manager.

#### Validation

- Setting `inFlightPods` while the `MaxInFlightPods` feature gate is disabled is rejected, and Kueue does not start.
- `maxPods` must be at least 1. Zero and negative values are rejected.
- `inFlightPods` does not require `waitForPodsReady.unscheduledTimeout`, and it can be combined with `blockAdmission`.

#### Handling

- When `inFlightPods` is not set, Kueue behaves as it does without this feature.
- To turn the feature off, remove `inFlightPods` and disable the feature gate in the same restart.
  Held Pods are still released, see [Disabling the feature and version skew](#disabling-the-feature-and-version-skew).
- A gang Workload that is skipped because of the limit shows `QuotaReserved=False` with the reason `WaitingForInFlightCapacity`, as described in [Workload-level limit](#workload-level-limit).

### In-flight accounting

Kueue keeps one global in-flight count in memory.
It is computed from the Pods of admitted Workloads, using the Pod tracking introduced by KEP-13502.
That tracking runs when either `unscheduledTimeout` or the in-flight limit is set.
The `PodsScheduled` condition is still written only when `unscheduledTimeout` is set.

A Pod counts as in flight when all of the following hold:

- It belongs to an admitted Workload that is not finished and not evicted.
- It is not behind the pacing gate.
- It is not bound to a node, or kube-scheduler has not yet reported it as scheduled.
- It is not terminal and not being deleted.

Counting rules:

- The contribution of a gang Workload is its unbound Pods plus the Pods of its admitted count that do not exist yet.
  It counts from admission, so Pods that the Job creates gradually stay counted until they exist and are bound.
  The contribution is removed when the Workload is evicted, finished or deleted, or when the admission write fails.
- The number of Pods uses the admitted count, which can be smaller than the requested count with partial admission.
- Replacement Pods created after the first scheduling are counted like any other Pod.
- A single Workload counts for at most half of the limit, rounded up, and at least one Pod.
  With a limit of 2 or more, this keeps one Workload with stuck Pods from using the whole budget.
  With a limit of 1, the share is one Pod and a single stuck Pod can block all releases until it binds or its lease expires.
  The admission check in [Workload-level limit](#workload-level-limit) uses the capped count.
  With a limit of 200, a gang Workload of 1,000 Pods counts as 100, so a second gang Workload of 100 Pods can be admitted, and about 1,100 Pods can be unbound.
  The limit is therefore a pacing target for gang Workloads, not a hard bound.
- A Pod stops counting after it has been in flight for the lease period (10 minutes in Alpha).
  The part of an admitted count that has no Pod yet follows the same rule, measured from admission.
  A Job that never creates its Pods therefore holds budget for at most the lease period, with or without `unscheduledTimeout`.
- Workloads of a ClusterQueue with a MultiKueue admission check, and Workloads dispatched to a worker cluster, are not counted on the management cluster.
- When Kueue starts or a new leader is elected, the count is rebuilt from the admitted Workloads and the Pods that Kueue observes, with the rules above.
  For each gang Workload this adds only the part of its admitted count that has no Pod yet, so observed Pods are not counted twice, and the per-Workload share still applies.
  Until the count is rebuilt, gang Workloads that need budget are not admitted and held Pods are not released, so a failover cannot release Pods beyond the limit.
  The rebuild needs the same Pod cache that Kueue has to sync before it admits any Workload, so it has no separate failure mode.

### Workload-level limit

This rule applies to gang Workloads.

1. Before reserving quota, Kueue's scheduler reads the current in-flight count.
2. If the count is above zero and the count plus the Pods of the Workload exceeds the limit, Kueue's scheduler skips the Workload for this cycle.
   It does not reserve quota for it.
3. If nothing is in flight, the Workload is admitted regardless of its size, so a Workload larger than the limit can run.
4. After a Workload is skipped, Kueue's scheduler skips every later Workload in the same cycle that needs budget, so a smaller Workload from another queue cannot overtake it.

A skipped Workload is not requeued immediately, because that would spin Kueue's scheduler.
It becomes eligible again when the in-flight count drops, including when a lease expires, which wakes the blocked queues.

A skipped Workload stays at the head of its ClusterQueue until it is admitted, so Workloads behind it cannot take its place.
This applies to StrictFIFO and BestEffortFIFO queues.

Alpha gives no guarantee across ClusterQueues.
A Workload from a queue that Kueue's scheduler considers earlier can take the budget first, so a stream of smaller Workloads from other queues can delay a Workload that is larger than the limit.
Elastic Workloads can also use budget without passing the skip rule above.
The lease bounds how long Pods that cannot be placed hold the budget.

The budget is one pool for all ClusterQueues.
Kueue's scheduler takes from it in the order in which it considers Workloads in each cycle.
No ClusterQueue has a reserved share in Alpha, and the per-Workload share above only keeps one Workload from using all of it.
Reserved shares per ClusterQueue or Cohort are described in [Isolation between tenants](#isolation-between-tenants).

The Workload shows `QuotaReserved=False` with the reason `WaitingForInFlightCapacity`, and an event with the same reason, rate limited so that repeated skips do not flood the event stream.

### Pod-level pacing

A Workload is paced only when the user opts in and all of the following hold:

- The Job carries the annotation `kueue.x-k8s.io/pod-pacing: "true"`.
- It is a `batch/v1` Job with a single PodSet (Alpha scope).
- It does not use topology-aware scheduling, a ProvisioningRequest, elastic scaling or MultiKueue.

If the annotation is set on a Job that is not eligible, Kueue does not pace it and records an event that gives the reason.

The annotation is immutable in Alpha.
The Job webhook rejects adding, changing or removing it on an existing Job.
A user who needs a different mode creates a new Job, so the Pods, quota and metrics of a running Job never switch between the gang and paced paths.
The webhook applies the same rule when the feature gate is disabled, so a Job keeps its mode across restarts.

All other Workloads are gang Workloads, which keeps today's behavior for every existing Job.
Pacing is opt-in so that enabling the limit never splits a job that needs all its Pods together.

When a paced Job starts, Kueue adds the scheduling gate `kueue.x-k8s.io/pacing` to the Pod template, in the same way it adds other Kueue gates when a job starts.
Kueue removes the gate again when the Job is stopped.

This write changes the scheduling directives of a suspended Job in the same update that unsuspends it.
The Kubernetes Job API allows that only in some versions and configurations, and a Job that was already started once is the sensitive case, such as a Job that is re-admitted after an eviction.
If the API rejects the write because it does not allow the change, Kueue does not retry it.
It starts the Job without the pacing gate, as it would start a gang Workload, records an event that says pacing was skipped and why, and counts it in a metric.
The Job runs, but its Pods are released at once, and they count as in flight once they exist.
Any other start error is retried as for every Job, and the Workload keeps its quota while it retries.
The `waitForPodsReady` timeout, when enabled, evicts a Workload that never starts, which releases the quota.
The minimum Kubernetes version and the Job feature gate that Alpha depends on are confirmed and documented before Alpha (see [Open Questions](#open-questions)).

A pacing controller removes the gate from held Pods:

- It computes the budget as `limit - in-flight Pods`.
- It releases held Pods of the oldest admitted Workloads first, up to the budget.
  It releases Pods in batches of at most the budget.
- It reserves the budget before it releases the Pods, and returns the reservation if a release fails, so that concurrent releases cannot exceed the limit.
  The budget of a release that succeeded stays reserved until the Pod is observed, or until the lease period ends, so Pods that are not observed yet are never released twice.
  The budget of a release with an unknown result, such as a timeout, stays reserved until the worker reads the Pods and confirms whether the gate was removed.
  It then counts the Pods that were released and returns the budget of the rest. The lease period bounds this wait.
- It runs as a single global worker, because the budget is global.
- It runs on the leader only.
- It reacts to Pods being bound, finished or deleted, to a Pod with the pacing gate being created, to Workloads being admitted, evicted or finished, and to a lease expiring.
  A Workload can be admitted before the Job creates its Pods, so the creation of a held Pod must start a release attempt.
  It also runs a periodic resync as a safety net, so a missed event cannot leave Pods held.

Pods of a Job that is evicted are deleted together with the Job's other Pods, including held Pods.
Held Pods do not count as in flight.

### Interactions with other features

| Feature | Behavior |
|---|---|
| Quota and preemption | Quota is accounted at admission, as before. Held Pods keep their quota. Preempted or evicted Workloads release their in-flight count. |
| `waitForPodsReady` timeout | The timer is not paused in Alpha. Held Pods are not reported as unscheduled (this amends KEP-13502 for the pacing gate). Administrators must set the timeout above the longest ramp. A per-Workload timeout can be set with the existing annotation. |
| `blockAdmission` | Both can be enabled. `blockAdmission` already admits one Workload at a time, so the limit is mostly redundant. Refill is disabled with `blockAdmission` as before. |
| `unscheduledTimeout` ([KEP-13502](../13502-unscheduled-pods-timeout/README.md)) | Pod tracking runs when either feature needs it. The `PodsScheduled` condition keeps its meaning. This KEP amends KEP-13502: Pods held by the pacing gate do not start the unscheduled timer, and Pods held by other Kueue gates remain unscheduled. |
| Topology-aware scheduling | Workloads with a topology assignment are gang Workloads and are never paced. Their Pods count as in flight. |
| ProvisioningRequest | Workloads with a ProvisioningRequest check are never paced, because delayed Pods can lose the booked capacity. The Workload-level limit still applies. |
| Elastic jobs (workload slices) | Never paced and never blocked by the Workload-level limit in Alpha. Their unbound Pods count as in flight. |
| MultiKueue | Not counted on the management cluster. Each worker cluster applies its own limit. |
| FairSharing and AdmissionFairSharing | Skipped Workloads reserve no quota and incur no penalty. The limit is checked for entries of both iterators and for refilled entries. |
| AdmissionGatedBy ([KEP-6915](../6915-scheduling-gated-by-annotation/README.md)) | Gated Workloads are not admitted, so they are not in flight. No conflict. |
| Partial admission | The admitted Pod count is used. |
| Concurrent admission | Only the parent Workload is counted. Variants are ignored. |
| Plain Pods, Deployments, StatefulSets, pod groups | Each Pod or group is a gang Workload and is subject to the Workload-level limit only. |

### Observability

- Gauge `kueue_inflight_pods`: current number of in-flight Pods, including reserved Pods.
- Gauge `kueue_inflight_pods_limit`: the configured limit.
  Alert when `kueue_inflight_pods` stays at the limit for longer than the expected time to bind.
- Gauge `kueue_pacing_held_pods`: Pods behind the pacing gate.
- Gauge `kueue_pacing_held_workloads`: Workloads with at least one held Pod.
- Gauge `kueue_inflight_blocked_workloads{cluster_queue}`: Workloads skipped in the last cycle because of the limit.
- Counter `kueue_inflight_pods_lease_expired_total`: Pods, including Pods that were never created, that stopped counting because their lease expired.
- Counter `kueue_pacing_skipped_total`: paced Jobs that started without the pacing gate because the Job API rejected it.
- The existing gate-removal histogram records the time Pods spend behind the pacing gate, labeled with the pacing gate name.

The metrics use the same label conventions as existing metrics.
They carry no Workload or Pod names.
The per-ClusterQueue gauge uses only the `cluster_queue` label.

### Disabling the feature and version skew

- When the feature gate is disabled or `inFlightPods` is removed, Kueue stops adding the pacing gate to new Jobs.
  The pacing controller runs whenever Pods that carry the pacing gate exist, whether or not the feature gate is enabled.
  It releases those Pods without a limit until none are left, so the gate and `inFlightPods` can be removed in the same restart.
- A Kueue version that does not know the pacing gate leaves such Pods held.
  Before downgrading, administrators must wait for held Pods to drain or delete the affected Jobs.
  The documentation describes this.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Prerequisite testing updates

Extend the existing tracking tests so that they cover tracking enabled by the limit alone.
Keep existing `unscheduledTimeout` expectations unchanged.

#### Unit tests

- Configuration: defaults and validation for unset, zero, negative and valid `maxPods`, with the gate disabled, and with `blockAdmission` and `unscheduledTimeout` set or unset.
- In-flight accounting: gated, released-unbound, bound, terminal and deleting Pods, replacement Pods, partial admission counts, evicted, finished and deleted Workloads, concurrent-admission variants, MultiKueue exclusion, Pods not yet created staying counted, the per-Workload share, lease expiry, and rebuild after restart without admitting gang Workloads or releasing held Pods before the count is ready.
- Workload-level limit: skip when over the limit, admit at zero in-flight, reservation across several Workloads in one cycle, the barrier, second-pass exemption, StrictFIFO and BestEffortFIFO behavior, refill, no quota reserved on skip, and release after a failed admission write.
- Pod-level pacing: the pacing gate is added only to Jobs that opted in and are eligible, the event for an annotated but ineligible Job, removal of the gate on stop, oldest-first release, batch size equal to the budget, concurrent reservations, release failure, deleted Pod, the Job API rejecting the gate (the Job starts unpaced with an event and the metric), and release after the feature is disabled.

#### Integration tests

- A Job that opts in to pacing and is larger than the limit runs to completion; while the lease has not expired, the number of released, unbound Pods never exceeds the limit, and only the Job's first batch is released at start.
- Pods that stay unbound past the lease period stop counting, and further Pods are released. The test advances the clock instead of waiting 10 minutes.
- A gang Workload whose Job never creates its Pods stops holding budget after the lease period, and a blocked gang Workload is then admitted.
- The webhook rejects adding, changing or removing the pacing annotation on an existing Job.
- Evicting a paced Job deletes its Pods and returns the in-flight count to zero.
- Pods that the Job creates after its Workload is admitted are released without any other event.
- A Workload with Pods that cannot be placed uses at most its share of the budget, and other Workloads are still admitted.
- A gang Workload is skipped and then admitted when Pods are bound or deleted.
- A Workload larger than the limit is admitted when nothing is in flight.
- A blocked larger Workload is not overtaken by later Workloads of its own queue, in either queueing strategy.
- Topology-aware, ProvisioningRequest and elastic Workloads are not paced.
- A Job whose pacing gate is rejected by the Job API starts unpaced, and the Workload does not hold its quota while waiting.
- The `waitForPodsReady` timeout and `unscheduledTimeout` behave as documented for paced Jobs.
- Disabling the feature releases held Pods.

#### e2e tests

1. Configure a small limit and create a Job that opts in to pacing, with many more Pods than the limit, on a cluster with kube-scheduler.
2. Verify that the in-flight gauge stays at or below the limit, and that all Pods eventually run.
3. Create a Job whose Pods no node can fit, and a second small Job.
   Verify that the first Job holds at most its share of the budget, that the second Job still runs, and that the budget is freed when the lease expires or `unscheduledTimeout` evicts the first Job.
4. Restart Kueue during pacing and verify that the count is rebuilt without releasing more than the limit.

These tests run in the sequential suite, because they change the Kueue configuration.

#### Performance tests

Add a scenario to the Kueue scheduler performance tests with many single-Pod Workloads and the limit set.
Verify that the scheduling cycle time does not regress.

### Graduation Criteria

#### Alpha

- The `MaxInFlightPods` feature gate is disabled by default.
- The feature is documented with examples, including the readiness timeout and autoscaler guidance.
- Unit, integration and e2e tests are added.

#### Beta

- The feature gate is enabled by default.
- All reported bugs are fixed.
- The `waitForPodsReady` deadline accounts for deliberate holds.
- Pod-level pacing extends beyond `batch/v1` Job through an explicit opt-in for each integration.
- Decide whether eligible Jobs can be paced by default, for example through an administrator setting.
- Replacement-Pod tracking, the stuck-Pod rule and the per-Workload share are re-evaluated with production feedback.
- Measured kube-scheduler and API server impact of many held Pods is documented.
- Leader failover is verified not to over-release.

#### Stable

- The feature gate is locked.
- Per-ClusterQueue and per-Cohort limits are decided in a separate KEP.
- All reported bugs are fixed.

## Implementation History

- 2026-10-30: Initial KEP draft

## Drawbacks

- Pacing adds latency to the start of a large Job, by design.
- Gang Workloads can wait for Pods of other Workloads to be bound.
- The in-flight count is approximate. It can briefly overcount, which is safe, and it relies on Pod events.
- Held Pods exist in the API server and count toward namespace quotas.

## Alternatives

### An out-of-tree controller using AdmissionGatedBy

A controller counts in-flight Pods and removes the AdmissionGatedBy annotation from Jobs in batches.
It needs no change to Kueue and works today.
It gates a Job before it is queued, so the controller must reimplement ordering, priority and fair sharing.
A Job is released whole, so it cannot pace a single large Job.
Every operator has to run it with cluster-wide Pod watch permissions and keep its accounting in sync with Kueue.
The gaps are structural.
Only Kueue knows how many Pods of an admitted Workload do not exist yet, so only Kueue can count them from admission.
Only Kueue's scheduler can skip a Workload for a cycle and keep its place in the queue, and only Kueue can wake the blocked queues when the count drops.
A Job that needs custom pacing logic can still use this option, so the proposal does not remove it.

### A custom AdmissionCheck acting as a semaphore

The check becomes ready only while budget is available.
The Workload holds its quota while it waits, which lowers utilization and blocks lending.
A check cannot add scheduling gates to Pods, so it cannot pace Pods inside a Workload.

### Extending blockAdmission

`blockAdmission` waits for all admitted Workloads to be ready and stops all of Kueue's scheduler while it waits.
It is keyed on readiness, not on scheduling, and releases a whole Workload at once.
Maintainers asked not to extend it.

### Lowering quota

This caps running Pods, not the release rate, and wastes capacity.

### A time-based admission rate limiter

A fixed rate does not follow the real backlog of kube-scheduler.
It also does not limit the Pods released by one large Workload.

### Per-ClusterQueue limit first

The scarce resource is kube-scheduler, which is shared by all queues, so a global limit matches the bottleneck.
A per-ClusterQueue limit needs Cohort semantics and a decision about metric labels, so it is a follow-up.

### Reusing the elastic job scheduling gate

That gate is added before admission, only for elastic Workloads, and its controller releases Pods up to the admitted count.
Pacing has a different meaning: wait for budget after admission.
A separate gate keeps the two features independent.

## Future Work

### Isolation between tenants

The Alpha limit is global.
It protects kube-scheduler, but it does not divide the budget between tenants.
One ClusterQueue that releases many Pods can use most of the budget and delay the others, even when all of them are within their quota.
Quota and FairSharing act at admission, so they do not prevent this.

Story: as an operator of a shared cluster, a team that submits many large Jobs fills kube-scheduler, and the Pods of other teams wait longer, although each team is within its own quota.
I want each ClusterQueue, or a Cohort of queues, to be able to use only a share of the in-flight budget, so that one team cannot slow down the others.

This needs per-ClusterQueue and per-Cohort limits.
It also needs decisions about borrowing between queues, how the shares interact with FairSharing, and which labels the new metrics may carry.
It builds on the global accounting and the pacing controller of this KEP, and is proposed as a separate KEP once the global limit has production feedback.
The barrier across queues, the per-Workload share and the release order are not part of the Alpha contract, and may change when per-ClusterQueue and per-Cohort limits are added.
The global limit remains an overall ceiling.

### Other follow-ups

- Pod-level pacing for more integrations, such as JobSet replicated jobs that are marked independent.
- A `waitForPodsReady` deadline that pauses while Pods are held on purpose.
- A configurable lease for stuck Pods, or excluding Pods that kube-scheduler marked unschedulable.
- Applying the budget to elastic job scale-ups.
- MultiKueue support, with the limit enforced on worker clusters.
