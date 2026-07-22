---
name: deleted-backwards-compatibility-code
description: Hard blocker — flag removal or relocation of code that manages cluster state owned by previous controller versions.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Deleted Backwards-Compatibility Code

**Deleted backwards-compatibility code** — this is a **hard blocker**. If the diff
removes or relocates code that manages cluster state owned by previous versions
(finalizer cleanup, annotation/label migration, ownership reconciliation, CRD field
handling), think through a rolling upgrade: objects created by the old version must
still be reconciled to a terminal state by the new version — finalizers removed,
obsolete annotations dropped, status converged. Deleting finalizer-removal logic, for
example, will leave pre-existing objects stuck forever.
