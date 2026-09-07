---
name: kueue-verify-changes
description: Verify Kueue code changes before committing, pushing, opening or updating a pull request, or handing work back to a user. Use after modifying Go code, APIs, generated files, Helm charts, shell scripts, website content, or agent skills to select and run the relevant local checks, including the project linter.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Verify Kueue Changes

Validate the final diff, not an earlier version of it. Any code change made after a
successful check invalidates that check when it could affect the result.

## Workflow

1. Inspect `git status --short` and `git diff` to identify the changed file types and
   avoid including unrelated work.
2. Run focused tests for the behavior being changed.
3. Select the applicable repository checks from the table below.
4. Run `make verify` before the final handoff when the environment and available time
   permit. It is the closest local equivalent to the checks enforced by CI.
5. Inspect `git status --short` and `git diff` again. Include intended generated changes
   and investigate unexpected ones.
6. Report exactly which checks passed and which checks were not run. Never imply that a
   change is fully verified when only focused tests were run.

## Required checks by change type

| Changed files | Minimum relevant checks |
|---|---|
| Go (`*.go`) | `make ci-lint` plus focused Go tests |
| API types or generated artifacts | `make verify-tree-prereqs`, `make lint-api`, and focused tests |
| Go formatting only | `make fmt-verify` |
| Shell scripts | `make shell-lint` |
| Helm charts | `make helm-verify` and `make helm-unit-test` |
| Agent skills | `make skills-lint` |
| Website links or content | `make verify-website-links` and the relevant website tests |

Treat these as minimums, not substitutes for tests specific to the changed package or
feature. Consult `make help` and `CONTRIBUTING.md` when a change type is not listed.

## Go linting rule

Always run `make ci-lint` after changing Go code and before committing, pushing, or
updating a PR. Unit and e2e tests do not execute golangci-lint and therefore cannot catch
rules such as `modernize/newexpr`.

Do not predict which lint rules apply from visual inspection alone. If
`make ci-lint` reports an issue, fix it and rerun the command against the final diff.

## When full verification is unavailable

If `make verify` or a relevant minimum check cannot run because of environment,
dependency, or time constraints, run every remaining applicable check. State the exact
command that did not run and the reason in the handoff. A CI run is not a substitute for
silently skipping local validation.
