---
name: algorithm-comments
description: Review that algorithm comments match the implementation. Use when a comment describes a formula or algorithm and you want to verify it matches what the code actually does.
---

# Skill: Algorithm Comments Must Match the Implementation

**Flag:** A comment describing a mathematical formula or algorithm that doesn't match what the code actually does (e.g., comment says "ceiling" but code uses rounding via `int(x + 0.5)`).

**Ask:** Correct the comment to match the implementation, or fix the implementation to match the comment. A mismatch is a latent correctness bug — future maintainers will trust the comment over the code.

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
