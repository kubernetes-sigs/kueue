---
name: integration-adapter-trust-boundary
description: Flag integration adapters reading credential-like fields from third-party CRDs without treating them as untrusted (RayCluster, SparkApplication, JobSet).
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Integration-Adapter Trust Boundary

- Adapters in `pkg/controller/jobs/<framework>/` that newly read credential-like
  fields from a third-party CRD (`RayCluster` head-node secret refs, `SparkApplication`
  driver env, JobSet pod template env) without treating those fields as untrusted —
  validate before forwarding, never log, never copy into Kueue-owned status.
