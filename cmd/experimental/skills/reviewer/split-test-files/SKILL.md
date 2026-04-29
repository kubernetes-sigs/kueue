---
name: split-test-files
description: Review that new feature tests are split into their own file rather than added to an already large test file.
---

# Skill: Split Large Test Files for New Features

**Flag:** A PR adds substantial test coverage for a new feature to an already large test file (e.g., `scheduler_test.go`, `workload_controller_test.go`).

**When to split** — all three conditions should hold:
1. The feature has a distinct name that maps cleanly to a file suffix (e.g., `_tas`, `_afs`, `_concurrentadmission`).
2. The new tests require feature-specific setup — enabling a feature gate, constructing feature-specific resources — that is unrelated to the base test suite's setup.
3. The new tests would add a significant block of lines (roughly 150+) to a file that is already large.

**Ask:** Extract the feature's tests into `<package>_<feature>_test.go`. Precedent: `scheduler_tas_test.go`, `scheduler_afs_test.go`. This keeps the base file navigable and makes feature-specific scenarios easy to find and extend.

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
