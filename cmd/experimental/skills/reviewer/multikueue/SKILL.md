---
name: multikueue
description: Review MultiKueue controllers, dispatching, and job adapters for unnecessary remote-cluster reads and lifecycle regressions.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Review MultiKueue Changes

Apply this skill when a diff changes MultiKueue reconciliation, worker selection,
remote clients, or job adapters. Trace the affected manager-to-worker call paths,
including shared helpers, before judging remote operations.

## Remote reads

Read and apply [Minimize MultiKueue Remote Reads](../../kueue-multikueue-remote-reads/SKILL.md).

Flag unnecessary remote `Get` or `List` calls when the diff and surrounding code
show that local state, an already fetched object, or a narrower query can supply
the required data with sufficient freshness. Check whether the actual client
reads from a cache or the API server. Explain how the avoidable reads multiply
across workers, workloads, or repeated reconciliations.

Do not flag a read solely because it accesses a remote cluster. Identify the
specific redundant work and a safe alternative, accounting for admission,
status synchronization, ownership checks, cleanup, and recovery.

## Lifecycle correctness

For the paths affected by the diff, check that:

- Worker selection and job creation preserve the existing admission and
  dispatch semantics, including concurrent updates and reconciliation retries.
- Disconnection, missing objects, and stale observations remain distinct.
  Failover preserves worker-lost grace periods and does not leave an old worker
  running a duplicate job.
- Deletion and completion still clean up owned remote objects, including on
  workers that reconnect later. Ownership checks protect unrelated objects.

When reads are removed or reordered, inspect the relevant behavior coverage and
request-count evidence. Follow [Writing tests](../../../../../site/content/en/community/contribution_guidelines/writing_tests.md)
when evaluating tests; require cache or watch integration coverage only when the
change depends on behavior that fake clients cannot establish.

## Findings

Report actionable findings introduced by the diff. Cite the file and line,
describe the triggering workload and worker state, explain the consequence, and
suggest a concrete correction. For unnecessary reads, identify the call path and
why the data is already available or irrelevant. Distinguish demonstrated
regressions from questions requiring more evidence; avoid speculative redesigns.
