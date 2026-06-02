---
name: supply-chain-hygiene
description: Flag supply-chain hygiene gaps — unpinned image refs, TLS bypass, unjustified go.mod replace directives, curl|sh, moving-tag GitHub Actions (CWE-295, CWE-494).
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Supply Chain Hygiene (CWE-295, CWE-494)

- Image references without a digest pin (`image: foo:latest` is a violation; require
  `@sha256:...`).
- `InsecureSkipVerify: true`, TLS verification bypass, or `--insecure` flags.
- `replace` directives in `go.mod` without a tracked issue and a target removal
  version.
- `curl | sh`, `wget | bash`, or scripts fetched from a URL inside a Dockerfile or
  Makefile.
- Third-party GitHub Actions / CI workflow steps pinned to a moving tag instead of a
  commit SHA.
- New direct dependencies without a one-line justification.
