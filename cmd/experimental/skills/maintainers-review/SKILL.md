---
name: maintainers-review
description: Write and self-review Kueue changes against the review bar of Kueue maintainers, distilled from their real review comments. Use before pushing or opening a PR, when addressing review comments, or when asked to review a PR "like a maintainer".
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Maintainers Review

The checklist below holds the requests that Kueue maintainers repeat most often in PR
reviews. Apply it to your diff before you push, so the reviewer can spend their time on
design. Each rule, with its reason and linked precedents, is in [rules.md](rules.md).

## How to use

- **Writing code or preparing a PR.** Walk the checklist for your diff. Fix `[simple]`
  items directly. For the other rules, change the code or explain your choice in the PR
  description.
- **Reviewing a PR.** Report a violation with its rule ID (for example `MR-H1`) and the file
  and line, only where the diff breaks the rule. Cite the rule and its precedent from
  [rules.md](rules.md) in place of claiming that a named maintainer would object.
- **Before pushing**, run `make lint-fix`, `make ci-lint`, `make lint-api` and `make verify`,
  and regenerate generated files (`make generate manifests update-helm`) from their
  sources. The checklist leaves out what these already check.

## Where the rules come from

They are distilled from the review history of eight current and emeritus Kueue
approvers (2022 to 2026). Each profile holds more rules and precedents.

| Reviewer | Standing | Profile |
|---|---|---|
| mimowo | approver | [reviewers/mimowo.md](reviewers/mimowo.md) |
| tenzen-y | approver | [reviewers/tenzen-y.md](reviewers/tenzen-y.md) |
| sohankunkerkar | approver | [reviewers/sohankunkerkar.md](reviewers/sohankunkerkar.md) |
| mbobrovskyi | approver for releases and kueuectl, reviewer | [reviewers/mbobrovskyi.md](reviewers/mbobrovskyi.md) |
| alculquicondor | emeritus approver | [reviewers/alculquicondor.md](reviewers/alculquicondor.md) |
| ahg-g | emeritus approver | [reviewers/ahg-g.md](reviewers/ahg-g.md) |
| gabesaba | emeritus approver | [reviewers/gabesaba.md](reviewers/gabesaba.md) |
| kerthcet | emeritus approver | [reviewers/kerthcet.md](reviewers/kerthcet.md) |

## Checklist

### A. Diff and scope

- [ ] `MR-A1` [simple] Change only the lines the fix needs; move code unchanged.
- [ ] `MR-A2` Send side fixes and refactors as separate PRs; land prep PRs first.
- [ ] `MR-A3` [simple] Add code with its first user; export only what other packages use.
- [ ] `MR-A4` Take API changes and new mechanisms through a KEP first.

### B. Simplicity and reuse

- [ ] `MR-B1` [simple] Search for an existing helper, constant or pattern, and reuse it.
- [ ] `MR-B2` [simple] Delete guards, variables and parameters that change nothing; derive state.
- [ ] `MR-B3` [simple] Use early returns and named functions, not anonymous functions, for logic.
- [ ] `MR-B4` [simple] Apply each change to sibling integrations and every similar place.
- [ ] `MR-B5` [simple] Use named constants, `workload.Reference`, `sets.Set` and typed keys.
- [ ] `MR-B6` Put feature code in its own file or type; keep generic code generic.
- [ ] `MR-B7` Write the simpler code first; back performance claims with numbers.

### C. Naming and Go style

- [ ] `MR-C1` [simple] Give names that match the behaviour; rename when it changes.
- [ ] `MR-C2` [simple] Follow local naming: getters without `Get`, setters without `Set`.
- [ ] `MR-C3` Use existing Kueue terms and PascalCase kinds; spell names out for users.
- [ ] `MR-C4` [simple] Use `len()`, `ptr.Deref`, `cmp.Or` and the established import aliases.

### D. Comments and docs

- [ ] `MR-D1` [simple] Write comments that explain why; delete ones that restate code.
- [ ] `MR-D2` [simple] Update comments, `By` texts and docs that the change made stale.
- [ ] `MR-D3` [simple] Mark temporary code with a tracking issue and its removal condition.
- [ ] `MR-D4` Document new fields, enums and exported symbols with user-facing semantics.
- [ ] `MR-D5` [simple] Update the site docs in the same PR; extend existing pages.
- [ ] `MR-D6` [simple] Mirror mechanical fixes into zh-CN; leave new prose to its owners.

