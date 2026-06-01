---
name: missing-deferred-removal-markers
description: Flag temporary or deprecated compatibility-shim code kept in the diff without a deferred-removal comment naming the removal version and a tracking issue.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Missing Required Deferred-Removal Markers on Compatibility Shims

**Missing required deferred-removal markers on compatibility shims** — if the diff
keeps temporary or deprecated code for backwards-compatibility reasons, a
deferred-removal comment is **required**, not optional. It must name the exact
version when the shim can be removed and reference a tracking issue. Match the
existing convention used elsewhere in the Kueue codebase for marking removable
shims.
