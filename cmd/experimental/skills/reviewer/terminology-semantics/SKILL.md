---
name: terminology-semantics
description: Review that code and events use terminology whose semantics match the operation (e.g., not Preempted for migration).
---

# Skill: Terminology Must Match Semantics

**Flag:** Code, events, status conditions, or log messages borrow a term from an adjacent concept when the semantics differ. Canonical example: using `Preempted` / `IssuePreemptions` for workload migration or topology-driven eviction that does not actually preempt another workload.

**Ask:** Use terminology that accurately reflects the operation. In Kueue, "preemption" means evicting a lower-priority workload to admit a higher-priority one. Other eviction causes (flavor migration, node replacement, workload slices) are distinct operations — labelling them `Preempted` produces misleading events and logs for operators debugging in production. Introduce a new reason constant (e.g., `FlavorMigration`) rather than reusing an existing one with different semantics.

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
