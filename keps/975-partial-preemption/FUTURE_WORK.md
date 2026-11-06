# KEP-975: Partial Preemption of Elastic Workloads - Future Work

<!-- toc -->
- [Cost model and candidate ordering](#cost-model-and-candidate-ordering)
  - [Strict priority](#strict-priority)
  - [Partial-preemption-aware ordering](#partial-preemption-aware-ordering)
  - [A cost function](#a-cost-function)
- [Fill-back order](#fill-back-order)
- [Spreading reductions across victims](#spreading-reductions-across-victims)
- [Timeout](#timeout)
- [Proactive downscaling](#proactive-downscaling)
<!-- /toc -->

Everything in this file is subject to change based on user feedback. Field, flag and annotation
names and types are only examples. These ideas are deferred to keep Alpha minimal. Each is
re-evaluated for Beta, and production feedback may bring one forward to a later Alpha.

## Cost model and candidate ordering

In Alpha, any shrink counts as cheaper than any whole eviction: the shrink pass walks the whole
candidate order, across segments and priorities, before anything is evicted. So an opted-in
higher-priority Workload can lose Pods while a lower-priority one is kept, and same-queue victims
can shrink before borrowed quota is reclaimed from other queues.

### Strict priority

Run the shrink pass and then the evict pass inside each group of segment and priority, before
moving to the next group. Lower priority then yields first, as it does for whole eviction today, at
the cost of evicting a lower-priority Workload whole that a cross-priority shrink would have kept
running. It could be exposed as a `partialPreemption.disableStrictPriorityPreemption` flag.

### Partial-preemption-aware ordering

Three criteria could be added to the existing candidate comparator:

1. **Resume an in-flight reduction first.** A candidate whose current count is still above an
   in-flight `preemptionTargetCount` sorts just after already-evicting candidates, so a reduction
   already in progress is resumed before a fresh victim is picked. This is what keeps the scheduler
   from spreading reductions across new victims while an earlier target is still converging.
2. **Prefer shrinking over evicting.** Once priority ties, an eligible partial-preemptible candidate
   is preferred over a whole-eviction-only one, because shrinking a PodSet is cheaper than evicting a
   whole Workload.
3. **Among partial peers, prefer the one that can give back more.** The tie-break key is how far the
   candidate's effective used count exceeds its `preemptionMinCount` (capped by any in-flight
   `preemptionTargetCount`) — the number of replicas it can still shed — not the raw used count.
   Because this key is recomputed each scheduling cycle, a victim that was already shrunk falls
   behind its peers, so repeated rounds rotate across candidates and spread the reduction instead of
   repeatedly hitting the same Workload.

In Alpha, the shrink pass already favors shrinking over evicting, and an in-flight target counts at
that target before the passes start.

### A cost function

Priority is not workload importance, and under Fair Sharing and Admission Fair Sharing the candidate
order is not by priority at all. A cost function, for example the number of preempted Pods weighted
by workload importance, could choose between shrinking a higher-priority victim and evicting a
lower-priority one. Users may also want to decide this themselves.

## Fill-back order

In Alpha, fill-back walks the victims backwards and, for each one, tries a whole restore and then,
for a victim that was shrunk but not evicted, a binary-searched partial restore. A refined order
could keep more victims running:

- Restore an evicted victim to its floor, instead of only whole or not at all.
- Do every whole restore before any partial one, so that a partial restore does not take the room
  another victim needs to come back whole.
- With Fair Sharing, consider a shrunk victim for whole eviction only after every shrink across
  ClusterQueues has been tried, as the classical passes do.

## Spreading reductions across victims

An optional `kueue.x-k8s.io/partial-preemption-max-reclaimed-count` annotation could cap how many
replicas one round takes from a victim. The Kueue scheduler would then walk the victims again over
more rounds until the preemptor fits or every victim reaches its floor. The cap would not limit how
much a victim loses in total, only the order in which victims lose Pods, and a capped victim would
shift its loss onto uncapped peers. A bounded step per victim per round, as a scheduler default
rather than a per-victim annotation, would spread reductions without that side effect.

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

In proactive downscaling, the job's runtime shrinks itself to `preemptionTargetCount`: for example,
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
