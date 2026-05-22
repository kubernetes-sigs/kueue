---
name: visibility-exports
description: Review that newly exported functions have callers outside the package, lowercasing those that don't.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Visibility Review — Don't Export What Doesn't Need to Be Exported

Use this when reviewing Go code that adds new exported functions or methods.

## What to check

For every new exported identifier, grep for callers outside the declaring package. If all
callers are within the same package, lowercase it — exported symbols become permanent API
surface that can't be removed without a breaking change.

```bash
grep -rn ".FunctionName()" ./ --include="*.go"
```

Note: test files in the same package (`package foo`) can access unexported symbols, so
there's no need to export just for tests.
