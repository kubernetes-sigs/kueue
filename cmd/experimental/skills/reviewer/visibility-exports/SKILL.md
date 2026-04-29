---
name: visibility-exports
description: Review that newly exported functions have callers outside the package, lowercasing those that don't.
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
