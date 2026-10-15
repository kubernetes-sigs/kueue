# KEP-14615: Elastic Workloads with ProvisioningRequests

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
- [Design Details](#design-details)
  - [Context: autoscaler annotations](#context-autoscaler-annotations)
  - [Delta calculation and baseline tracking](#delta-calculation-and-baseline-tracking)
  - [Admission and ProvisioningRequest lifecycle](#admission-and-provisioningrequest-lifecycle)
  - [Per-Pod provisioning identity and node selectors](#per-pod-provisioning-identity-and-node-selectors)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [Delta baseline can be lost](#delta-baseline-can-be-lost)
    - [Late replacement pods can become unschedulable](#late-replacement-pods-can-become-unschedulable)
    - [Node failure recovery depends on Job policy and provisioning class](#node-failure-recovery-depends-on-job-policy-and-provisioning-class)
    - [Users can confuse total quota with delta capacity](#users-can-confuse-total-quota-with-delta-capacity)
    - [Provider behavior differs](#provider-behavior-differs)
    - [Topology Aware Scheduling is not supported](#topology-aware-scheduling-is-not-supported)
  - [Test Plan](#test-plan)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [Stable](#stable)
- [Notes](#notes)
  - [Workload integration validation status](#workload-integration-validation-status)
  - [Provisioning class validation status](#provisioning-class-validation-status)
- [Alternatives](#alternatives)
  - [Request the full Workload size after every scale-up](#request-the-full-workload-size-after-every-scale-up)
  - [Treat a quota-reserved predecessor as available capacity](#treat-a-quota-reserved-predecessor-as-available-capacity)
  - [Persist request identity on the shared elastic template](#persist-request-identity-on-the-shared-elastic-template)
  - [Apply request node selectors on the template and override them per pod](#apply-request-node-selectors-on-the-template-and-override-them-per-pod)
  - [Record the increment on the Workload at admission](#record-the-increment-on-the-workload-at-admission)
  - [Keep the previous Workload until no successor needs it](#keep-the-previous-workload-until-no-successor-needs-it)
<!-- /toc -->

## Summary

This proposal adds support for running elastic WorkloadSlice-based workloads
with ProvisioningRequest admission checks.

It covers providers that separate capacity from successive scale-ups by
injecting different node selectors for each ProvisioningRequest. Existing pods
continue running on previously provisioned capacity while newly admitted pods
receive the provisioning metadata for the latest scale-up.

## Motivation

Elastic workloads use a chain of WorkloadSlices to represent changes to one
running job. Each scale-up creates a new pending WorkloadSlice while the
underlying job and its admitted pods continue running.

ProvisioningRequest admission currently treats each Workload independently.
Without elastic integration, a replacement Workload can request its entire
size again instead of only the additional capacity. It can also apply one
ProvisioningRequest's identity to a shared pod template, even though later
scale-ups may use different requests and node selectors.

Elastic workloads need a corresponding chain of incremental
ProvisioningRequests. Each request must represent only the new capacity for its
WorkloadSlice, and only pods joining that increment should receive its
provisioning identity.

### Goals

- Support elastic WorkloadSlice scale-up with ProvisioningRequest admission
  checks, including providers that use distinct node selectors for successive
  provisioning waves.
- Preserve running pods on previously admitted capacity while provisioning and
  admitting only the new increment.

### Non-Goals

- Redesigning the general WorkloadSlice replacement mechanism.
- Adding provider-specific behavior to Kueue's class-agnostic
  ProvisioningRequest controller.
- Combining elastic WorkloadSlices, ProvisioningRequests, and Topology Aware
  Scheduling (TAS) in Alpha.
- Guaranteeing rank-preserving recovery for late replacement pods.
- Supporting TPU multi-host provisioning, non-GKE reservation classes, or
  Elastic Workloads with MultiKueue in Alpha.

## Proposal

Introduce
`ElasticJobsViaWorkloadSlicesForProvisioningRequests`, an Alpha feature gate
that is disabled by default and depends on
`ElasticJobsViaWorkloadSlices`.

When enabled:

1. Each scale-up WorkloadSlice creates a corresponding ProvisioningRequest.
2. The new ProvisioningRequest is sized as the difference between:
   - the total size requested by the current quota-reserved WorkloadSlice; and
   - capacity represented by the previously admitted WorkloadSlice.
3. The new WorkloadSlice tracks the delta ProvisioningRequest through its
   `status.admissionChecks`.
4. Once the additional capacity is provisioned, the WorkloadSlice becomes
   admitted.
5. Existing pods continue running with their previous provisioning identity.
6. Newly admitted pods receive the new request's provisioning metadata and can
   use a distinct node selector for that provisioning wave.

For example:

```text
admitted size:  3
requested size: 5
new ProvisioningRequest size: 2
```

If another scale-up arrives while the size-5 request is still pending, the
next delta remains based on the last successfully admitted size rather than
capacity that has only been requested.

## Design Details

### Context: autoscaler annotations

ProvisioningRequest admission can add two annotations to a Pod:

- `autoscaling.x-k8s.io/consume-provisioning-request` identifies one specific
  ProvisioningRequest.
- `autoscaling.x-k8s.io/provisioning-class-name` identifies the provisioning
  mechanism.

Some providers use the consume annotation to inject scheduling metadata. For
example, `queued-provisioning.gke.io` injects a request-specific node selector
so the Pod can run only on nodes allocated for that request.

A normal, non-elastic workload is admitted once, so its template and pods can
share one request identity. An elastic workload is different: one
long-lived template creates pods across several provisioning waves. Persisting
the consume annotation or a request-specific node selector on that template
would make later pods inherit an obsolete request.

### Delta calculation and baseline tracking

The current WorkloadSlice always represents the total desired workload. Kueue
quota is reserved for that total. Its ProvisioningRequest asks only for the
positive increment beyond previously admitted capacity.

For each PodSet:

```text
delta = current assigned count - previous admitted effective count
```

The effective previous count is the predecessor's admitted assignment count
reduced by its reported reclaimable pods, the same count Kueue charges against
quota. This avoids treating stale peak assignments after scale-down, partial
admission, or already-finished pods as capacity that is still available. For
example, a predecessor admitted for 3 with 1 pod reclaimed is a baseline of 2,
so scaling to 5 requests 3.

Only a fully admitted predecessor is a valid baseline. A Workload with quota
reservation but a pending ProvisioningRequest has not proven that its capacity
exists.

The Alpha implementation finds the latest admitted, non-evicted Workload in
the live slice chain. Replaced Workloads remain eligible even after they are
marked Finished because their pods are still the running-capacity baseline
while a successor waits for admission. Pending intermediate slices are skipped.
The effective counts reuse Kueue's existing `workload.Info` total-requests
computation, so reclaimable pods are applied exactly as they are for quota.

Alpha deliberately does not persist a count snapshot annotation. A snapshot
can become stale if its predecessor is preempted after the successor is
created. If retention garbage-collects every usable predecessor, Kueue falls
back to requesting the full current size. This may over-provision, but it does
not under-provision based on capacity that may no longer exist.

For Beta, how the baseline is retained or derived will be reevaluated. Alpha
does not commit to any one mechanism. Candidates include:

- An interim condition before `Finished`, such as `ReplacementPending`, that
  releases quota but defers garbage collection until the successor is admitted.
- Delaying the predecessor's `Finished` condition until the replacement is
  fully admitted, or keeping it admitted while marking the replacement pending.
- Recording the increment the scheduler already computes for a replacement
  slice on the Workload at admission, so the provisioning controller reads it
  instead of walking the chain. This cannot reuse
  `.status.admission.podSetAssignments[].count`, which must stay consistent
  with the copied topology assignment, so it would need a dedicated field.
- A finalizer or another retention mechanism that preserves historical state
  such as `topologyAssignment` or `reclaimablePods`, not only PodSet counts.

A dedicated delta field solves exactly one number for one consumer; the
ProvisioningRequest controller is the only asynchronous reader of predecessor
state today, while everything else reads the predecessor in-cycle from the
snapshot. Keeping the predecessor available (`ReplacementPending` or similar)
is more general: it leaves time to copy or look up `reclaimablePods`,
`topologyAssignment`, and whatever a future asynchronous consumer needs. Each
approach must account for quota ownership while both slices coexist.

### Admission and ProvisioningRequest lifecycle

The scheduler reserves quota for the complete current WorkloadSlice and
initializes its ProvisioningRequest AdmissionCheck.

The provisioning controller creates a delta ProvisioningRequest owned by that
slice. The request status is reflected in the slice's
`status.admissionChecks`:

- Pending while capacity is being evaluated or provisioned.
- Ready after the request reports that capacity is available.
- Retry or Rejected according to the ProvisioningRequest retry policy after a
  failure or expired booking.

The WorkloadSlice becomes admitted only after its required AdmissionChecks are
Ready. This admission authorizes the new pods to join the already-running
elastic workload.

If a newer scale-up supersedes a pending slice, the intermediate slice is
finished and its ProvisioningRequest is removed. The new request is calculated
from the last admitted baseline, not the unfinished request.

If the current size is already covered by admitted capacity, the AdmissionCheck
is marked Ready without creating an empty ProvisioningRequest.

### Per-Pod provisioning identity and node selectors

The consume annotation and request-specific AdmissionCheck node selectors must
not persist on shared elastic templates. The provisioning class and stable
ResourceFlavor selectors can remain because they do not change between
WorkloadSlices.

Two API constraints force this split. A Job's pod template is immutable once
the Job is unsuspended, so whatever the first admission writes there is
inherited by every later pod. And a gated Pod may only gain `spec.nodeSelector`
keys; the API server rejects changing or deleting an existing key
(`ValidatePodUpdate`). If the first ProvisioningRequest's selector were baked
into the template, pods of every later slice would inherit it and the
ElasticJobUngater could not retarget them to their own request.

At the job's first admission the job reconciler therefore removes the
AdmissionCheck `podSetUpdates` node selectors from the template it applies,
keeping only the stable selectors supplied by the ResourceFlavor. The consume
annotation is stripped when admission metadata is merged into the template,
alongside the existing overrideable Kueue annotations. Both steps run only
for elastic Workloads with the feature gate enabled.

Each new pod starts with an elastic scheduling gate and without
request-specific placement inherited from the shared template. When the
corresponding WorkloadSlice is admitted, the ElasticJobUngater:

1. Finds the active admitted WorkloadSlice.
2. Counts already-ungated pods against the total granted count.
3. Selects only the number of gated pods needed to fill the admitted increment.
4. Adds the active request's consume and class annotations if they are absent.
5. Adds the active request's AdmissionCheck node selectors.
6. Removes the elastic scheduling gate.

Existing pods keep their previously assigned request identity. The immutable
ProvisioningRequest annotations are never overwritten with a conflicting
value, and a node selector key that already exists on the pod with a different
value is never changed because the API would reject it. A conflicting pod
remains gated rather than being silently reassigned.

ProvisioningRequest simulation PodTemplates are copied and sanitized before
Cluster Autoscaler evaluates them. For elastic Workloads with the feature gate
enabled, elastic Workload and request identity is removed and scheduling gates
are cleared; Cluster Autoscaler ignores gated pods, so leaving the elastic gate
on the template can keep a request Accepted indefinitely. Flavor selectors,
tolerations, and resources remain available for capacity simulation. Templates
for non-elastic Workloads, or with the gate disabled, are unchanged.

### Risks and Mitigations

#### Delta baseline can be lost

Workload retention or manual deletion can remove the previous admitted
Workload before a later slice calculates its delta. Without a retained
baseline, Kueue may create an oversized ProvisioningRequest.

Alpha deliberately accepts this as an over-provisioning failure mode rather
than persisting a snapshot that can become stale after preemption. For Beta,
the retention or derivation of the baseline will be reevaluated; see
[Delta calculation and baseline tracking](#delta-calculation-and-baseline-tracking)
for the candidates under consideration.

#### Late replacement pods can become unschedulable

Suppose pod A ran on capacity created by an earlier ProvisioningRequest. After
a later scale-up, the job controller creates a replacement. The new pod has no
portable lineage identifying the
provisioning wave it replaces, so it receives the active WorkloadSlice's newer
provisioning identity.

Two problems can result:

- The replacement's new node selector may not fit any remaining capacity in
  the new pool, while capacity from the earlier pool is available under a
  different selector.
- Even if the replacement can schedule, assigning it to the latest wave can
  violate rank-based ordering expected by some distributed workloads.

For Alpha, administrators should configure `waitForPodsReady` with
`recoveryTimeout`. This provides a bounded recovery path by allowing Kueue to
evict and requeue a workload that does not recover.

The identity and rank-assignment problem will be reevaluated for Beta.

#### Node failure recovery depends on Job policy and provisioning class

A live GKE failure test forced an origin pod A and a later-scale pod
B onto different nodes by using required hostname anti-affinity. Before
injecting failure, node A contained only pod A and platform DaemonSets. Its
backing GCE instance was then deleted.

With `backoffLimit: 6`, the observed recovery was:

1. Pod B remained Running on node B with the same Pod UID throughout.
2. Node A became NotReady.
3. The Job controller created a replacement for pod A 93 seconds later.
4. Kueue assigned the replacement to the currently active WorkloadSlice and
   current ProvisioningRequest, rather than recovering A's historical request.
5. The replacement became Ready about 95 seconds after node A became NotReady.
6. GKE separately repaired node A under the same node name but with a new
   Kubernetes Node UID and GCE instance ID.

The replacement scheduled on a different already-available node matching the
same ComputeClass. This works with
`best-effort-atomic-scale-up.autoscaling.x-k8s.io` because atomic provisioning
does not add a request-specific node selector: capacity from different
provisioning waves is interchangeable when the stable ComputeClass selector
matches.

A control run with `backoffLimit: 0` did not recover one pod. Kubernetes marked
the entire Job failed after pod A was lost and deleted the healthy pods.
Operators that require pod-level recovery must configure a retry policy that
allows the Job controller to create a replacement.

Queued provisioning remains subject to the late-replacement risk above. A
replacement receives the current request selector, which may not match a
repaired node created for the failed pod's historical request.

#### Users can confuse total quota with delta capacity

The Workload and ProvisioningRequest intentionally show different counts:

- Workload quota represents the complete active workload.
- ProvisioningRequest count represents only additional capacity.

Documentation and events should make this distinction clear when operators
debug scaling.

#### Provider behavior differs

Kueue remains class-agnostic, but providers differ in whether they inject node
selectors, reserve capacity, or permit reuse across request generations.

Alpha documentation lists only combinations that have been exercised. Other
classes remain unsupported until their behavior is validated.

#### Topology Aware Scheduling is not supported

The combination of elastic WorkloadSlices, ProvisioningRequests, and TAS is not
supported in Alpha. TAS alone works with elastic WorkloadSlices, and
ProvisioningRequests alone work with elastic WorkloadSlices, but combining them
introduces two independently changing placement constraints:

- TAS assigns each replacement WorkloadSlice to topology domains.
- Each incremental ProvisioningRequest can add request-specific node selectors
  for newly provisioned capacity.

The Alpha baseline lookup uses only the predecessor's effective PodSet counts.
It does not use the predecessor's `topologyAssignment` or otherwise guarantee
that the new request's nodes match the topology domains selected for the
replacement slice. Template updates can also cause stale provisioning metadata,
replacement pods, and additional WorkloadSlice/ProvisioningRequest churn before
convergence.

The conflict is specific to provisioning classes that inject a request-specific
node selector (for example `queued-provisioning.gke.io`). The new slice injects
the new request's selector, and a failed pod from a previous slice cannot easily
be replaced onto its original capacity. Classes that do not inject a selector,
such as the community atomic scale-up class, are expected to work reasonably
well because capacity from different waves is interchangeable.

Alpha does not reject enabling `ElasticJobsViaWorkloadSlicesWithTAS` together
with this feature gate. The combination is unsupported and users who enable
both may observe the churn described above without an error. Validation is
intentionally not added because:

- Early adopters can experiment with the combination and provide feedback.
- The failure modes are mitigated by `waitForPodsReady`, which evicts and
  requeues a Workload that does not recover.
- Users may legitimately have disjoint ResourceFlavors for elastic jobs, one
  using TAS and one using ProvisioningRequests, which a gate-level rejection
  would block.
- It is likely to work for classes that do not inject node selectors.

Supporting this combination requires a lifecycle mechanism that preserves
enough predecessor admission state to coordinate both placement decisions.
This will be reevaluated for Beta rather than presenting the partially
convergent behavior as supported in Alpha.

### Test Plan

#### Unit tests

- Delta calculation subtracts the last admitted effective count.
- Pending intermediate slices do not become the subtraction baseline.
- A missing or garbage-collected admitted predecessor falls back to requesting
  the full current size.
- Fully covered replacements create no empty ProvisioningRequest.
- Delta calculation applies reclaimable pods to the predecessor's baseline.
- Elastic templates retain stable ResourceFlavor selectors but not
  request-specific consume annotations or AdmissionCheck node selectors.
- Per-pod request identity and node selectors are added once; a conflicting
  annotation or node selector value leaves the pod gated.
- ProvisioningRequest PodTemplates are sanitized only for elastic Workloads
  with the feature gate enabled.
- Feature-disabled behavior remains compatible with existing WorkloadSlice and
  ProvisioningRequest behavior.

#### Integration tests

- Submit elastic batch Job and RayCluster workloads with a ProvisioningRequest
  AdmissionCheck.
- Admit the origin slice and provision its request.
- Scale while the underlying workload remains running.
- Verify each replacement request contains only incremental capacity.
- Supersede a pending request and verify the next request uses the last
  admitted baseline.
- Verify the intermediate Workload is finished and its request removed.
- Verify new pods receive the new request identity while existing pods continue
  running.
- Delete one backing VM after pods from different scale-up waves are placed on
  separate nodes; verify an unaffected pod keeps its UID and the failed pod is
  replaced according to the Job retry policy.
- Exercise the feature with the gate enabled and verify gate-disabled
  compatibility.
- Cover RayJob's embedded RayCluster templates at unit/integration level.

### Graduation Criteria

#### Alpha

- Implementation is guarded by
  `ElasticJobsViaWorkloadSlicesForProvisioningRequests`, disabled by default.
- User documentation includes configuration and scale-up examples.

#### Beta

- The feature gate is enabled by default.
- All reported Alpha bugs are fixed.
- The mitigation for late replacement pods that can become unschedulable is
  reevaluated.
- The mechanism for retaining or deriving the previous Workload baseline is
  reevaluated (see the candidates in
  [Delta calculation and baseline tracking](#delta-calculation-and-baseline-tracking)).
- The interaction of elastic jobs on TAS flavors with selector-injecting
  provisioning classes is reevaluated.

#### Stable

- The feature gate is locked and enabled by default.
- All reported bugs are fixed.
- Late replacement pods have a defined solution that preserves schedulability
  and ordering requirements.

## Notes

This KEP was raised during review of the initial elastic WorkloadSlice and
ProvisioningRequest implementation.

### Workload integration validation status

- `batch/v1 Job`: live queued and atomic provisioning verified through five
  sequential admitted slices (`1 -> 2 -> 3 -> 4 -> 5`), in addition to an
  overlapping `1 -> 2 (pending) -> 3` resize, and a `3 -> 4 -> 2 -> 3`
  sequence covering an in-place scale-down followed by a re-grow. The
  immutable Job template retained only its stable ResourceFlavor selector;
  each gated Pod received the identity and selector for its active
  ProvisioningRequest, and the re-grow requested exactly the one-pod delta
  over the shrunk slice.
- `RayCluster`: live autoscaler-driven queued-provisioning `1 -> 2 -> 3`
  scale-up verified with the origin worker remaining running.
- `RayJob`: uses the same per-pod admission mechanism for its embedded
  RayCluster templates and is covered by unit/integration tests, but has not
  yet been exercised in a live queued-provisioning scale-up.
- Other job-framework integrations require explicit validation before being
  listed as supported with request-specific node selectors.

### Provisioning class validation status

The Kueue ProvisioningRequest controller is class-agnostic. The following
classes have been exercised with the elastic path:

- `queued-provisioning.gke.io`
  - GKE queued/flex-start provisioning.
  - Live RayCluster and batch Job E2Es exercised `1 -> 2 -> 3` scale-up. In
    both cases the pending size-2 slice was replaced, the final
    ProvisioningRequest asked for the two-pod delta, the origin pod retained
    its original request selector, and the two new pods received the final
    request selector. All three pods became Ready without restarting the
    origin.
  - A separate sequential batch Job test admitted all five slices from size 1
    through 5. Every replacement carried the immediately preceding admitted
    count, requested a delta of 1, and produced one Ready Pod with a distinct
    request selector.
- `best-effort-atomic-scale-up.autoscaling.x-k8s.io`
  - Community Cluster Autoscaler atomic scale-up.
  - Unit, integration, and live node-failure tests covered shared ComputeClass
    capacity. A five-slice sequential test also requested delta 1 at every
    transition and retained five distinct per-wave request identities.
Not yet validated:

- `check-capacity.autoscaling.x-k8s.io` with elastic WorkloadSlice scale-up.
  The generic ProvisioningRequest path is covered, but the exact elastic
  integration has not been exercised.
- TPU multi-host queued provisioning.
- Non-GKE queued or reservation classes.
- Elastic workloads combined with MultiKueue.
- Elastic workloads combining ProvisioningRequests with TAS.
- Job-framework integrations not listed as validated above.

Legacy isolation tests showed why TAS is excluded: WorkloadSlices with TAS but
without ProvisioningRequests passed, and WorkloadSlices with
ProvisioningRequests but without TAS passed. The combined path reached running
pods on GKE with `queued-provisioning.gke.io` using a patched pre-PR Kueue
build, but produced a transient `MissingProvisioningRequest` Pod warning and an
extra replacement WorkloadSlice/ProvisioningRequest before convergence.
`MissingProvisioningRequest` is emitted by GKE's queued-provisioning
integration, not by Kueue or the upstream Cluster Autoscaler code, when a Pod's
`consume-provisioning-request` annotation names a ProvisioningRequest that no
longer exists. In that run a replacement worker still carried the superseded
slice's request annotation, which had already been deleted, while receiving the
new request's node selector. That result demonstrates partial functionality,
not a stable supported lifecycle.

Overlapping scale-up requests are covered by replacing the pending intermediate
request and calculating the new delta from the last admitted capacity.

## Alternatives

### Request the full Workload size after every scale-up

Rejected because it repeatedly requests capacity for pods that are already
running and can over-provision each replacement wave.

### Treat a quota-reserved predecessor as available capacity

Rejected because quota reservation does not prove its ProvisioningRequest
succeeded. This can under-provision the next slice.

### Persist request identity on the shared elastic template

Rejected because future pods would inherit an obsolete request and potentially
the wrong provider-injected node selector.

### Apply request node selectors on the template and override them per pod

Keeping the template untouched and letting the ElasticJobUngater override an
inherited request-specific node selector on each gated pod would keep
placement logic in one layer. Rejected because the API server only allows
adding `spec.nodeSelector` keys to a gated Pod, never changing an existing one.
On GKE with `queued-provisioning.gke.io`, the first admission baked its
request selector into the immutable Job template, later pods inherited it, and
every ungater retarget to the new request was rejected with
`only additions to spec.nodeSelector are allowed`; the new pods stayed gated
indefinitely. Request-specific selectors are therefore kept off the template
at first admission and added per gated pod.

### Record the increment on the Workload at admission

The scheduler already computes the increment for a replacement slice in-cycle
and charges only that against quota. Recording it on the Workload would let the
provisioning controller read the delta directly, with no baseline to lose and
nothing to keep alive; `DelayedTopologyRequest` is a precedent for scheduler
state stamped for an asynchronous check. It cannot reuse
`.status.admission.podSetAssignments[].count`, which is copied together with
the topology assignment and must stay consistent with it, so it would require a
dedicated field. Deferred to Beta evaluation rather than adding API surface in
Alpha.

### Keep the previous Workload until no successor needs it

This is a candidate Beta improvement. Options include delaying `Finished`
until the successor is admitted, retaining the predecessor as admitted while a
replacement is pending, introducing a `ReplacementPending` condition that
releases quota but defers garbage collection, or using a finalizer/retention
mechanism. Alpha does not commit to any of these. The selected design must
preserve historical state without double-counting quota while both slices
coexist.
