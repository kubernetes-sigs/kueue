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

{{< feature-gates-removed-table >}}
