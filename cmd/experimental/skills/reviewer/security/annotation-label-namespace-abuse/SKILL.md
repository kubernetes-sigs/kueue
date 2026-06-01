---
name: annotation-label-namespace-abuse
description: Flag generic loops over obj.Annotations that copy, forward, or echo values — allow tenant injection into reserved kueue.x-k8s.io/ namespace.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Annotation/Label Namespace Abuse

- Generic loops over `obj.Annotations` that copy, forward, or echo values — these
  allow tenant injection into the reserved `kueue.x-k8s.io/` namespace and leak
  operator-set values across boundaries. Forwarding between CRs must go through an
  explicit allowlist.
