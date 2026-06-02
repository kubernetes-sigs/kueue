---
name: input-validation
description: Flag missing input validation at trust boundaries — user-settable CR fields, webhook payloads, labels, annotations, or untrusted deserialization (CWE-20, CWE-502).
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Missing Input Validation at Trust Boundaries

- New user-settable fields on a CR, webhook payload, label, or annotation used without
  explicit validation (length, regex, enum, parse).
- Strings from `obj.Spec`, `obj.Annotations`, `obj.Labels`, or webhook request bodies
  consumed directly. Reconciler code must still nil-check optional fields the webhook
  has already validated.
- Untrusted input re-parsed with permissive deserializers
  (`yaml.Unmarshal(..., interface{})`, `gob`, generic JSON into
  `map[string]interface{}`). **Hard violation** (CWE-20, CWE-502).
