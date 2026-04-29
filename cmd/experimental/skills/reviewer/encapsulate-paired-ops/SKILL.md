---
name: encapsulate-paired-ops
description: Review that paired operations and fields that always appear together are encapsulated in a helper or nested struct.
---

# Skill: Encapsulate Paired Operations and Fields

**Flag (operations):** Two function calls that always appear together (e.g., `subtractPendingResources` + `inadmissibleWorkloads.delete`, or `addPendingResources` + `inadmissibleWorkloads.insert`).

**Ask:** Wrap them in a single helper so callers cannot forget one half. The invariant should be enforced by the API, not by convention.

**Flag (fields):** Two struct fields that must always be set and cleared together (e.g., `inflight` + `inflightResources`).

**Ask:** Encapsulate them in a nested struct so they are updated atomically. This eliminates the "what if field A is nil but field B is not" inconsistency.

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
