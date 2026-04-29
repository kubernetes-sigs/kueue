---
name: table-driven-tests
description: Review that tests covering multiple scenarios use table-driven style instead of inline sequential blocks.
---

# Skill: Table-Driven Tests

**Flag:** A Go test function that covers multiple scenarios inline as a single sequential block.

**Ask:** Refactor to use table-driven style — `tests := map[string]struct{ ... }{ ... }` with `for name, tc := range tests`. A monolithic test that grows one `if` block per case is a "spaghetti test" and makes it hard to add new scenarios.

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
