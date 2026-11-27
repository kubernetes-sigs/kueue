---
name: kueue-test-writing
description: Write or extend Kueue unit, integration, or e2e tests so they match the neighboring tests and the contribution guidelines. Use when adding or changing a test, or when a bug fix needs a regression test.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Write Kueue Tests

The rules live in
[Writing tests](../../../../site/content/en/community/contribution_guidelines/writing_tests.md)
and the "Test Code Guidelines" section of
[Coding Guidelines](../../../../site/content/en/community/contribution_guidelines/coding_guidelines.md).
Apply both to every new or changed test. Where they disagree with this skill,
follow the guideline pages. Steps 2 and 3 are this skill's own. Steps 1 and 4
point to the pages and skills that already cover them.

## Steps

1. **Pick the level and suite** with
   [Writing tests](../../../../site/content/en/community/contribution_guidelines/writing_tests.md).
2. **Mirror the closest neighbor.** Open the existing test in the target
   directory that checks the same kind of behavior, such as a webhook rejection,
   a reconcile, or an admission flow. Copy its `Describe`/`When` nesting,
   `BeforeEach`/`AfterEach`, suite helpers, wrapper calls, Ginkgo labels, and the
   casing of its `It` text. For a unit test, the neighbor is the existing table
   in the same package.
3. **Check the diff for fixture builders.** List the functions and closures the
   diff adds to test files:

   ```shell
   git diff <base> -- '*_test.go' | grep -nE '^\+(.*:= func\(|func [a-z])'
   ```

   Replace each one that builds a test object with wrapper calls, as the
   guidelines describe.
4. **Check and report.** Run [kueue-go-lint](../kueue-go-lint/SKILL.md) and
   [kueue-verify](../kueue-verify/SKILL.md). Run and report the tests as
   [Test intent in Useful notes](../kueue-pr-issue-workflow/SKILL.md#test-intent-in-useful-notes)
   describes. For a bug fix, show red/green with
   [kueue-pr-evidence-red-green](../kueue-pr-evidence-red-green/SKILL.md).
