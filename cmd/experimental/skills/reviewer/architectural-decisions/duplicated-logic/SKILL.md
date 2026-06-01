---
name: duplicated-logic
description: Flag identical (or near-identical) blocks across types/adapters/call sites that should live in a shared helper — and newly extracted helpers with only one caller.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Duplicated Logic Across Types/Adapters/Call Sites

**Duplicated logic across types/adapters/call sites** — identical (or near-identical)
blocks that should live in a single shared helper. Conversely, flag a *newly extracted*
helper that has only one caller and no clear near-term reuse.
