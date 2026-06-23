---
name: injection
description: Flag injection vectors from user-supplied strings — exec, SSRF, fmt.Sprintf-built selectors/URLs, template rendering, query concatenation (CWE-78, CWE-94, CWE-918).
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Injection from User-Supplied Strings

- `os/exec` / `exec.Command` with user-controlled arguments (CWE-78, CWE-94).
- `http.Get` / `http.NewRequest` to URLs built from CR fields (CWE-918 SSRF).
  Container image refs, init-container pull specs, and webhook target URLs count as
  untrusted.
- `fmt.Sprintf` building YAML, JSON, label selectors, field paths, or API server URLs
  from user input. Require typed marshalling and `labels.SelectorFromSet`.
- Server-side template rendering of annotation/label values (CWE-94).
- SQL-like or query-language string concatenation against any external store.
