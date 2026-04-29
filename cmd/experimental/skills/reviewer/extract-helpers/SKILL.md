---
name: extract-helpers
description: Review that local variables with unnecessarily wide scope are extracted into helper functions.
---

# Skill: Extract Helpers to Limit Variable Scope

**Flag:** A local variable whose scope spans more of a function than it needs to — it's only used inside a self-contained block of logic.

**Ask:** Extract that block into a helper function. This limits the variable's lifetime to the helper, shortens the enclosing function, and makes the intent clearer.

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
