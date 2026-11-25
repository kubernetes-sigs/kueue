---
name: kueue-ci-performance
description: Optimize slow Kueue CI jobs through diagnosis, developer-approved proposals, draft PRs, targeted Prow test commands, and measured iterations that preserve validation quality. Use for CI duration or timeout optimization across tests, builds, lint, generation, and setup.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Optimize Kueue CI duration

Reduce CI execution cost while preserving validation quality: diagnose, propose options, wait for the developer's decision, open a draft PR, trigger selected jobs, monitor, compare, and iterate until acceptance.

This workflow applies to unit, integration, E2E, build, lint, generation, dependency installation, and environment setup jobs. Select metrics and quality checks for the actual job; test-specific artifacts are not required for jobs that do not run tests. For failure diagnosis without an optimization task, use [kueue-flake-debugger](../kueue-flake-debugger/SKILL.md).

## 1. Establish the baseline and diagnose

Read applicable instructions, the issue/comments, job entrypoints, Make targets, and relevant source. Search open, closed, and merged PRs for the objective; inspect matching diffs and outcomes. Report an unavailable search as a limitation.

Use the developer's baseline when supplied. Otherwise, select a relevant accessible build and explain the choice. Record:

- Issue, PR, exact job name, build ID, result, and artifact links.
- PR head SHA and Prow's actual base SHA, plus available merge information.
- Commands, toolchain and image versions, CPU/memory limits, concurrency, shards, timeouts, filters, and applicable instrumentation or repetition flags.
- Queueing, setup, download, compilation, command, and target-stage durations where available. Label missing measurements; do not substitute total duration for a stage.
- Validation inputs and outputs: test cases and coverage, build targets and artifacts, lint rules and file selection, or generated-file consistency, as applicable.

Discover artifact paths from the Prow page or metadata. Keep task artifacts and notes under this repository's `.cache/<task-id>/`.

Locate the expensive phase: download/install, cache misses, compile/link, environment provisioning, setup/teardown, repeated computation, analysis, polling, resource contention, or shard imbalance. Separate facts from hypotheses and cite evidence. When proposing additional CI instrumentation, explain its cost and measurement effects.

A timeout is a censored baseline, not a completed duration: do not compute an exact speedup against it. Resource-pressure claims need metrics; a timeout alone does not establish OOM or CPU throttling.

## 2. Propose options and wait for a decision

Present feasible options before implementation or publication. Rank them by evidence, expected benefit, and risk, and recommend a starting point.

| Decision | Include |
| --- | --- |
| Cause | Build and source evidence, unresolved hypotheses |
| Approach | Components/files to change, mechanism, measured versus predicted benefit |
| Compatibility | Behavior and interfaces to preserve, validation invariants, regression checks |
| Execution | Local-execution permissions, draft PR, exact measurement and regression jobs |
| Acceptance | Duration or cost objective, quality requirements, validation matrix, budget if relevant, stopping conditions |

**Ask the developer to select, accept, or revise the proposal, and wait for an explicit reply before dependent edits, pushes, PR publication, or CI triggers.** Continue independent read-only investigation while waiting. Silence is not approval.

Reuse existing decisions and permissions. After approval, iterate autonomously within the plan. Request another decision for material scope, behavior, job, or budget changes. Analysis alone does not authorize publication or CI comments.

## 3. Implement on an independent branch and open a draft PR

Preserve existing changes. Use the requested branch/base or project conventions, and keep unrelated PR changes out of the branch.

Test one main hypothesis per round. Trace callers of changed shared helpers or scripts, preserving lifecycle, cleanup, and failure propagation.

Before pushing or creating/updating the PR, read [AGENTS.md](../../../../AGENTS.md) and the [PR template](../../../../.github/PULL_REQUEST_TEMPLATE.md), and repeat the overlapping-PR search. Use English commit/PR text, appropriate labels, and AI disclosure. Run `make verify` unless the developer requires CI-only execution as described below.

For developer-required CI-only execution, skip local tests, builds, lint, and test-component installation. Document the restriction, inspection performed, intended CI checks, and pending evidence. Otherwise run required local checks. Never report planned checks as passed.

Create the authorized PR in `kubernetes-sigs/kueue` with `gh pr create --draft`, explicit base/fork head, title, and `--body-file`, or update the authorized existing draft. Keep `isDraft: true`; do not mark ready or merge automatically.

## 4. Trigger selected jobs with `/test <job>`

