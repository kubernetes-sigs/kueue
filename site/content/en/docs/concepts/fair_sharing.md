---
title: "Fair Sharing"
date: 2025-05-28
weight: 6
description: >
  Mechanisms in Kueue to share quota between tenants fairly.
---


## [Admission Fair Sharing](/docs/concepts/admission_fair_sharing)

A mechanism for ordering workloads based on the historical resource usage of
their source LocalQueues, giving preference to those that have consumed fewer
resources over time.

## [Preemption based Fair Sharing](/docs/concepts/preemption/#fair-sharing)

### Proof that two workloads won't preempt each other

Here we present a proof that FairSharing preemption
[strategies](https://github.com/kubernetes-sigs/kueue/blob/3f9504f65db14a947c27b4155d3bd297fa1b60d5/pkg/scheduler/preemption/fairsharing/strategy.go#L35-L43)
won't result in a loop where two workloads in different ClusterQueues keep
preempting each other.

Suppose Workload A is trying to preempt Workload B, and that these workloads are
in different ClusterQueues.  We define the following variables as Dominant
Resource Shares (DRS) of respective ClusterQueues before and after admission of
their respective workloads:
  - DRS_A_pending
  - DRS_A_admitted
  - DRS_B_pending
  - DRS_B_admitted

We assume that, if the ClusterQueue is borrowing after admission, admission
increases DRS and preemption decreases DRS: DRS_A_pending < DRS_A_admitted and
DRS_B_pending < DRS_B_admitted. This assumption is necessary, as this could
fail to hold if the workloads in question are not using the dominant resource.

We assume that both workloads are borrowing after admission. If either workload
is not borrowing after admission, a preemption loop will not occur, as we
exclude ClusterQueues/Cohorts which are not borrowing from preemption
candidates. See
[nextTarget](https://github.com/kubernetes-sigs/kueue/blob/3f9504f65db14a947c27b4155d3bd297fa1b60d5/pkg/scheduler/preemption/fairsharing/ordering.go#L132-L138),
where we prune Cohorts and ClusterQueues with DRS=0.

#### Workload A Preempts Workload B

For preemption to occur, either [DRS_A_admitted <= DRS_B_pending](https://github.com/kubernetes-sigs/kueue/blob/3f9504f65db14a947c27b4155d3bd297fa1b60d5/pkg/scheduler/preemption/fairsharing/strategy.go#L35-L38),
or [DRS_A_admitted < DRS_B_admitted](https://github.com/kubernetes-sigs/kueue/blob/3f9504f65db14a947c27b4155d3bd297fa1b60d5/pkg/scheduler/preemption/fairsharing/strategy.go#L40-L43)
must hold.

Since DRS_B_pending < DRS_B_admitted, we conclude that DRS_A_admitted <
DRS_B_admitted always holds when preemption is possible

**Lemma 1** Workload A can preempt Workload B => DRS_A_admitted < DRS_B_admitted

#### Workload B Preempts Workload A
We use the same logic to create a contradiction.

For preemption to occur, either DRS_B_admitted <= DRS_A_pending, or
DRS_B_admitted < DRS_A_admitted must hold.

Since DRS_A_pending < DRS_A_admitted, we conclude that DRS_B_admitted <
DRS_A_admitted always holds when preemption is possible.

**Lemma 2** Workload B can preempt Workload A => DRS_B_admitted < DRS_A_admitted

#### Contradiction
Assume that both preemption events are possible.

From Lemma 1, we have DRS_A_admitted < DRS_B_admitted.  From Lemma 2, we have
DRS_B_admitted < DRS_A_admitted.

This is a contradiction, as DRS_A_admitted cannot be both less than and greater
than DRS_B_admitted simultaneously.  Therefore, both preemption events cannot be
possible at the same time.
