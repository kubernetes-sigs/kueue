---
title: "Feature Gates (removed)"
linkTitle: "Feature Gates (removed)"
weight: 10
---

This page contains list of feature gates that have been removed. The information on this page is for reference. 
A removed feature gate is different from a GA'ed or deprecated one in that a removed one is no longer recognized 
as a valid feature gate. However, a GA'ed or a deprecated feature gate is still recognized by the corresponding 
Kueue components although they are unable to cause any behavior differences in a cluster.

For feature gates that are still recognized by the Kueue components, please refer to the 
[Alpha/Beta](/docs/installation/#feature-gates-for-alpha-and-beta-features) feature gate table or the 
[Graduated/Deprecated](/docs/installation/#feature-gates-for-graduated-or-deprecated-features) feature gate table

### Feature gates that are removed

In the following table:

- The "From" column contains the Kueue release when a feature is introduced or its release stage is changed.
- The "To" column, if not empty, contains the last Kueue release in which you can still use a feature gate. 
  If the feature stage is either "Deprecated" or "GA", the "To" column is the Kueue release when the feature 
  is removed.

| Feature                           | Default | Stage      | From | To   |
|-----------------------------------|---------|------------|------|------|
| `AdmissionCheckValidationRules`   | `false` | Deprecated | 0.9  | 0.12 |
| `KeepQuotaForProvReqRetry`        | `false` | Deprecated | 0.9  | 0.12 |
| `MultiplePreemptions`             | `false` | Alpha      | 0.8  | 0.8  |
| `MultiplePreemptions`             | `true`  | Beta       | 0.9  | 0.9  |
| `MultiplePreemptions`             | `true`  | GA         | 0.10 | 0.13 |
| `WorkloadResourceRequestsSummary` | `false` | Alpha      | 0.9  | 0.10 |
| `WorkloadResourceRequestsSummary` | `true`  | Beta       | 0.10 | 0.11 |
| `WorkloadResourceRequestsSummary` | `true`  | GA         | 0.11 | 0.13 |
| `QueueVisibility`                 | `false` | Alpha      | 0.5  | 0.9  |
| `QueueVisibility`                 | `false` | Deprecated | 0.9  | 0.14 |
| `ManagedJobsNamespaceSelector`    | `true`  | Beta       | 0.10 | 0.13 |
| `ManagedJobsNamespaceSelector`    | `true`  | GA         | 0.13 | 0.15 |
| `ProvisioningACC`                 | `false` | Alpha      | 0.5  | 0.6  |
| `ProvisioningACC`                 | `true`  | Beta       | 0.7  | 0.14 |
| `ProvisioningACC`                 | `true`  | GA         | 0.14 | 0.15 |
| `ExposeFlavorsInLocalQueue`       | `false` | Beta       | 0.9  | 0.15 |
| `ExposeFlavorsInLocalQueue`       | `false` | Deprecated | 0.15 | 0.15 |
