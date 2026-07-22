---
name: feature-gated-insecure-paths
description: Flag feature-gated behavior whose security assumptions do not hold in both gate-on and gate-off states; require a comment near the gate check for new alpha attack surface.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Feature-Gated Insecure Paths

- Behavior added behind a feature gate whose security assumptions
  do not hold when the gate is off **and** when it is on. A feature gate must not ship
  a path that would be insecure if accidentally enabled. New alpha gates with new
  attack surface should note the assumption in a code comment near the gate check.
