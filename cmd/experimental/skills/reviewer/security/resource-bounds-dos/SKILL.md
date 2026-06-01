---
name: resource-bounds-dos
description: Flag missing resource bounds / DoS resistance — uncapped loops or allocations, missing timeouts, reconciler-wedging input, unbounded metric cardinality (CWE-400, CWE-770).
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Resource Bounds / DoS Resistance (CWE-400, CWE-770)

- Loops over user-supplied slices (`PodSets`, `ResourceGroups`, `Flavors`, cohort
  children, admission checks) without a documented webhook-enforced cap or explicit
  length check.
- `make([]T, n)` / `make(map[K]V, n)` where `n` comes from user data without a sanity
  cap.
- Outbound API or HTTP calls without `context.WithTimeout` (or a bounded inherited
  reconcile context).
- Invalid `.spec.interval`-style fields that could wedge the reconciler for the entire
  kind (CVE-2022-39272 pattern). Failures from invalid CR content must surface to
  status, not crash-loop.
- New metrics with unbounded label cardinality — `workload_name`, `pod_name`,
  namespace+name composites, annotation values as labels.
