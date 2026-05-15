---
name: encapsulate-paired-ops
description: Review that paired operations and fields that always appear together are encapsulated in a helper or nested struct.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Encapsulate Paired Operations and Fields

**Flag (operations):** Two function calls that always appear together (e.g., `subtractPendingResources` + `inadmissibleWorkloads.delete`, or `addPendingResources` + `inadmissibleWorkloads.insert`).

**Ask:** Wrap them in a single helper so callers cannot forget one half. The invariant should be enforced by the API, not by convention.

**Flag (fields):** Two struct fields that must always be set and cleared together (e.g., `inflight` + `inflightResources`).

**Ask:** Encapsulate them in a nested struct so they are updated atomically. This eliminates the "what if field A is nil but field B is not" inconsistency.
