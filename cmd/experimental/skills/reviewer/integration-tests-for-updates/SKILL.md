---
name: integration-tests-for-updates
description: Review that update/mutation logic has integration test coverage in test/integration/.
---

# Skill: Integration Tests for Update/Mutation Paths

**Flag:** New logic that handles an *update* case (as opposed to create or delete), with no corresponding integration test.

**Ask:** "Do we have an integration test covering this update scenario?" Unit tests alone are not sufficient — update paths are a common source of subtle bugs and should be covered in `test/integration/`.

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
