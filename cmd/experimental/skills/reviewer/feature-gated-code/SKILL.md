---
name: feature-gated-code
description: Review that new feature-gated behavior has explicit gate checks on all reachable code paths.
---

# Skill: Feature-Gated Code Must Check the Gate

**Flag:** New behavior that belongs to a feature gate (e.g., `features.Enabled(features.SomeFeature)`) but a reachable code path is missing the check — either the function has no early return when the gate is off, or a call site branches on other conditions without first checking the gate.

**Ask:** Add an explicit `if !features.Enabled(features.SomeFeature) { return }` short-circuit. Prefer placing it inside the function (so it's enforced for all current and future callers) over guarding every call site individually. If the check must live at the call site, document why.

<!--
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->
