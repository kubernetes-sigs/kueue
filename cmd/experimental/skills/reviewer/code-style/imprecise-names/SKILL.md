---
name: imprecise-names
description: Flag identifiers whose name does not describe exactly what they contain — drift from package patterns, contradictory contents, misnamed feature gates, asymmetric mirror fields.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Imprecise Names

**Imprecise names** — every new identifier must describe exactly what it contains. A
reader should not be able to mistake it for something broader or narrower.

- Builder/wrapper methods that drift from the package's existing pattern (e.g. adding
  `AddLabel(k, v)` when the rest of the package uses `Label(k, v)`).
- Variables whose contents contradict the name — e.g. a set called
  `noMoreBorrowingCohorts` that actually contains cohorts which *are* still borrowing.
- Feature gates whose name does not match what they actually gate. A gate named
  `SkipFinalizersForServingWorkloads` that controls a different condition is a hard
  naming violation.
- Asymmetric mirror fields on a CRD — if `excludeResourcePrefixes` exists, the
  complement must be `includeResourcePrefixes`, not a freshly invented name.