Verify job names, branches, triggers, entrypoints, filters, and resources against current checks and the official [Kueue Prow configuration](https://github.com/kubernetes/test-infra/tree/master/config/jobs/kubernetes-sigs/kueue). Do not invent names or copy another release's jobs. Changing `kubernetes/test-infra` requires separate scope and authorization.

Confirm PR head and draft state. Post one exact `/test <job>` command per line. In this comment template, replace the placeholder with a verified name and omit the outer code fence when posting:

```text
AI assistance was used to prepare this CI optimization run.

/test <verified-job-name>
```

Publish the cached body with `gh pr comment --body-file`. Trigger this round's measurement and regression jobs; avoid `/test all` or broad `/retest`.

Draft PRs may need manual triggers. Confirm creation of builds for this head SHA and record their IDs. Reuse suitable existing builds for the same commit/job. An accepted comment does not prove execution or success.

If triggering is refused or trust authorization is required, report the bot response and request action from an authorized maintainer. Do not self-issue `/ok-to-test` to bypass trust requirements or switch the PR to ready to obtain runs.

## 5. Monitor, compare, and iterate

Monitor `headRefOid`, `statusCheckRollup` or commit statuses, and Prow logs/metadata (`prowjob.json`, `started.json`, `finished.json`). Verify the expected head, actual base SHA, and build ID to exclude stale statuses and other revisions or attempts.

Poll at spaced intervals and share progress and next steps. Wait for queued/running builds instead of reposting triggers. Approval contexts such as `tide` are not test results.

Maintain a compact record for each round, using pending or unavailable for missing values:

| Round / hypothesis | Head / base SHA | Job / build ID / link | Target-stage duration | Command duration | Quality evidence | Keep / adjust / revert |
| --- | --- | --- | --- | --- | --- | --- |

Apply these comparison rules:

- Use consistent units and boundaries. Separate queueing, stage execution, and command time including setup/compilation. Sampled CPU time is not wall-time savings.
- Keep inputs and unrelated settings comparable. Disclose base-SHA, image, toolchain, resources, cache state, or workload changes; obtain comparable runs when needed. Do not describe moving-main measurements as a fixed-base benchmark.
- For intentional parallelism, shard, resource, or cache changes, record the changed variable. Measure the full workload and relevant resource cost, including cold/warm behavior, rather than only a faster shard or cache hit.
- When gains are close to run variation, repeat the same revision and report individual results plus median and range. Repetitions inside one job are not independent CI builds. Follow the agreed budget rather than retrying indefinitely.
- Preserve failures, timeouts, cancellations, missing artifacts, and failed internal attempts. Do not cherry-pick successful samples. A passing stage in a failed job is only partial evidence.
- Check whether the expensive work actually decreased, then keep, adjust, or revert the round. Diagnose failures before rerunning; retries must not hide regressions.

Use completed results to choose the next opportunity, commit, and trigger jobs on the same draft PR. Separate diagnostic runs from final acceptance; remove temporary instrumentation and measure the final revision.

## 6. Preserve validation quality

Faster execution and green checks alone do not prove equivalent validation. Select the invariants relevant to the changed job:

| Job or change | Required comparison |
| --- | --- |
| Tests | Scenario and feature/configuration matrices, repetitions, race/instrumentation flags, assertions, failure/timeout detection, and isolation/cleanup |
| Test results | JUnit names with execution counts as a multiset, failures and skips; explain added/renamed cases and their scenario mapping rather than comparing totals alone |
| Coverage | Covered statements/blocks in the target production packages and agreed thresholds; distinguish package coverage from an aggregate instrumented-package percentage |
| Builds | Required targets/platforms, inputs and dependency versions, output contracts, error propagation, and reproducibility requirements |
| Lint / verification | Rule sets, diagnostics, file selection, generated-file consistency, exit status, and execution of every required check |
| Setup / caches / parallelism | Fresh-state behavior, cache invalidation, isolation, concurrency safety, teardown, and preservation of all work across shards |

Do not claim success by deleting or skipping validation, weakening assertions, disabling checks, reducing required repetitions, or merely increasing a timeout. Review changed assertion logic and ordering rules; unchanged test names or coverage cannot prove semantic equivalence on their own. Add focused regression checks when correcting behavior, including relevant failure and cleanup paths.

Run the applicable final jobs and verification, plus integration/E2E checks when shared helpers or product behavior warrant them. Bind final evidence to the final head SHA. If reusing an earlier integration result, explain the intervening diff and verify that the relevant implementation is identical; rerun when it is not. List unrun real-cluster, load, capacity, or chaos validation explicitly.

## 7. Stop and deliver

Finish when the agreed performance and quality criteria pass on the final changes. Without a numeric target, assess whether confirmed hotspots have been addressed and whether another supported, worthwhile optimization remains within scope. If none remains, explain the remaining bottlenecks and benefit boundary; do not claim a theoretical optimum.

If progress instead requires exceeding the budget, expanding scope, or obtaining unavailable CI permissions/evidence, preserve the work and request the developer's decision. Report the objective as incomplete. If asked to pause, stop new pushes/triggers and report in-flight jobs.

Update the draft PR with the cause, final implementation, per-round measurements, quality comparisons, build links, limitations, and rollback. Provide the branch, final revision, suggested commit message and labels, and last validation date. Verify the published body, head, draft state, and worktree before finishing.

Keep durable evidence in the authorized PR. Delete this task's logs, profiles, backups, and notes without touching other tasks' files. Ask before retaining local artifacts. Report cleanup and unrun checks.