### E. Logging, errors, events

- [ ] `MR-E1` [simple] Pick log verbosity from the Logging Policy table.
- [ ] `MR-E2` [simple] Use the propagated logger and `ctx`, with structured key/values.
- [ ] `MR-E3` Return or log every error once; drop error returns that cannot fail.
- [ ] `MR-E4` Panic only on internal contract breaks; validate user input.
- [ ] `MR-E5` [simple] Emit events, conditions and state metrics only on change; keep messages short.

### F. Controllers and API calls

- [ ] `MR-F1` Patch only on change, once per object, and select with indexes.
- [ ] `MR-F2` Clean up in-memory state on every delete path; deep-copy shared objects.
- [ ] `MR-F3` Handle `Create` after restarts and know the leader-election behaviour.
- [ ] `MR-F4` Keep event handlers to filtering; retry by returning errors from `Reconcile`.
- [ ] `MR-F5` Keep webhooks free of API calls, and check UPDATE as well as CREATE.

### G. Feature gates, API, upgrades

- [ ] `MR-G1` Gate new and risky behaviour; check the gate first; keep gate-off unchanged.
- [ ] `MR-G2` [simple] Alpha off, Beta on; one versioned gate line named by scope.
- [ ] `MR-G3` Add the smallest API a user story needs; reuse existing fields first.
- [ ] `MR-G4` Follow Kubernetes API conventions: `Ref`, conditions, status, enum modes.
- [ ] `MR-G5` Validate new fields strictly with markers or CEL, and test them.
- [ ] `MR-G6` Keep old objects, configs and defaults working across upgrades.

### H. Tests

- [ ] `MR-H1` Show that each new test and branch fails without the change (gate-off: with the gate check forced on).
- [ ] `MR-H2` Test at the cheapest level, through the entry point, in a realistic setup.
- [ ] `MR-H3` [simple] Add rows to the existing table test; keep existing cases unchanged.
- [ ] `MR-H4` [simple] Use `map[string]struct`, `want*` literals, `featureGates` and `cmp.Diff`.
- [ ] `MR-H5` [simple] Compare errors with `cmpopts.EquateErrors()` against sentinel or typed errors.
- [ ] `MR-H6` [simple] Build minimal objects inline with the wrappers, using telling values.
- [ ] `MR-H7` [simple] Write one `ginkgo.It` per scenario; `DescribeTable` only for validation tests.
- [ ] `MR-H8` [simple] Use `ginkgo.By` steps, `behavioral` timeouts and `DeferCleanup`.
- [ ] `MR-H9` [simple] Assert with `g.Expect` and object matchers in place of booleans.
- [ ] `MR-H10` Test both gate states; set gates per case; use a fake clock.
- [ ] `MR-H11` Loop-run new integration and e2e tests; fix flakes at the root cause.

### I. PR, release note, review loop

- [ ] `MR-I1` [simple] Write `<Area>: <effect>` or `NONE`, and set `/kind` by user impact.
- [ ] `MR-I2` [simple] State the problem, the chosen approach and how you verified it.
- [ ] `MR-I3` [simple] Match the title to the diff; use `Fixes #N` only for full fixes.
- [ ] `MR-I4` [simple] Keep issue refs and AI trailers out of commits; add commits during review.
- [ ] `MR-I5` Plan cherry-picks for user-observable bug fixes.
- [ ] `MR-I6` [simple] Link a flake issue when you `/retest`.
- [ ] `MR-I7` [simple] Reply to every thread, resolve after pushing, and rebase on conflicts.
- [ ] `MR-I8` Disclose AI use and write your replies yourself.

### J. KEPs

- [ ] `MR-J1` Describe the design, with the API as Go types in Design Details.
- [ ] `MR-J2` Keep Alpha minimal; write yes-or-no graduation criteria.
- [ ] `MR-J3` List feature interactions, limitations and what the user sees.
- [ ] `MR-J4` [simple] Keep `kep.yaml` milestones, gates and reviewers current.
