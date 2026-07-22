---
name: webhook-safety-regressions
description: Flag webhook safety regressions — loosened failurePolicy, shortened timeoutSeconds, TLS bypass paths, non-idempotent mutating webhooks.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Webhook Safety Regressions

- Validating/mutating webhook changes that loosen `failurePolicy` to `Ignore`,
  shorten `timeoutSeconds` below the existing default without justification, or add a
  path that bypasses TLS verification.
- Non-idempotent mutating webhooks — both a correctness and a security risk under
  admission retry storms.
