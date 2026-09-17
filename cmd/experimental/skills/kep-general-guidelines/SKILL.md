---
name: kep-general-guidelines
description: Apply project-wide design guidance when drafting, updating, or reviewing Kueue enhancement proposals (KEPs).
---

# KEP General Guidelines

* Do not create KEPs dedicated to a specific out-of-tree Job integration. An
  out-of-tree Job integration uses a workload API that is not built into
  Kubernetes. Generalize KEPs around Kueue concepts and behavior so they apply
  across Job integrations.
