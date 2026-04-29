---
name: metrics-label-sets
description: Review that new ClusterQueue metrics use the same label set as existing ClusterQueue metrics.
---

# Skill: New Metrics Must Match Existing Label Sets

**Flag:** A new ClusterQueue metric that includes labels (e.g., `cohort`) that existing ClusterQueue metrics don't have.

**Ask:** Other ClusterQueue metrics don't carry a `cohort` label because the semantics are ambiguous (immediate parent vs. any ancestor — tracked in issue #7539). Start with the same label set as existing metrics and open a follow-up issue for the extension.

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
