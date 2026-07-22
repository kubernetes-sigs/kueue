---
name: pointless-intermediate-variables
description: Flag redundant local variables that add noise without adding clarity.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Pointless Intermediate Variables

**Pointless intermediate variables** — `x := foo.Bar; use(x)` where `use(foo.Bar)` is
equally clear. Redundant locals add noise without adding clarity.
