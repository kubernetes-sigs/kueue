---
name: integration-tests-for-updates
description: Review that update/mutation logic has integration test coverage in test/integration/.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Integration Tests for Update/Mutation Paths

**Flag:** New logic that handles an *update* case (as opposed to create or delete), with no corresponding integration test.

**Ask:** "Do we have an integration test covering this update scenario?" Unit tests alone are not sufficient — update paths are a common source of subtle bugs and should be covered in `test/integration/`.
