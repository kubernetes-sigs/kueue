---
name: kep-review
description: Review Kueue enhancement proposals (KEPs) for project-wide design concerns. Use when reviewing a new or updated KEP.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# KEP Review

* Do not create KEPs dedicated to a specific out-of-tree Job integration. An
  out-of-tree Job integration uses a workload API that is not built into
  Kubernetes. Generalize KEPs around Kueue concepts and behavior so they apply
  across Job integrations.
