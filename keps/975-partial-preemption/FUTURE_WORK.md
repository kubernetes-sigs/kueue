# KEP-975: Partial Preemption of Elastic Workloads - Future Work

<!-- toc -->
- [Cost model and candidate ordering](#cost-model-and-candidate-ordering)
  - [Partial-preemption-aware ordering](#partial-preemption-aware-ordering)
  - [A cost function](#a-cost-function)
- [Fill-back order](#fill-back-order)
- [Spreading reductions across victims](#spreading-reductions-across-victims)
- [Multiple PodSets per Workload](#multiple-podsets-per-workload)
- [Overlap recomputation](#overlap-recomputation)
- [Timeout](#timeout)
- [Proactive downscaling](#proactive-downscaling)
<!-- /toc -->

Everything in this file is subject to change based on user feedback. Field, flag and annotation
names and types are only examples. These ideas are deferred to keep Alpha minimal. Each is
re-evaluated for Beta, and production feedback may bring one forward to a later Alpha.

## Cost model and candidate ordering

Alpha uses the existing candidate ordering and performs a shrink pass followed by a whole-eviction
pass. It does not define priority bands or a cost trade-off between shrinking a higher-priority
victim and evicting a lower-priority one. Those policies may be revisited with user feedback.

### Partial-preemption-aware ordering

The existing candidate comparator could be extended with partial-preemption-specific criteria:

1. **Resume an in-flight reduction first.** A candidate with an in-flight
   `partialPreemption.targetCount` could be resumed before a fresh victim is picked, so a reduction
   already in progress converges before another victim is selected.
2. **Prefer shrinking over evicting.** An eligible partial-preemption candidate could be preferred
   over a whole-eviction-only candidate, because shrinking a PodSet may be less disruptive than
   evicting a whole Workload.
3. **Account for remaining reclaimable replicas.** A tie-break could consider how far a candidate's
   effective used count exceeds its `partialPreemption.minCount`, capped by any in-flight target,
   rather than its raw used count.

These criteria interact with the existing priority and queue ordering. Alpha deliberately leaves the
existing ordering unchanged; a future cost model should make the trade-offs explicit rather than
implicitly changing priority semantics.

### A cost function

Priority is not workload importance. A cost function, for example the number of preempted Pods
weighted by workload importance, could choose between shrinking a higher-priority victim and evicting
a lower-priority one. Users may also want to decide this themselves. A related policy question is
whether candidates should be grouped into priority bands and when the scheduler may advance to a
higher-priority band; Alpha makes no such guarantee.

A shrunk Workload may later regrow through the existing scale-up path. Alpha does not coordinate
that regrowth with preemption by priority, so a higher-priority Workload may reclaim quota currently
used by a lower-priority Workload. The lower-priority Workload may then be shrunk or evicted, and
repeated regrowth and preemption can cause quota oscillation. Beta should define priority-aware
coordination so regrowth does not continually undo preemption decisions.

## Fill-back order

In Alpha, fill-back walks the victims backwards and, for each one, tries a whole restore and then,
for a victim that was shrunk but not evicted, a binary-searched partial restore. A refined order
could keep more victims running:

- Restore an evicted victim to its floor, instead of only whole or not at all.
- Do every whole restore before any partial one, so that a partial restore does not take the room
  another victim needs to come back whole.

## Spreading reductions across victims

Alpha reduces a selected candidate directly from its effective used count to its configured
`partialPreemption.minCount`. This can remove more replicas from one victim than the preemptor needs
while other eligible victims remain untouched. A future strategy could spread the required reduction
across multiple candidates, for example by limiting how many replicas are reclaimed from each victim
per round and revisiting candidates until the preemptor fits or all candidates reach their minimum.

An optional `kueue.x-k8s.io/partial-preemption-max-reclaimed-count` annotation could cap how many
replicas one round takes from a victim. The cap would limit only how much that victim loses in a
round, not its total loss; a capped victim may shift more of the reduction onto uncapped peers. A
scheduler-wide bounded step per victim per round could provide spreading without a per-victim cap
and its side effect. The policy, configuration surface, and interaction with candidate ordering need
user feedback.

## Multiple PodSets per Workload

In Alpha, at most one PodSet per Workload may have an effective used count above its
`partialPreemption.minCount` and therefore be shrinkable. PodSets at or below their floor do not count toward
this limit; a Workload with multiple shrinkable PodSets is evicted whole instead of shrinking them.
Most elastic workloads in scope for Alpha (a Ray worker group, a Spark executor pool) only have one
shrinkable PodSet, so this does not block the primary use cases. Supporting several shrinkable
PodSets per Workload is a Beta criterion; it needs the ordering, target selection and fill-back logic
to reason about the combination of all of a Workload's floors rather than a single one.

## Overlap recomputation

In Alpha, `PartialPreemption` cannot be enabled together with
`RecomputeAssignmentUponPreemptionTargetsOverlap`. The current overlap-recomputation path does not
consistently account for a partially preempted Workload's reduced assignment: both the simulated
quota usage and the `preemptedWorkloads` handling during overlap resolution need to agree on how an
in-flight target affects the assignment. Changing only the usage passed to the simulation is not
sufficient.

Supporting both features is deferred to Beta. The implementation must define and consistently apply
the reduced-assignment semantics through overlap recomputation, including how existing partial
preemption targets participate, before removing the feature-gate incompatibility validation.

## Timeout

In Alpha, a shrink whose Pods never go away (a PodDisruptionBudget refusing the eviction, a Pod
stuck terminating behind a finalizer or on an unreachable node) keeps the preemptor `Pending`.
A `partialPreemption.timeout` in the Configuration API, defaulting to 5m, could bound that wait:

- The clock starts when Kueue sets `PartiallyPreempted` to `True`. Lowering the target while the
  condition is still `True` does not restart it; lowering it again after the quota was released
  starts a new clock.
- It never expires before the evicted Pods' longest `terminationGracePeriodSeconds`.
- When it expires with Pods still above the target, Kueue sets `PartiallyPreempted` to `False` with
  reason `Timeout`. The victim is no longer counted at its target, and it becomes a whole-eviction
  candidate that is not shrunk again.

## Proactive downscaling

In proactive downscaling, the job's runtime shrinks itself to `partialPreemption.targetCount`: for example,
a Kueue-aware Spark driver lowers its executor target, or the batch/Job integration lowers
`parallelism`. The runtime chooses which Pods leave, and quota is released through the existing
elastic scale-down path when the Workload's spec count drops. A framework would opt in per
integration; the Kueue scheduler side of this KEP does not change.

We hold off on it because:

- It needs code in each runtime. Upstream Spark has no Kueue-aware executor allocator, a driver
  plugin needs Spark internals, and a new Spark release cannot reach the Spark 3 versions in use.
- Ray has no clean way in (see [Kueue mutates the job's replica count
  directly](README.md#kueue-mutates-the-jobs-replica-count-directly)).
- It adds little for Spark: an evicted executor already decommissions on SIGTERM, the same path that
  a driver-initiated scale-down uses.
- With an autoscaler, a runtime that lowers its own size races with the autoscaler raising it again.
- A second release path doubles the API and test surface before reactive downscaling has user
  feedback.

Its main benefit is choosing which Pods leave. Under reactive downscaling,
`controller.kubernetes.io/pod-deletion-cost` already lets a framework steer that.
