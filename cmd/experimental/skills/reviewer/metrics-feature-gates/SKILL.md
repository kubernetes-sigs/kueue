---
name: metrics-feature-gates
description: Review that new metrics are gated by the same feature flags as similar existing metrics.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: New Metrics Must Be Gated by Existing Feature Flags

**Flag:** A new metric similar to existing ClusterQueue resource metrics, where it's not obvious that `EnableClusterQueueResources` (see `apis/config/v1beta2/configuration_types.go`) controls it.

**Ask:** Make the feature flag gating explicit in code. If similar metrics are gated, this one should be too.
