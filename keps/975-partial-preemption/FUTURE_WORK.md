# KEP-975: Partial Preemption of Elastic Workloads - Future Work

<!-- toc -->
- [Cost model and candidate ordering](#cost-model-and-candidate-ordering)
  - [Strict priority](#strict-priority)
  - [Shrink larger candidates first](#shrink-larger-candidates-first)
  - [A cost function](#a-cost-function)
- [Spreading reductions across victims](#spreading-reductions-across-victims)
- [Timeout](#timeout)
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

### Shrink larger candidates first

Among candidates of equal priority, put the one that can shed more Pods first: the key is how far
its effective used count exceeds its `preemptionMinCount`, capped by any in-flight
`preemptionTargetCount`. Because the key is recomputed each scheduling cycle, a victim that was
already shrunk falls behind its peers, so later cycles spread the reduction instead of repeatedly
hitting the same Workload.

### A cost function

Priority is not workload importance, and under Fair Sharing and Admission Fair Sharing the candidate
order is not by priority at all. A cost function, for example the number of preempted Pods weighted
by workload importance, could choose between shrinking a higher-priority victim and evicting a
lower-priority one. Users may also want to decide this themselves.

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
