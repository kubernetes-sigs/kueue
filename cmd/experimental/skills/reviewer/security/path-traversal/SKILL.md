---
name: path-traversal
description: Flag file paths built from user-supplied names without sanitization, symlink-following in tenant dirs, and unvalidated archive extraction (CWE-22).
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Path Traversal

- File paths built from user-supplied names without `filepath.Clean` plus a prefix check
  against an allowlisted root.
- Symlinks followed in tenant-controlled directories.
- Archive extraction without validating each entry stays within the destination root
  (zip-slip, CWE-22).
