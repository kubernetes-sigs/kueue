---
name: information-disclosure
description: Flag information-disclosure patterns — full-object logging, request echoing, credentials in status/annotations, kubeconfig logging, secret-bearing status (CWE-200, CWE-522, CWE-532, CWE-552).
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Information Disclosure (CWE-200, CWE-522, CWE-532, CWE-552)

- Full Kubernetes objects logged with `%+v` / `%#v`. Required: `klog.KObj(obj)` plus
  structured key/value pairs.
- Webhook responses, events, or status fields that echo request bodies, stack traces,
  or credential-bearing fields.
- Credentials or tokens persisted in annotations, labels, env vars, or status —
  credentials belong in mounted Secrets.
- kubeconfigs logged, echoed in status, or written with
  `insecure-skip-tls-verify: true`.
- Visibility-API or metrics queries that widen results past the caller's
  namespace/queue.
- Secret-bearing data written to `.status` — status updates are at-least-once, so
  every retry leaks.
