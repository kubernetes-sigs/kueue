---
name: feature-gated-code
description: Review that new feature-gated behavior has explicit gate checks on all reachable code paths.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Feature-Gated Code Must Check the Gate

**Flag:** New behavior that belongs to a feature gate (e.g., `features.Enabled(features.SomeFeature)`) but a reachable code path is missing the check — either the function has no early return when the gate is off, or a call site branches on other conditions without first checking the gate.

**Ask:** Add an explicit `if !features.Enabled(features.SomeFeature) { return }` short-circuit. Prefer placing it inside the function (so it's enforced for all current and future callers) over guarding every call site individually. If the check must live at the call site, document why.
