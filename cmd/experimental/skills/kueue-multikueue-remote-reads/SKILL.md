---
name: kueue-multikueue-remote-reads
description: Minimize unnecessary remote-cluster reads when implementing or reviewing MultiKueue controllers, dispatching, and job adapters.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Minimize MultiKueue Remote Reads

Apply this guidance when changing or reviewing MultiKueue code that reads worker
clusters, including reads hidden inside job adapters and shared helpers.

Remote API reads add network latency and worker API-server load. Their cost can
multiply across workloads, worker clusters, and repeated reconciliations. Minimize
unnecessary reads while preserving admission, status synchronization, cleanup,
and recovery behavior.

## Before adding a remote read

- Identify the decision that needs the data and whether the manager's state or
  an object already fetched during this reconciliation can answer it.
- Trace the client implementation. A controller-runtime client is not necessarily
  cached. Check `pkg/controller/admissionchecks/multikueue/remote_client.go` and
  the cached kinds configured in `multikueuecluster.go`; uncached kinds and cache
  synchronization failures can result in direct API reads.
- Check the full call path, including adapters, for repeated `Get` or `List`
  calls for the same object on the same worker.

## Prefer the smallest necessary read

- Evaluate local conditions and other inexpensive guards before remote reads
  when they establish that no remote action is needed. Keep reads required to
  find and clean up existing remote objects.
- Reuse objects fetched in the current reconciliation when their freshness is
  sufficient. Pass them to helpers instead of fetching them again. Do not add
  persistent state merely to avoid a read without defining invalidation and
  recovery behavior.
- Restrict reads to the workers needed for the current decision. An established
  placement may permit reading only the selected worker; dispatch, failover,
  and cleanup may still require inspecting other workers. Prove those paths
  remain correct before reducing the set of workers queried.
- Use a named `Get` when the object key is known. When a `List` is necessary,
  narrow its namespace and selectors, and use existing cache indexes where
  appropriate. Verify that field selectors are supported by the actual reader.
- Prefer existing watches and caches to repeated polling when they provide the
  needed state. Account for initial list/watch traffic, memory, synchronization,
  and reconnect behavior before introducing a new cache or watch.

## Preserve correctness and demonstrate the reduction

Missing or stale cached data and an unreachable worker do not prove that a remote
object is absent. Preserve ownership checks, error handling, worker-lost grace
periods, and eventual cleanup. Keep fresh reads when a decision needs them.

For an optimization, explain which reads disappear, on which reconciliation
paths, and how the cost scales with workers and workloads. Use client interceptors
or request measurements to verify fewer reads on the affected paths, alongside
behavior checks for relevant admission, deletion, and recovery cases. Fake-client
call counts alone do not establish cache freshness or real API traffic; use
integration coverage when changing cache or watch behavior.
