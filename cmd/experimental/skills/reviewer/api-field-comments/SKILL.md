---
name: api-field-comments
description: Review that new CRD fields have complete godoc comments with enum value descriptions.
---

# Skill: New API Fields Need Complete Comments

**Flag:** A new CRD field in `apis/kueue/` is added with no godoc comment, a one-word comment, or a comment that references modes or values not yet implemented in this PR.

**Ask:**
- Every new field needs a comment explaining its purpose and effect.
- Enum-like string fields must explicitly list all valid values and their semantics: "Possible values are: X (does …), Y (does …)."
- Don't mention unsupported or planned-but-not-yet-implemented values — iterate the comment as values are added in future PRs to avoid misleading users about what the API currently supports.

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
