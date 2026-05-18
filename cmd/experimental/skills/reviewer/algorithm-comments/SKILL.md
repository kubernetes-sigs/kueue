---
name: algorithm-comments
description: Review that algorithm comments match the implementation. Use when a comment describes a formula or algorithm and you want to verify it matches what the code actually does.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Algorithm Comments Must Match the Implementation

**Flag:** A comment describing a mathematical formula or algorithm that doesn't match what the code actually does (e.g., comment says "ceiling" but code uses rounding via `int(x + 0.5)`).

**Ask:** Correct the comment to match the implementation, or fix the implementation to match the comment. A mismatch is a latent correctness bug — future maintainers will trust the comment over the code.
