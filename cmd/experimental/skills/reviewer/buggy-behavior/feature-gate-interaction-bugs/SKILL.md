---
name: feature-gate-interaction-bugs
description: Flag feature-gated behavior whose gate-off path is incorrect — early returns that skip cleanup, or state set only when on but read unconditionally.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Feature-Gate Interaction Bugs

**Feature-gate interaction bugs** — when the diff adds behavior guarded by a feature
flag, verify correctness in **both** states. An untested gate-off path is a latent
bug. Look especially for: early returns that skip required cleanup when the gate is
off, or new state set only when the gate is on but read unconditionally.
