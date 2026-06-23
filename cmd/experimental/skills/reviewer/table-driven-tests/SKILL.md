---
name: table-driven-tests
description: Review that tests covering multiple scenarios use table-driven style instead of inline sequential blocks.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Table-Driven Tests

**Flag:** A Go test function that covers multiple scenarios inline as a single sequential block.

**Ask:** Refactor to use table-driven style — `tests := map[string]struct{ ... }{ ... }` with `for name, tc := range tests`. A monolithic test that grows one `if` block per case is a "spaghetti test" and makes it hard to add new scenarios.
