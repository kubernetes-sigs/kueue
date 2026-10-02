---
name: tests-run-in-ci
description: Review that tests added or changed by a PR are selected and run by at least one presubmit CI job.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Tests Must Run in CI

**Flag:** A PR adds or changes tests without ensuring that a presubmit CI job runs them. Check package discovery, test targets, Ginkgo labels, and shard filters.

**Ask:** Ensure the tests run in presubmit CI. Add them to an existing CI target or update the relevant filter when needed.
