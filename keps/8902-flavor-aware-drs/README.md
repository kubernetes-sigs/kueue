# KEP-8902: Flavor-Aware Dominant Resource Share (DRS)

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Overview](#overview)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API change](#api-change)
    - [Why not choose a single cost/weight per flavor instead of having different weights per resource?](#why-not-choose-a-single-costweight-per-flavor-instead-of-having-different-weights-per-resource)
  - [Weighted borrowing and lendable](#weighted-borrowing-and-lendable)
    - [Effect on dominant resource selection](#effect-on-dominant-resource-selection)
  - [Validation and defaults](#validation-and-defaults)
  - [Backward compatibility](#backward-compatibility)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Single weight per ResourceFlavor](#single-weight-per-resourceflavor)
  - [Weighting elsewhere (ClusterQueue, ResourceGroup)](#weighting-elsewhere-clusterqueue-resourcegroup)
<!-- /toc -->

## Summary

This KEP proposes extending Fair Sharing to account for heterogeneous ResourceFlavors when computing Dominant Resource Share (DRS). This is done by applying configurable per-(flavor, resource) weights to borrowing/lendable aggregation.

## Motivation

Today, DRS aggregates borrowing by resource type across flavors, so borrowing 500 cheap a10-spot GPUs increases a ClusterQueue’s nvidia.com/gpu DRS the same way as borrowing 500 premium h100-reserved GPUs. As a result, a ClusterQueue that opportunistically borrows spot/cheap GPUs can be deprioritized for admission and lose reclaim/preemption opportunities when it later tries to run within its nominal quota on premium/reserved GPUs (as in User Story 1).

### Goals

- Allow administrators to express relative value/cost differences between flavors for a given resource type.
- Allow configuring weights per-(flavor, resource) so that only selected resources (for example `nvidia.com/gpu`) are treated as premium while CPU/memory can remain unweighted.
- Make DRS and Fair Sharing decisions reflect the configured per-(flavor, resource) value differences when comparing resource shares across flavors.
- Preserve existing behavior when weights are not configured (default multiplier = 1.0).
- Keep the algorithm deterministic (stable tie-breaking remains unchanged).

### Non-Goals

- Change quota semantics (nominal/borrowing/lending limits) outside of how DRS is computed.
- Introduce cross-resource-type weighting (for example favoring GPUs over CPU/memory); this KEP only differentiates **flavors within a resource type**.

## Proposal

### Overview

- **API**: Add `ResourceFlavor.spec.resourceWeights`, a map from resource name (for example `nvidia.com/gpu`) to a **scalar multiplier**. This lets admins express that some flavors are more valuable than others for a given resource type (for example `h100-reserved` vs `a10-spot` GPUs).
- **Behavior**: When computing DRS, apply the configured multiplier for each $(ResourceFlavor, resource)$ pair so that borrowing on more valuable flavors contributes more to the computed share than borrowing on cheaper flavors. This affects behavior **wherever DRS is used**, including **admission ordering** and **Fair Sharing preemption**.
- **Backward compatibility**: When weights are unset, the default multiplier is $1.0$, preserving existing behavior.
- **Details**: Full API spec change, DRS definitions and formulas are described in **Design Details**.

### User Stories (Optional)

#### Story 1

As a cluster admin managing heterogeneous GPUs, I want borrowing of cheap/opportunistic GPU capacity to contribute less to Fair Sharing DRS than borrowing of scarce/premium GPU capacity.

**Setup**

- A cohort has two ResourceFlavors that provide `nvidia.com/gpu` capacity:
  - `h100-reserved`: 100 GPUs (premium, scarce)
  - `a10-spot`: 500 GPUs (cheap, opportunistic)
- There are two ClusterQueues (CQs), one per team:

| ClusterQueue | `h100-reserved` nominalQuota | `a10-spot` nominalQuota | `a10-spot` flavor & borrowing |
| --- | ---: | ---: | --- |
| Team-A | 10 | 0 | Has `a10-spot` flavor; can borrow all available |
| Team-B | 90 | 0 | No `a10-spot` flavor |
- Assumptions: Fair Sharing is enabled, and cohort preemption is configured to allow reclaiming nominal quota as described below.

**Problem I see with today’s flavor-agnostic DRS**

Team-A can opportunistically borrow many `a10-spot` GPUs, but later wants to schedule within its nominal quota on the premium `h100-reserved` flavor. Because DRS is flavor-agnostic, borrowing cheap/spot GPUs increases the `nvidia.com/gpu` share the same way as borrowing premium/reserved GPUs.

This can (a) deprioritize Team-A in Fair Sharing admission ordering when competing for `h100-reserved`, and (b) reduce its ability to reclaim `h100-reserved` via Fair Sharing preemption even when it is trying to stay within nominal quota.

**Desired outcome**

Borrowing cheap/spot GPU capacity should contribute less to DRS than borrowing premium/reserved GPU capacity, so that opportunistic use of `a10-spot` does not penalize using `h100-reserved` within nominal quota.


### Risks and Mitigations

- **Risk**: Misconfiguration (extreme weights) can lead to surprising dominant-resource choices and more aggressive preemption for specific resources.
  - **Mitigation**: Validate weights are > 0 and expand documentation with configuration guidelines, dominant-resource examples, and recommended starting points.
- **Risk**: Changing DRS semantics changes preemption ordering when weights are configured.
  - **Mitigation**: The change is opt-in via `resourceWeights` and defaults to no-op.

## Design Details

### API change

Extend `ResourceFlavorSpec` with a new optional field:

- `spec.resourceWeights`: a map from resource name (for example `nvidia.com/gpu`) to a multiplier (a positive scalar weight). The name `resourceWeights` is chosen to be consistent with the naming used in Admission Fair Sharing.
  - **Type**: `map[corev1.ResourceName]resource.Quantity` (serialized as a string quantity), so decimals are supported (for example `"8"`, `"0.5"`).
  - Values are treated as **scalar multipliers** (not resource amounts).
- A missing map or missing entry implies a multiplier of **1.0** for that (flavor, resource) pair.

Example:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ResourceFlavor
metadata:
  name: h100-gpu-reserved
spec:
  nodeLabels:
    accelerator: nvidia-h100
  resourceWeights:
    nvidia.com/gpu: "8"
    cpu: "1"
    memory: "1"
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: ResourceFlavor
metadata:
  name: a10-gpu-spot
spec:
  nodeLabels:
    accelerator: nvidia-a10
  resourceWeights:
    nvidia.com/gpu: "1"
    cpu: "1"
    memory: "1"
```

#### Why not choose a single cost/weight per flavor instead of having different weights per resource?

DRS (and the “dominant resource” choice) is computed **per resource type** (`cpu`, `memory`, `nvidia.com/gpu`, …). A single scalar per flavor would multiply *all* resources in that flavor equally, which can distort fairness for resources whose value does **not** differ across flavors.

For example, a cluster may want to treat *GPUs* as premium/scarce while treating *CPU/memory* as roughly comparable across the same set of flavors. If a flavor had a single weight of 8 to reflect “H100 GPUs are 8× more valuable”, then CPU and memory accounted under that flavor would also be treated as 8× more valuable, incorrectly influencing DRS and potentially changing which resource becomes dominant. Per-resource weights let admins express that GPU and CPU/memory should scale differently, and keep DRS meaningful for each resource type.

### Weighted borrowing and lendable

Let:

- $f$ be a ResourceFlavor
- $r$ be a resource type (for example `nvidia.com/gpu`)
- $w(f, r)$ be the configured weight (default 1.0)
- $borrowed(f, r)$ be the amount borrowed for flavor-resource pair $(f, r)$
- $lendable(f, r)$ be the lendable amount for $(f, r)$ in the cohort tree

For each resource type $r$, today’s flavor-agnostic aggregation is:

$$borrowed(r) = \sum_{f} \max(0, borrowed(f, r))$$

$$lendable(r) = \sum_{f} lendable(f, r)$$

With flavor-aware weights, we instead compute:

$$weightedBorrowed(r) = \sum_{f} \max(0, borrowed(f, r)) \times w(f, r)$$

$$weightedLendable(r) = \sum_{f} lendable(f, r) \times w(f, r)$$

Then DRS uses the same “dominant resource” concept as today:

- Compute $ratio(r) = weightedBorrowed(r) / weightedLendable(r)$ (if $w(f, r)$ is constant across all flavors $f$ for a given $r$, this reduces to $borrowed(r) / lendable(r)$)
- Pick the resource $r$ with the maximum ratio as the dominant resource (alphabetical tie-break)

#### Effect on dominant resource selection

Because weights change $ratio(r)$, they can change which resource has the maximum ratio and therefore becomes dominant. Since DRS comparisons use the dominant resource’s ratio, this can change behavior wherever DRS is used, including admission ordering and Fair Sharing preemption.

**Example A: CPU weights fixed at 1.0, GPU flavor weights differ**

Inputs:

| Resource | Flavor | Borrowed | Lendable | Weight |
| --- | --- | ---: | ---: | ---: |
| `cpu` | `standard-cpu` | 300 | 1000 | 1.0 |
| `nvidia.com/gpu` | `h100-reserved` | 100 | 100 | 8.0 |
| `nvidia.com/gpu` | `a10-spot` | 0 | 1000 | 1.0 |

Computed ratios:

| Ratio | Value |
| --- | ---: |
| $ratio(cpu)$ | $300/1000 = 0.30$ |
| Unweighted $ratio(gpu)$ | $(100+0)/(100+1000)=0.091$ |
| Weighted $ratio(gpu)$ | $(100\times8 + 0\times1)/(100\times8 + 1000\times1)=0.444$ |

Dominant resource changes from `cpu` (unweighted) to `nvidia.com/gpu` (weighted). If GPU weights are constant across GPU flavors (for example both `8.0`), they cancel and GPU ratio does not change.

**Example A2: Same weights as Example A, but borrowing is mostly `a10-spot`**

Inputs:

| Resource | Flavor | Borrowed | Lendable | Weight |
| --- | --- | ---: | ---: | ---: |
| `cpu` | `standard-cpu` | 300 | 1000 | 1.0 |
| `nvidia.com/gpu` | `h100-reserved` | 10 | 100 | 8.0 |
| `nvidia.com/gpu` | `a10-spot` | 400 | 1000 | 1.0 |

Computed ratios:

| Ratio | Value |
| --- | ---: |
| $ratio(cpu)$ | $300/1000 = 0.30$ |
| Unweighted $ratio(gpu)$ | $(10+400)/(100+1000)=0.373$ |
| Weighted $ratio(gpu)$ | $(10\times8 + 400\times1)/(100\times8 + 1000\times1)=0.267$ |

With the same weights, borrowing more `a10-spot` and less `h100-reserved` yields a lower weighted GPU ratio ($0.267$ vs $0.444$ in Example A), which is the intended effect.

**Example B: CPU and GPU flavor weights both vary**

Inputs:

| Resource | Flavor | Borrowed | Lendable | Weight |
| --- | --- | ---: | ---: | ---: |
| `cpu` | `cpu-premium` | 250 | 300 | 3.0 |
| `cpu` | `cpu-standard` | 50 | 700 | 1.0 |
| `nvidia.com/gpu` | `h100-reserved` | 100 | 100 | 8.0 |
| `nvidia.com/gpu` | `a10-spot` | 0 | 1000 | 1.0 |

Computed ratios:

| Ratio | Value |
| --- | ---: |
| Unweighted $ratio(cpu)$ | $(250+50)/(300+700)=0.30$ |
| Weighted $ratio(cpu)$ | $(250\times3 + 50\times1)/(300\times3 + 700\times1)=0.50$ |
| Weighted $ratio(gpu)$ | $(100\times8 + 0\times1)/(100\times8 + 1000\times1)=0.444$ |

With CPU weights, `cpu` becomes dominant again ($0.50 > 0.444$). This is why CPU/memory weights should only be used when their cross-flavor value differences are intentional.

Guidelines:

- Prefer weights that capture relative flavor value for the same resource type (for example `nvidia.com/gpu`).
- Start with moderate multipliers and validate dominant-resource outcomes in a staging cohort before production rollout.
- If you set the same multiplier across all flavors for a resource type, that resource's ratio is unchanged (weights cancel).
- Avoid using CPU/memory weights as a proxy for GPU value unless you intentionally want CPU/memory to influence dominance and Fair Sharing decisions.
- Document the intent for each configured multiplier (for example reserved vs spot flavors) so outcomes remain interpretable.


### Validation and defaults

- Default multiplier is 1.0 (per (flavor, resource) pair).
- Values must be strictly greater than 0 (reject 0 / negative).

### Backward compatibility

- Existing clusters with no `resourceWeights` configured observe identical behavior.
- The field is optional; upgrades do not require changes to existing manifests.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to existing tests to make this code solid enough prior to committing the changes necessary to implement this enhancement.

#### Unit Tests

- Extend unit coverage for weighted DRS in `pkg/cache/scheduler/fair_sharing_test.go` to cover:
  - multiple flavors for the same resource type with different weights
  - multi-resource workloads where only a subset of resources are weighted
  - backward compatibility (no weights => unchanged outcomes)

#### Integration tests

- Add an integration/e2e scenario where two ClusterQueues (CQs) borrow the same number of GPUs from different flavors and verify:
  - admission ordering within the cohort prefers the CQ with lower *weighted* DRS
  - Fair Sharing preemption ordering matches the configured weights

### Graduation Criteria

#### Alpha

- API field available, documented, and defaulted to no-op (1.0).
- DRS implementation weighted as described; unit tests added.
- Integration/e2e coverage for at least one representative heterogeneous-flavor scenario.
- This change is opt-in via ResourceFlavor.spec.resourceWeights; when unset, behavior is unchanged. So, no feature gate is required.

#### Beta

- Documentation includes configuration guidance and examples.
- Integration test added for a few more representative examples.

## Implementation History

- 2026-02-15: Initial draft.

## Drawbacks

- Adds additional configuration surface area to ResourceFlavors.
- DRS becomes slightly more complex to reason about when weights are configured.

## Alternatives

### Single weight per ResourceFlavor

A single scalar per flavor cannot express “premium GPU but standard CPU/memory” flavors
without inflating CPU/memory borrow/lendable calculations. Per-resource weights are needed to avoid distorting dominance for other resources when only a subset (for example GPUs) should be treated as premium.

### Weighting elsewhere (ClusterQueue, ResourceGroup)

We want to keep configuration simple and consistent, so weights are specified per ResourceFlavor and apply cluster-wide. This makes it natural and easy to express the value of each flavor across all ClusterQueues.
