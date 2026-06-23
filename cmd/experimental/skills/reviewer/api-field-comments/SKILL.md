---
name: api-field-comments
description: Review that new CRD fields have complete godoc comments with enum value descriptions.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: New API Fields Need Complete Comments

**Flag:** A new CRD field in `apis/kueue/` is added with no godoc comment, a one-word comment, or a comment that references modes or values not yet implemented in this PR.

**Ask:**
- Every new field needs a comment explaining its purpose and effect.
- Enum-like string fields must explicitly list all valid values and their semantics: "Possible values are: X (does …), Y (does …)."
- Don't mention unsupported or planned-but-not-yet-implemented values — iterate the comment as values are added in future PRs to avoid misleading users about what the API currently supports.

