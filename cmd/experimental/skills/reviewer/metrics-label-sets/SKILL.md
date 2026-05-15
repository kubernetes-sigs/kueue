---
name: metrics-label-sets
description: Review that new ClusterQueue metrics use the same label set as existing ClusterQueue metrics.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: New Metrics Must Match Existing Label Sets

**Flag:** A new ClusterQueue metric that includes labels (e.g., `cohort`) that existing ClusterQueue metrics don't have.

**Ask:** Other ClusterQueue metrics don't carry a `cohort` label because the semantics are ambiguous (immediate parent vs. any ancestor — tracked in issue #7539). Start with the same label set as existing metrics and open a follow-up issue for the extension.
