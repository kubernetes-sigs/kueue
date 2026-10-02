# KEP-13396: Configurable Preemptions - Future Work

<!-- toc -->
- [Configurable Candidate Ordering](#configurable-candidate-ordering)
  - [Proposed API for Custom Ordering](#proposed-api-for-custom-ordering)
  - [Examples with Custom Ordering](#examples-with-custom-ordering)
    - [Story 1 - Defragmentation with Explicit Priority Ordering](#story-1---defragmentation-with-explicit-priority-ordering)
    - [Story 2 - Hero Workload with Explicit Priority Ordering](#story-2---hero-workload-with-explicit-priority-ordering)
- [[Optimized] Dynamically Adjusted Candidate Generation](#optimized-dynamically-adjusted-candidate-generation)
    - [Motivation and Architectural Benefits](#motivation-and-architectural-benefits)
    - [Problem Statement](#problem-statement)
    - [Naive Solutions and Complexity Bottlenecks](#naive-solutions-and-complexity-bottlenecks)
    - [Proposed Approach: Multi-Queue Dynamic Iteration](#proposed-approach-multi-queue-dynamic-iteration)
    - [Example Walkthrough](#example-walkthrough)
    - [Implementation Caveats and Selector Isolation](#implementation-caveats-and-selector-isolation)
    - [Complexity Analysis](#complexity-analysis)
    - [Complexity Comparison](#complexity-comparison)
    - [Open Challenges](#open-challenges)
- [Time-Based Candidate Selectors (Execution and Creation Duration)](#time-based-candidate-selectors-execution-and-creation-duration)
  - [Proposed API for Time-Based Candidate Selectors](#proposed-api-for-time-based-candidate-selectors)
  - [Examples with Time-Based Candidate Selectors](#examples-with-time-based-candidate-selectors)
    - [Story 1 - Minimal Execution Duration Before Preemption](#story-1---minimal-execution-duration-before-preemption)
    - [Story 2 - SLA Protection Based on Workload Creation Time](#story-2---sla-protection-based-on-workload-creation-time)
- [Quota-Based Candidate Selectors (PreemptionConfigQuotaConstraint)](#quota-based-candidate-selectors-preemptionconfigquotaconstraint)
  - [Proposed API for Quota-Based Candidate Selectors](#proposed-api-for-quota-based-candidate-selectors)
- [PreemptionLimit (Rate-Limiting Guardrails)](#preemptionlimit-rate-limiting-guardrails)
  - [Proposed API for PreemptionLimit](#proposed-api-for-preemptionlimit)
  - [Observability When Reaching Preemption Limits](#observability-when-reaching-preemption-limits)
  - [Examples with PreemptionLimit](#examples-with-preemptionlimit)
    - [Story 1 - Global Preemption Rate Limiting](#story-1---global-preemption-rate-limiting)
    - [Story 2 - Protecting a Mission-Critical ClusterQueue from Preemption](#story-2---protecting-a-mission-critical-clusterqueue-from-preemption)
- [Minimum Trigger Duration (MinTriggerRequiredDuration)](#minimum-trigger-duration-mintriggerrequiredduration)
  - [Proposed API for Minimum Trigger Duration](#proposed-api-for-minimum-trigger-duration)
  - [Examples with Minimum Trigger Duration](#examples-with-minimum-trigger-duration)
    - [Story 1 - Grace Period for Topology Defragmentation](#story-1---grace-period-for-topology-defragmentation)
- [Per-Node DRA Device Feasibility Trigger (InsufficientDRADevices)](#per-node-dra-device-feasibility-trigger-insufficientdradevices)
  - [Future Evolution](#future-evolution)
<!-- /toc -->

## Configurable Candidate Ordering

In the initial iteration of `PreemptionConfig`, candidate workloads are ordered strictly by reusing the default ordering rules from classical preemption and fair sharing (as defined in `pkg/scheduler/preemption/common/ordering.go`):

0. Workloads already marked for preemption first (`isEvicted`).
1. Workloads from other ClusterQueues in the cohort before the ones in the same ClusterQueue as the preemptor.
2. (AdmissionFairSharing only) Workloads with lower LocalQueue fair sharing usage first.
3. Workloads with lower priority first (effective priority).
4. Workloads admitted more recently first (protecting long-running workloads).
5. Workload UID as tie-breaker for deterministic sorting.

In future iterations, we plan to reintroduce the `Ordering` field in `PreemptionConfigSpec` to allow cluster administrators to configure custom multi-key ordering comparator chains.

### Proposed API for Custom Ordering

```go
type PreemptionConfigSpec struct {
  // Rules to select preemption candidates.
  Rules []PreemptionConfigPreemptionRule

  // Ordering of preemption candidates evaluated sequentially as a multi-key comparator chain.
  // Workloads already marked for eviction (`isEvicted`) are always prioritized first implicitly,
  // so this criterion is omitted from the configurable ordering list.
  // The order is always deterministic, as the Workload UID is used as the final tie-breaker.
  // If not set, candidates will be ordered by default like this:
  // 1. Priority (Ascending: lowest priority first)
  // 2. AdmissionTimestamp (Descending: most recently admitted first, protecting long-running workloads)
  // 3. UID (Ascending: deterministic tie-breaker)
  // +optional
  Ordering []PreemptionConfigOrder `json:"ordering,omitempty"`
}

// PreemptionConfigOrderingField specifies the criterion used to sort candidate workloads during preemption evaluation.
// Note: PreemptionConfigOrderingField is a predefined enum of sorting keys, not arbitrary fields of the Workload struct.
// Supported values are:
// - "Priority": orders workloads by effective priority (accounting for priority boost if enabled).
//   - Ascending (default): lowest priority first.
//   - Descending: highest priority first.
//
// - "AdmissionTimestamp": orders workloads by the timestamp when quota was reserved (admitted).
//   - Ascending (default): oldest admitted workloads first (FIFO preemption).
//   - Descending: most recently admitted workloads first (LIFO preemption, protecting long-running workloads, matching classical Kueue).
//
// - "ClusterQueueDRS": orders workloads based on their ClusterQueue's Dominant Resource Share.
//   - Ascending (default): workloads from ClusterQueues with lower Dominant Resource Share first.
//   - Descending: workloads from ClusterQueues with higher Dominant Resource Share first (preempting heavy borrowers first).
//
// - "IsOtherCQ": orders workloads based on whether they belong to a different ClusterQueue than the preemptor.
//   - Ascending (default): workloads from the same ClusterQueue first, followed by other ClusterQueues.
//   - Descending: workloads from other ClusterQueues first, followed by the same ClusterQueue.
//
// - "IsOtherCohort": orders workloads based on whether they belong to a different Cohort than the preemptor.
//   - Ascending (default): workloads from the same direct Cohort first, followed by other Cohorts.
//   - Descending: workloads from other Cohorts first, followed by the same Cohort.
//
// - "IsDRSLessThanInitialShare": orders workloads based on whether preemption of the workload is fair according to the DRSLessThanInitialShare strategy.
//   - Ascending (default): workloads from ClusterQueues exceeding their initial share first (prioritizing preemption of borrowing workloads).
//   - Descending: workloads from ClusterQueues within their initial share first.
//
// - "IsDRSLessThanOrEqualToFinalShare": orders workloads based on whether preemption of the workload is fair according to the DRSLessThanOrEqualToFinalShare strategy.
//   - Ascending (default): workloads from ClusterQueues exceeding their final share first (protecting workloads within fair share).
//   - Descending: workloads from ClusterQueues within or equal to their final share first.
//
// - "LocalQueueDRS": orders workloads based on their LocalQueue's Dominant Resource Share (fair sharing usage).
//   - Ascending (default): workloads from LocalQueues with lower Dominant Resource Share first.
//   - Descending: workloads from LocalQueues with higher Dominant Resource Share first (preempting heavy LocalQueue borrowers first).
//
// +kubebuilder:validation:Enum=Priority;AdmissionTimestamp;ClusterQueueDRS;IsOtherCQ;IsOtherCohort;IsDRSLessThanInitialShare;IsDRSLessThanOrEqualToFinalShare;LocalQueueDRS
type PreemptionConfigOrderingField string

const (
  // Priority orders candidates by effective priority (accounting for priority boost if enabled).
  // Ascending order places lowest priority candidates first.
  Priority PreemptionConfigOrderingField = "Priority"

  // AdmissionTimestamp orders candidates by the time quota was reserved.
  // Ascending order places oldest admitted candidates first and most recently admitted last.
  AdmissionTimestamp PreemptionConfigOrderingField = "AdmissionTimestamp"

  // ClusterQueueDRS orders candidates based on their ClusterQueue's Dominant Resource Share.
  // Ascending order places candidates from ClusterQueues with lower Dominant Resource Share first.
  ClusterQueueDRS PreemptionConfigOrderingField = "ClusterQueueDRS"

  // IsOtherCQ orders candidates based on whether their ClusterQueue differs from the preemptor.
  // Ascending order places workloads from the same ClusterQueue first.
  IsOtherCQ PreemptionConfigOrderingField = "IsOtherCQ"

  // IsOtherCohort orders candidates based on whether their direct Cohort differs from the preemptor.
  // Ascending order places workloads from the same Cohort first.
  IsOtherCohort PreemptionConfigOrderingField = "IsOtherCohort"

  // IsDRSLessThanInitialShare orders candidates based on whether preemption is fair according to DRSLessThanInitialShare.
  // Ascending order places workloads whose ClusterQueue exceeds initial share first.
  IsDRSLessThanInitialShare PreemptionConfigOrderingField = "IsDRSLessThanInitialShare"

  // IsDRSLessThanOrEqualToFinalShare orders candidates based on whether preemption is fair according to DRSLessThanOrEqualToFinalShare.
  // Ascending order places workloads whose ClusterQueue exceeds final share first.
  IsDRSLessThanOrEqualToFinalShare PreemptionConfigOrderingField = "IsDRSLessThanOrEqualToFinalShare"

  // LocalQueueDRS orders candidates based on their LocalQueue's Dominant Resource Share (fair sharing usage).
  // Ascending order places candidates from LocalQueues with lower Dominant Resource Share first.
  LocalQueueDRS PreemptionConfigOrderingField = "LocalQueueDRS"
)

// OrderingDirection specifies the sort direction for a candidate ordering criterion.
// Possible values are:
// - "Ascending": sort in natural ascending order (default).
// - "Descending": sort in reverse/descending order.
//
// +kubebuilder:validation:Enum=Ascending;Descending
type OrderingDirection string

const (
  // Ascending sorts candidate workloads in natural order (e.g., lowest priority first, oldest admission first, or same CQ/Cohort first).
  Ascending OrderingDirection = "Ascending"

  // Descending sorts candidate workloads in reverse order (e.g., highest priority first, newest admission first, or other CQ/Cohort first).
  Descending OrderingDirection = "Descending"
)

// PreemptionConfigOrder specifies a single sorting criterion and direction for ordering preemption candidates.
// Multiple PreemptionConfigOrder criteria are evaluated sequentially as a multi-key comparator chain,
// with ties broken by Workload UID for deterministic ordering.
type PreemptionConfigOrder struct {
  // OrderingField specifies the field to sort preemption candidates by.
  //
  // +kubebuilder:validation:Required
  OrderingField PreemptionConfigOrderingField `json:"orderingField"`

  // Direction specifies whether to sort preemption candidates in ascending or descending order.
  // Defaults to "Ascending" if not specified.
  //
  // +kubebuilder:default=Ascending
  // +optional
  Direction OrderingDirection `json:"direction,omitempty"`
}
```

As defined by [current ordering](https://github.com/kubernetes-sigs/kueue/blob/24f6f99135979076a8d56ca7fc407990b98c66af/pkg/scheduler/preemption/common/ordering.go#L34-L41),
the order is currently based on:

0. Workloads already marked for preemption first.
1. Workloads from other ClusterQueues in the cohort before the ones in the same ClusterQueue as the preemptor.
2. (AdmissionFairSharing only) Workloads with lower LocalQueue's usage first.
3. Workloads with lower priority first.
4. Workloads admitted more recently first.

Therefore, the proposed custom ordering fields were designed to cover and generalize this well.

Because DRS values depend on the dynamic state of the cluster, workloads selected for preemption directly affect the relative ordering of remaining candidates when DRS-based ordering fields (such as `ClusterQueueDRS`, `LocalQueueDRS`, or share-based criteria) are used. Consequently, candidate generation and ordering must be adjusted dynamically during evaluation. Because naive re-sorting would degrade scheduling throughput in large clusters, an optimized approach is detailed in the [[Optimized] Dynamically Adjusted Candidate Generation](#optimized-dynamically-adjusted-candidate-generation) section.

### Examples with Custom Ordering

In future work, users would be able to configure explicit candidate ordering in `PreemptionConfig` manifests:

#### Story 1 - Defragmentation with Explicit Priority Ordering

```yaml
spec:
  rules:
    - name: defrag-smaller-tpu-workloads
      activationPolicy:
        trigger: "QuotaFeasibleAndInsufficientTopology"
      candidateSelectors:
        - priority:
            mode: "Boosted"
            comparison: "LessThanOrEqual"
          scope: "AnyClusterQueue"
          numericLabels:
            - key: "tpus-count"
              comparison: "LessThan"
              fallbackValue: 0
  ordering:
    - orderingField: "Priority"
      direction: "Ascending"
```

#### Story 2 - Hero Workload with Explicit Priority Ordering

```yaml
spec:
  rules:
    - name: hero-preemption
      activationPolicy:
        trigger: "Always"
      candidateSelectors:
        - priority:
            mode: "Boosted"
            comparison: "LessThan"
          scope: "AnyClusterQueue"
  ordering:
    - orderingField: "Priority"
      direction: "Ascending"
```

## [Optimized] Dynamically Adjusted Candidate Generation

In the initial iteration, candidate workloads are gathered from both strategies into two separate sets, merged, deduplicated, and sorted in a single flat list (as detailed in [Integration](README.md#integration)). This minimizes modifications to the existing codebase during Alpha.

However, when configurable candidate ordering or quota based selectors are introduced in future the candidate generatio has to be dynamically adjusted to take into account changes of DRS or borrowing due to previous candidates preemption. Generatio of such candidates for large clusters will benefit from a more sophisticated candidate organization: **Per-Selector, Per-ClusterQueue Priority Queues** paired with iteration through priority queue heads.

#### Motivation and Architectural Benefits

Rather than pooling all candidate workloads across the cluster into a single unstructured list, the evaluator would organize candidate workloads into **separate priority queues partitioned by `(CandidateSelector, ClusterQueue)`**:

1. **Static Intra-Queue Ordering (Sort Once)**: Within any given ClusterQueue, relative candidate ordering (e.g., by Priority, `AdmissionTimestamp`, Workload UID) is static and unaffected by dynamic cluster state. Sorting each queue independently once at the start of preemption evaluation ($O(\frac{n}{c} \log \frac{n}{c})$ per queue) avoids expensive full-array re-sorting during candidate iteration.
2. **Fast CQ-Level Pruning**: Dynamic cluster properties—such as current borrowed quota—can be tracked at the queue level. When a ClusterQueue exhausts its borrowing capacity, its entire priority queue under that borrowing selector is immediately pruned from consideration.
3. **Selector Isolation**: Maintaining distinct queues per selector ensures that dropping an ineligible queue under a borrowing selector does not inadvertently discard candidates from the same ClusterQueue that remain eligible under static selectors (such as priority-only preemption within the same CQ).
4. **Deduplication & Multi-Queue Popping**: Workloads matching multiple selectors (across one or more rules) reside at the heads of multiple queues (held via shared references) and are popped simultaneously when selected. This multi-queue membership directly identifies all matching candidate selectors and rules, providing precise metadata for preemption justification in workload status conditions and audit logs (see [Observability](README.md#observability)).

#### Problem Statement

Certain preemption candidate rules—such as those based on `BorrowingCapacityFromPreemptor` or Dominant Resource Share (DRS) fair-sharing strategies—depend on dynamic cluster state that changes as candidate workloads are simulated for preemption during evaluation.

For example, consider ClusterQueues A and B, each with a nominal quota of 5. Suppose CQ B is currently borrowing 1 unit of quota from CQ A.
If a workload in CQ A triggers preemption under a rule targeting only borrowing workloads, and each candidate workload in CQ B consumes 1 unit of quota, the evaluator should only preempt a single workload from CQ B.
Once that first workload is selected, CQ B is no longer borrowing quota from CQ A, so remaining workloads in CQ B must immediately become ineligible for that borrowing rule.

Furthermore, dynamic cluster metrics (such as DRS in fair-sharing cohorts) mean that preemption eligibility and relative candidate ordering across ClusterQueues can shift after every candidate selection step.

#### Naive Solutions and Complexity Bottlenecks

Let:

- $n$: total number of candidate workloads across all ClusterQueues in the cohort.
- $c$: number of ClusterQueues in the cohort, with $c \ll n$.
- $s$: number of candidate selectors configured in `PreemptionConfig` rules, with $s \le 5$.
- $m$: number of victim workloads required to satisfy the preemptor, with $m \le n$.

Under dynamic state changes:

- **Naive Linear Filtering per Selection** (`O(m · n)` to `O(n²)`): Dynamically filtering the candidate set and linearly scanning for the minimum at each of the $m$ preemption steps requires $O(n)$ work per step, yielding $O(m \cdot n)$ time (up to $O(n^2)$ in the worst case where $m \approx n$).
- **Naive Dynamic Re-sorting** (`O(m · n log n)` to `O(n² log n)`): Naively re-sorting the candidate array whenever CQ borrowing or DRS metrics change introduces an $O(n \log n)$ sorting step per eviction, leading to $O(m \cdot n \log n)$ time and severe scheduler throughput degradation.

#### Proposed Approach: Multi-Queue Dynamic Iteration

Leveraging **Per-Selector, Per-ClusterQueue Priority Queues**, the evaluator achieves optimal scheduling performance without repetitive full-array scans or re-sorting:

1. **Static Intra-Queue Ordering (Sort Once):**
   Within any given ClusterQueue, relative candidate ordering (e.g., by Priority, `AdmissionTimestamp`, Workload UID) is static and unaffected by dynamic quota borrowing or DRS changes. Therefore, candidate workloads within each `(Selector, CQ)` queue need to be sorted only once at the start of evaluation.

2. **CQ-Level State Tracking & Fast Pruning:**
   Dynamic state—such as current borrowed quota and ClusterQueue DRS—is tracked via lightweight counters attached to each CQ queue. When a CQ property no longer satisfies the selector's criteria (e.g., borrowed quota reaches zero for borrowing selectors), the entire priority queue for that CQ under that selector is pruned from consideration.

3. **Handling Workload-Specific Constraints (`DRSLessThanOrEqualToFinalShare`):**
   For selectors requiring workload-level evaluation (such as `DRSLessThanOrEqualToFinalShare`), the entire queue cannot simply be dropped at the CQ level because eligibility depends on the individual workload's DRS value. For these selectors, candidates are evaluated at extraction time when inspected at the queue head. If a candidate violates the fair-sharing constraint under current simulated state, it is popped and discarded for that selector.

4. **Multi-Queue Head Selection:**
   At each preemption step, the evaluator inspects the heads of all active priority queues and selects the globally minimal candidate according to the configured ordering comparator chain.

5. **Deduplication & Multi-Queue Popping:**
   A single workload can match multiple candidate selectors (across one or more preemption rules) and thus reside in multiple priority queues. Because the ordering comparator is consistent across queues, the selected minimal workload will always be at the head of all its corresponding queues. When chosen, it is popped from all matching queue heads simultaneously. Workloads are stored as shared pointers/references across queues to eliminate data duplication.

6. **Simulated State Updates:**
   After popping a candidate, the evaluator updates simulated state (reclaimed quota, updated DRS counters) and drops any newly ineligible CQ priority queues before the next selection step.

#### Example Walkthrough

Consider three ClusterQueues (CQ A, CQ B, and CQ C) in a flat cohort, each with 2 admitted workloads:

- **Workloads & Priorities**:
  - Preemptor: Workload A3 in ClusterQueue A, Priority = 40.
  - Candidates in CQ A: A1 (Priority = 20), A2 (Priority = 50).
  - Candidates in CQ B: B1 (Priority = 5), B2 (Priority = 10).
  - Candidates in CQ C: C1 (Priority = 30), C2 (Priority = 60).
- **Rules & Candidate Ordering**:
  - Candidate ordering: lower priority workloads preempted first.
  - _Rule 1 (Priority-based, intra-CQ)_: Preempt workloads within the same CQ (CQ A) with strictly lower priority than the preemptor (priority < 40). Candidate matching: Workload A1 (Priority 20).
  - _Rule 2 (Fair Sharing, inter-CQ)_: Preempt workloads from any ClusterQueue whose DRS exceeds its fair share.

Now, workload A3 arrives in ClusterQueue A and requires preemption to be admitted:

1. **Queue Initialization (2 selectors × 3 ClusterQueues = 6 priority queues)**:
   - For the priority selector (Rule 1), DRS is ignored; these queues only contain workloads passing the static priority filter and intra-CQ constraint (Workload A1).
   - For the fair sharing selector (Rule 2), cohort DRS is evaluated dynamically for each CQ:
     - ClusterQueue B is borrowing and heavily exceeds fair share $\implies$ Workloads B1 and B2 are eligible.
     - ClusterQueue C is currently within its fair share $\implies$ Workloads C1 and C2 are **ineligible** under Rule 2, and do not match Rule 1 (different CQ). Thus, queues for CQ C are initially inactive/empty.

2. **Candidate Selection (Workloads B1, B2, A1)**:
   - The evaluator inspects the heads of all active priority queues and selects the candidate with the lowest priority.
   - First, it selects candidate **B1** (Priority 5), then candidate **B2** (Priority 10). As the cohort structure is flat, evicting B1 and B2 does not alter CQ C's fair-share status.
   - Next, the evaluator selects candidate **A1** (Priority 20). Because preemption within the same ClusterQueue is also considered fair under fair-sharing rules, A1 matches both Rule 1 and Rule 2, and is popped simultaneously from both queues representing ClusterQueue A.

3. **Dynamic State Recomputation & Selection of Workload C1**:
   - Simulating the preemption of A1 reduces ClusterQueue A's resource usage, which shifts the cohort fair-share baseline. Under the updated DRS values, ClusterQueue C now exceeds its fair share!
   - Consequently, the priority queue for ClusterQueue C under Rule 2 becomes active, making C1 (Priority 30) eligible for preemption.
   - The evaluator inspects active queue heads (C1 at 30 vs A2 at 50, C2 at 60) and selects candidate **C1** (Priority 30) as the lowest-priority eligible candidate.
   - _(Note: Without dynamic state recomputation, C1 would have been prematurely excluded or would have required a full scan of all cluster workloads.)_

4. **Termination**:
   - Workload A3 resource requirements can now be satisfied after selecting {B1, B2, A1, C1}. Candidate iteration terminates, and the scheduler proceeds to reverse-order backfilling.

#### Implementation Caveats and Selector Isolation

Maintaining separate priority queues per candidate selector is essential. If queues were pooled across selectors (either within a rule or across rules), dropping an ineligible CQ queue due to exhausted borrowing or DRS thresholds would inadvertently discard candidates that matched other non-borrowing, static selectors (such as priority-only preemption within the same CQ). Distinct per-selector queues permit aggressive filtering using static constraints up front while isolating dynamic state invalidation.

#### Complexity Analysis

To evaluate algorithmic efficiency under realistic cluster conditions:

- $n$: total number of candidate workloads across all ClusterQueues in the cohort.
- $c$: number of ClusterQueues in the cohort, with $c \ll n$.
- $s$: number of candidate selectors configured in the `PreemptionConfig`, with $s \le 5$.
- $m$: number of victim workloads required to admit the preemptor, with $m \le n$.

Assuming workloads are roughly evenly distributed across ClusterQueues (approximately $n/c$ workloads per queue):

1. **Queue Initialization & Sorting**:
   - The algorithm instantiates at most $s \times c$ priority queues.
   - Sorting each queue of size $n/c$ takes $O(\frac{n}{c} \log \frac{n}{c})$. Across all $s \times c$ queues:

     $$\sum_{i=1}^{s \times c} O\left(\frac{n}{c} \log \frac{n}{c}\right) = s \cdot c \cdot O\left(\frac{n}{c} \log \frac{n}{c}\right) = O\left(s \cdot n \log\left(\frac{n}{c}\right)\right)$$

   - Since $\log(n/c) \le \log n$, this is bounded by standard $O(s \cdot n \log n)$.

2. **Victim Selection & Dynamic Updates**:
   - At each selection step, finding the globally minimal candidate takes $O(c \cdot s)$ time to inspect the heads of all active queues.
   - Popping $m$ victim workloads requires $O(m \cdot c \cdot s)$ comparisons.
   - Updating simulated resource allocations and DRS values per victim takes $O(1)$ on a flat cohort structure.

3. **Overall Time Complexity**:

   $$T = O(s \cdot n \log n + m \cdot c \cdot s)$$

   Treating the number of selectors $s$ as a small constant, with $s = O(1)$, the overall complexity simplifies to:

   $$O(n \log n + m \cdot c)$$

#### Complexity Comparison

| Algorithm                              | Per-Step Selection Time     | Total Selection Time (for $m$ victims) | Overall Algorithm Time   | Scalability Bottleneck                                                        |
| -------------------------------------- | --------------------------- | -------------------------------------- | ------------------------ | ----------------------------------------------------------------------------- |
| **Naive Linear Filtering**             | $O(n)$                      | $O(m \cdot n)$                         | $O(m \cdot n)$           | High per-step scan overhead when $n$ is large.                                |
| **Naive Dynamic Re-sorting**           | $O(n \log n)$               | $O(m \cdot n \log n)$                  | $O(m \cdot n \log n)$    | Severe throughput degradation on frequent evictions.                          |
| **Proposed Per-(Selector, CQ) Queues** | $O(c \cdot s) \approx O(c)$ | $O(m \cdot c)$                         | **`O(n log n + m · c)`** | Scales with number of ClusterQueues $c$, independent of $n$ during selection. |

Because in real clusters the number of ClusterQueues is much smaller than the total number of workloads (where $c \ll n$, e.g. dozens of queues vs. thousands of workloads), where $m \cdot c \ll m \cdot n$. The multi-queue approach eliminates repetitive scans and re-sorting, ensuring scalable preemption evaluation.

#### Open Challenges

**Challenge 1** — how to handle the situation where workloads are preempted from the preemptor CQ, which makes previously removed workloads viable again — [issue #14122](https://github.com/kubernetes-sigs/kueue/issues/14122).

**Vague implementation idea** — keep track of workloads that are dropped because of DRS in the appropriate order and re-evaluate them (when a whole CQ is dropped because of DRS, save all of the workloads from it).

**Challenge 2** — how to make sure that preemptions are fair even if we backfill some workloads. The algorithm described above is fair if no backfilling is happening, but if we preempt and then backfill it can lead to issues as described in [issue #14543](https://github.com/kubernetes-sigs/kueue/issues/14543).

**Vague implementation idea** — when backfilling, hold the required values (attached to the CQs or in a cohort-tree-like struct) to make preemption of suffix workloads still fair according to the DRS rules. If backfilling changes the DRS in a way that makes the "fairness" rule no longer true for suffix workloads, then do not reintroduce them.
As stricter backfilling can lead to lower cluster utilization (a trade-off with fairness), this should probably be introduced as an additional preemption config parameter (boolean flag).
There are some additional caveats that should be addressed — for example, what if suffix candidate preemption is still possible because other non-DRS rules allow it? Then we should probably allow backfilling of the workloads, but this may lead to a change in the ordering of the candidates. For simplicity, it may be worth documenting as a known limitation that candidates are only ordered once according to the original plan and not reordered during backfilling.

## Time-Based Candidate Selectors (Execution and Creation Duration)

Filtering candidates based on workload execution duration or creation age addresses valid operational and SLA requirements, but is not deemed a must-have in the first iteration of `PreemptionConfig`. These fields are deferred to future work.

Relevant use cases include:

1. **Minimal execution duration before preemption ([Issue #9596](https://github.com/kubernetes-sigs/kueue/issues/9596)):**
   Avoid thrashing workloads that have just started by requiring candidates to have run for a minimum duration (e.g., at least 15 minutes) before being eligible for preemption.
2. **SLA protection based on workload creation time:**
   Prevent preemption of older workloads nearing completion or SLA deadlines by selecting only recently created workloads (e.g., created less than 1 hour ago) as preemption candidates.
3. **Relative execution time comparison:**
   Compare the candidate workload's runtime or creation time against the preemptor workload (e.g., only preempt workloads that have been running for shorter duration than the preemptor).

### Proposed API for Time-Based Candidate Selectors

In a future iteration, `PreemptionConfigPreemptionCandidateSelector` can be extended with the following duration and time-relation fields:

```go
type PreemptionConfigPreemptionCandidateSelector struct {
  // ... baseline candidate selector fields ...

  // Accepts any execution times if not set.
  // MinExecutionDuration specifies the minimum runtime a candidate workload must have completed.
  MinExecutionDuration *metav1.Duration `json:"minExecutionDuration,omitempty"`

  // MaxExecutionDuration specifies the maximum runtime a candidate workload can have completed.
  MaxExecutionDuration *metav1.Duration `json:"maxExecutionDuration,omitempty"`

  // ExecutionTimeRelation defines how the candidate's execution time compares to the preemptor's.
  ExecutionTimeRelation *NumericComparison `json:"executionTimeRelation,omitempty"`

  // Accepts any time from creation if not set.
  // MinTimeFromCreationDuration specifies the minimum age of the workload from creation timestamp.
  MinTimeFromCreationDuration *metav1.Duration `json:"minTimeFromCreationDuration,omitempty"`

  // MaxTimeFromCreationDuration specifies the maximum age of the workload from creation timestamp.
  MaxTimeFromCreationDuration *metav1.Duration `json:"maxTimeFromCreationDuration,omitempty"`

  // TimeFromCreationRelation defines how the candidate's creation time compares to the preemptor's.
  TimeFromCreationRelation *NumericComparison `json:"timeFromCreationRelation,omitempty"`
}
```

### Examples with Time-Based Candidate Selectors

#### Story 1 - Minimal Execution Duration Before Preemption

```yaml
spec:
  rules:
    - name: preempt-only-after-min-exec-time
      activationPolicy:
        trigger: "InsufficientQuota"
      candidateSelectors:
        - scope: "WithinClusterQueue"
          priority:
            mode: "Boosted"
            comparison: "LessThan"
          minExecutionDuration: "15m"
```

#### Story 2 - SLA Protection Based on Workload Creation Time

```yaml
spec:
  rules:
    - name: preempt-recent-workloads-only
      activationPolicy:
        trigger: "InsufficientQuota"
      candidateSelectors:
        - scope: "WithinClusterQueue"
          priority:
            mode: "Boosted"
            comparison: "LessThan"
          maxTimeFromCreationDuration: "1h"
```

## Quota-Based Candidate Selectors (PreemptionConfigQuotaConstraint)

In Alpha, candidate evaluation reuses the regular preemption ordering rules from classical preemption and fair sharing, which already take borrowing capacity and Dominant Resource Share (DRS) into account dynamically. Explicit pre-filtering of preemption candidates via a `Quota` constraint (such as `BorrowingCapacityFromPreemptor` or DRS share comparisons) is therefore not needed for Alpha and is deferred to future work.

Because borrowing and DRS values depend on the dynamic state of the cluster, workloads selected for preemption directly affect the eligibility of remaining candidates. Consequently, candidates must be adjusted dynamically during evaluation. Because a naive re-evaluation would degrade scheduling throughput in large clusters, an optimized approach is detailed in the [[Optimized] Dynamically Adjusted Candidate Generation](#optimized-dynamically-adjusted-candidate-generation) section.

### Proposed API for Quota-Based Candidate Selectors

In a future iteration, `PreemptionConfigPreemptionCandidateSelector` can be extended with the `Quota` field:

```go
// +kubebuilder:validation:Enum=BorrowingCapacityFromPreemptor;DRSLessThanOrEqualToFinalShare;DRSLessThanInitialShare;DRSAllStrategies
type PreemptionConfigQuotaConstraint string

const (
  // BorrowingCapacityFromPreemptor restricts preemption candidates to workloads
  // in other ClusterQueues within the cohort that are currently borrowing capacity from the preemptor's ClusterQueue.
  BorrowingCapacityFromPreemptor PreemptionConfigQuotaConstraint = "BorrowingCapacityFromPreemptor"

  // DRSLessThanOrEqualToFinalShare restricts preemption candidates to workloads in ClusterQueues
  // whose Dominant Resource Share after preemption remains less than or equal to their final share.
  DRSLessThanOrEqualToFinalShare PreemptionConfigQuotaConstraint = "DRSLessThanOrEqualToFinalShare"

  // DRSLessThanInitialShare restricts preemption candidates to workloads in ClusterQueues
  // whose Dominant Resource Share before preemption was less than their initial share.
  DRSLessThanInitialShare PreemptionConfigQuotaConstraint = "DRSLessThanInitialShare"

  // DRSAllStrategies allows any preemption candidates permitted under configured DRS fair-sharing strategies.
  DRSAllStrategies PreemptionConfigQuotaConstraint = "DRSAllStrategies"
)

type PreemptionConfigPreemptionCandidateSelector struct {
  // ... baseline candidate selector fields ...

  // Quota specifies quota-based preemption constraints (e.g., borrowing capacity or fair sharing share).
  // Cannot be set if Scope is WithinLocalQueue or WithinClusterQueue.
  // Accepts all if not set.
  //
  // +optional
  Quota *PreemptionConfigQuotaConstraint `json:"quota,omitempty"`
}
```

## PreemptionLimit (Rate-Limiting Guardrails)

While `PreemptionConfig` provides declarative candidate selection policies, cluster administrators also need rate-limiting guardrails to prevent cascading preemptions, eviction storms, and cluster instability during large-scale rescheduling. To maintain focus on core `PreemptionConfig` mechanics for Alpha, the `PreemptionLimit` cluster-scoped CRD is deferred to future work.

Relevant capabilities include:

1. **Global rate-limiting**: Restrict the total number of preemption events across the entire cluster within a sliding time window.
2. **Preempting ClusterQueue rate-limiting**: Throttle preemptions triggered by workloads originating from a specific ClusterQueue.
3. **Preempted ClusterQueue protection**: Limit or block preemptions targeting workloads belonging to a specific ClusterQueue (e.g., setting `limit: 0` to make mission-critical or hero queues non-preemptible).
4. **Preempted Workload churn limiting**: Restrict how many times an individual workload can be preempted within a given time window to avoid starvation or ping-pong eviction loops.

### Proposed API for PreemptionLimit

In a future iteration, `PreemptionLimit` will be introduced as a cluster-scoped CRD:

```go
type PreemptionLimit struct {
  metav1.TypeMeta `json:",inline"`
  metav1.ObjectMeta `json:"metadata,omitempty"`
  Spec PreemptionLimitSpec `json:"spec,omitempty"`
  Status PreemptionLimitStatus `json:"status,omitempty"`
}

// +kubebuilder:validation:Enum=Global;PreemptingClusterQueue;PreemptedClusterQueue;PreemptedWorkload
type PreemptionLimitScope string
const (
  GlobalPreemptionLimitScope PreemptionLimitScope = "Global"
  PreemptingCQLimitScope PreemptionLimitScope = "PreemptingClusterQueue"
  PreemptedCQLimitScope PreemptionLimitScope = "PreemptedClusterQueue"
  PreemptedWorkloadLimitScope PreemptionLimitScope = "PreemptedWorkload"
)

type PreemptionLimitSpec struct {
  // Scope specifies the entity boundary for this preemption limit.
  //
  // +kubebuilder:validation:Required
  Scope PreemptionLimitScope `json:"scope"`

  // ConfigSelector selects PreemptionConfigs to which this limit applies.
  // If not set, it applies to all PreemptionConfigs.
  //
  // +optional
  ConfigSelector *metav1.LabelSelector `json:"configSelector,omitempty"`

  // ClusterQueueSelector selects ClusterQueues to which this limit applies.
  // If not set, it applies to all ClusterQueues under the configured scope.
  //
  // +optional
  ClusterQueueSelector *metav1.LabelSelector `json:"clusterQueueSelector,omitempty"`

  // RuleNames restricts the limit to specific rule names within matching PreemptionConfigs.
  // If not set, it applies to all rules.
  //
  // +optional
  RuleNames []string `json:"ruleNames,omitempty"`

  // Limit defines how many preemption events can occur within the given time window.
  // An event is defined as a confirmed (preemptor, preemptee) eviction pair.
  // Setting Limit to 0 blocks all preemptions under this limit's scope.
  //
  // +kubebuilder:validation:Required
  // +kubebuilder:validation:Minimum=0
  Limit int32 `json:"limit"`

  // LimitWindowDuration specifies the sliding time window duration.
  // Must be greater than or equal to 1s to prevent sub-second thrashing.
  //
  // +kubebuilder:validation:Required
  // +kubebuilder:validation:XValidation:rule="self >= duration('1s')",message="must be at least 1s"
  LimitWindowDuration metav1.Duration `json:"limitWindowDuration"`
}

type PreemptionLimitStatus struct {
  Conditions []metav1.Condition `json:"conditions,omitempty"`

  // Periodically updated, for reference only.
  // Map key depends on the scope. For Global it is just Global.
  // For CQ it is ClusterQueue name.
  // For Workload it is namespace + "/" + workload name.
  // Restricted to the top 1000 counts to fit within CRD size limits.
  Count map[string]int32 `json:"count,omitempty"`
}
```

PreemptionLimit limits the number of preemptions that happen for the specified set of rules. The preemption evaluator evaluates proposed preemptions against defined limit objects, allowing them to proceed only if adequate preemption quota remains. If a preemption is in the scope of multiple limits, quota must exist in all of them.
To track this, a list of preemption rule names responsible for selecting each candidate must be maintained.

To manage this data, Kueue will store a comprehensive preemption map in memory, isolated per PreemptionLimit. This map tracks all preemption event timestamps under a specific CQ/workload key, capturing events that occurred within the designated `LimitWindowDuration`. Moreover, it tracks only events that are in the scope of the specific limit; if a preemption does not match the defined config or rules selector, it will not be tracked in that particular instance of the preemption map. This list is dynamically trimmed upon each retrieval to filter out expired timestamps.

Furthermore, the status of the PreemptionLimit is refreshed periodically — approximately every minute — to write the aggregated totals into the count map (restricted to the top 1000 counts to fit within CRD size limits).

### Observability When Reaching Preemption Limits

When preemption is throttled or blocked due to an exhausted `PreemptionLimit`:

1. **Workload Condition**: A condition with type `PreemptionBlockedByLimit` (reason `PreemptionLimitExceeded`) is assigned to the preemptor Workload, with an informative message indicating which limit blocked admission (e.g. `"Preemption was blocked by PreemptionLimit <limit-name>"`).
2. **Kubernetes Events**: A Kubernetes `Event` with reason `PreemptionThrottled` is emitted on both the preemptor Workload and its ClusterQueue.
3. **Structured Audit Logging**: Informational/debug log entries are recorded specifying the limit name, scope, and affected entities for operator troubleshooting.

### Examples with PreemptionLimit

#### Story 1 - Global Preemption Rate Limiting

Rate-limit global preemptions to at most 10 evictions across the entire cluster in any 5-minute sliding window:

```yaml
spec:
  scope: "Global"
  limit: 10
  limitWindowDuration: "5m"
```

#### Story 2 - Protecting a Mission-Critical ClusterQueue from Preemption

Ensure that workloads running in the `hero-cq` ClusterQueue can never be preempted by setting `limit: 0` under the `PreemptedClusterQueue` scope:

```yaml
spec:
  scope: "PreemptedClusterQueue"
  clusterQueueSelector:
    matchLabels:
      kueue.x-k8s.io/queue-name: "hero-cq"
  limit: 0
  limitWindowDuration: "1h"
```

## Minimum Trigger Duration (MinTriggerRequiredDuration)

In many production environments, administrators want to avoid premature or "flapping" preemptions caused by transient quota shortages or temporary topology fragmentation that might resolve naturally within a short window (e.g., as short jobs complete or as autoscaling nodes join). By requiring that a trigger condition (such as `QuotaFeasibleAndInsufficientTopology` or `InsufficientQuota`) persists for a minimum duration before evaluating candidate preemptions, clusters can grant a grace window for normal placement or natural workload completions before resorting to disruptive evictions.

In the initial Alpha release, preemption evaluation triggers immediately upon observing the trigger condition without timer-based requeueing, keeping the execution flow synchronous with scheduling passes and avoiding timer management complexity. In future iterations, `PreemptionConfigActivationPolicy` will be extended with `minTriggerRequiredDuration`.

### Proposed API for Minimum Trigger Duration

```go
type PreemptionConfigActivationPolicy struct {

  // trigger specifies the condition (InsufficientQuota, Always, or QuotaFeasibleAndInsufficientTopology)
  // that must be observed on the preemptor workload for this rule to apply.
  Trigger PreemptionConfigActivationTrigger `json:"trigger"`

  // MinTriggerRequiredDuration specifies how long the trigger condition must be observed before
  // preempting workloads specified by candidateSelectors. 0s indicates that preemptions can be started immediately.
  // Defaults to 0s.
  //
  // +optional
  // +kubebuilder:default="0s"
  MinTriggerRequiredDuration metav1.Duration `json:"minTriggerRequiredDuration,omitempty"`
}
```

When `minTriggerRequiredDuration` is configured with a duration greater than `0s`:

- When an active trigger is first observed on an unadmitted workload, its observation timestamp is stored in the in-memory cache, and the workload is moved to `inadmissibleWorkloads`.
- An in-memory timer is scheduled to requeue the workload back to the active queue once the required duration elapses.
- During scheduling cycles, `PreemptionEvaluator` validates whether `now - inMemoryObserved >= minTriggerRequiredDuration` before considering the rule eligible for candidate selection.

### Examples with Minimum Trigger Duration

#### Story 1 - Grace Period for Topology Defragmentation

Delay defragmentation preemption by 30 seconds to give running workloads time to finish or allow the cluster autoscaler to provision a suitable topology domain before evicting smaller workloads:

```yaml
spec:
  rules:
    - name: defrag-smaller-tpu-workloads
      activationPolicy:
        trigger: "QuotaFeasibleAndInsufficientTopology"
        minTriggerRequiredDuration: "30s"
      candidateSelectors:
        - priority:
            mode: "Boosted"
            comparison: "LessThanOrEqual"
          scope: "AnyClusterQueue"
          numericLabels:
            - key: "tpus-count"
              comparison: "LessThan"
              fallbackValue: 0
```

Alternative - Persisting trigger conditions directly on the Workload API object via status condition patches (`Workload.Status.Conditions`).
Ruled out because:

- **Informer Watch Latency & Desync**: Writing a condition to etcd via `PatchAdmissionStatus()` and waiting for the informer watch event to update the scheduler's local cache introduces significant latency (tens of milliseconds) compared to the sub-millisecond scheduling cycle. If a workload is requeued immediately for evaluation in the next cycle, the scheduler pops the stale, unpatched object from cache, leading to scheduling failures, high latency, or race conditions.
- **Duplicate API Patches & Conflicts**: Because informer watch delivery is asynchronous, re-queuing the workload immediately while the watch event is in flight causes the scheduler to re-evaluate the workload repeatedly against stale cache state, generating duplicate status patch requests and triggering API server conflict errors (`409 Conflict`).
- **Inadmissible Trapping vs. Infinite Busy-Loops**: If workloads requiring preemption were marked inadmissible after setting the condition, they would become stuck in `inadmissibleWorkloads` indefinitely because informer condition updates only update inadmissible workloads in place without re-queuing them to the active heap (unless an unrelated cluster event triggers `QueueInadmissibleWorkloads`). Conversely, keeping them in the active queue without conditions causes infinite busy-loops when preemption candidates do not exist in the cluster.
- **etcd Churn & Scalability**: Updating status conditions in etcd on every unadmitted scheduling pass creates severe write amplification and API server pressure, particularly in busy clusters with high workload arrival rates and short scheduling intervals.
- **Conclusion**: Maintaining triggers and observation timestamps in-memory within the queue management and scheduler cache eliminates informer watch latency, avoids etcd write churn and duplicate API patches, and allows immediate requeuing to the active heap (while laying the groundwork for timer-based requeuing for deferred `MinTriggerRequiredDuration` rules in future iterations).

## Per-Node DRA Device Feasibility Trigger (InsufficientDRADevices)

Dynamic Resource Allocation (DRA) enables fine-grained hardware device requests via ResourceClaims. While aggregate device counts defined via `ResourceClaimTemplates` are already mapped to Kueue flavors and protected by `InsufficientQuota`, per-node device feasibility presents unique challenges for preemption:

1. **Lack of Device Allocation Tracking in Kueue**:
   ResourceClaims are bound to specific physical devices by `kube-scheduler` and DRA device drivers during Pod scheduling, not by Kueue. Consequently, Kueue does not track device-to-workload bindings or device identities in its in-memory cache.

2. **The Problem with Blind Preemption**:
   Preemption triggers require causal determinism: evicting candidate workloads must reliably satisfy the preemptor's unmet requirement. If Kueue triggers preemption for a workload failing per-node device feasibility without knowing which running workloads occupy the required devices, it would have to select candidates blindly. Evicting a candidate that does not hold the needed device would disrupt workloads without allowing the preemptor to admit, leading to cascading, fruitless evictions.

3. **Separation from Topology-Aware Scheduling (TAS)**:
   Per-node device feasibility constraints are fundamentally distinct from TAS topology domains (racks, blocks). Folding device feasibility under `InsufficientTopology` would break the semantic clarity of TAS rules and cause rules written for rack/block defragmentation to trigger unwanted evictions on device constraint failures.

### Future Evolution

Once Kueue or the Kubernetes DRA ecosystem supports tracking ResourceClaim allocation status and device identities, a dedicated trigger can be introduced:

```go
const (
  // InsufficientDRADevices indicates that aggregate quota is available, but per-node
  // DRA device feasibility constraints cannot be satisfied without preempting workloads
  // holding the required device instances.
  InsufficientDRADevices PreemptionConfigActivationTrigger = "InsufficientDRADevices"
)
```

With device identity modeling, the preemption simulation will be able to verify that evicting a specific workload frees the exact device(s) or device topology needed by the incoming claim before any eviction is executed.

To make the candidates selection more efficient the selectors and ordering may be also extended to choose only workloads that hold the required device as candidates or to prioritize preemption of those.
