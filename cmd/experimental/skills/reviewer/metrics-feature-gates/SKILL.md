---
name: metrics-feature-gates
description: Review that new metrics are gated by the same feature flags as similar existing metrics.
---

# Skill: New Metrics Must Be Gated by Existing Feature Flags

**Flag:** A new metric similar to existing ClusterQueue resource metrics, where it's not obvious that `EnableClusterQueueResources` (see `apis/config/v1beta2/configuration_types.go`) controls it.

**Ask:** Make the feature flag gating explicit in code. If similar metrics are gated, this one should be too.

<!--
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->
