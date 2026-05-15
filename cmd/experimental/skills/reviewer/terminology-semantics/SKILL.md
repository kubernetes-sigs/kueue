---
name: terminology-semantics
description: Review that code and events use terminology whose semantics match the operation (e.g., not Preempted for migration).
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Terminology Must Match Semantics

**Flag:** Code, events, status conditions, or log messages borrow a term from an adjacent concept when the semantics differ. Canonical example: using `Preempted` / `IssuePreemptions` for workload migration or topology-driven eviction that does not actually preempt another workload.

**Ask:** Use terminology that accurately reflects the operation. In Kueue, "preemption" means evicting a lower-priority workload to admit a higher-priority one. Other eviction causes (flavor migration, node replacement, workload slices) are distinct operations — labelling them `Preempted` produces misleading events and logs for operators debugging in production. Introduce a new reason constant (e.g., `FlavorMigration`) rather than reusing an existing one with different semantics.
