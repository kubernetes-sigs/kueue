# KEP-15614: Pre-Submission Admission Simulator

<!-- toc -->
- [Summary](#summary)
  - [Example](#example)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Data Scientist submitting an AI job](#story-1-data-scientist-submitting-an-ai-job)
    - [Story 2: AI Agent Negotiation](#story-2-ai-agent-negotiation)
    - [Story 3: CI/CD Pipeline](#story-3-cicd-pipeline)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Test Plan](#test-plan)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

Build a non-destructive simulation capability for Kueue that tells users what is likely to happen *before* they submit a workload. This introduces a `kueuectl simulate` command and a corresponding backend API endpoint that evaluates a workload against the live cluster state (quotas, flavors, topology) without persisting the workload to etcd.

### Example

```bash
$ kueuectl simulate -f training.yaml

Workload: llama-training

Admission: NOT FEASIBLE

Quota:       ✓ Available
GPU:         ✓ 64 requested
Topology:    ✗ Cannot place workload
Preemption:  Not required

Reason:
No eligible topology domain currently provides 64 H100 GPUs.
```

## Motivation

Currently, users often follow this flow for validating workloads:
1. Submit workload
2. Workload stays pending
3. Inspect Workload / ClusterQueue / events
4. Find quota, topology, flavor, or scheduling issue
5. Modify workload
6. Try again

For large GPU/AI/HPC clusters, this trial-and-error cycle becomes expensive and frustrating. Kueue may also see sufficient aggregate quota while the workload is not actually schedulable because of node placement, topology, devices, or other constraints. 

By exposing Kueue's existing internal scheduling simulation logic (e.g., `wasSimulator`, `flavorassigner`) via a dedicated endpoint, we can dramatically improve the developer experience and enable AI agents and CI/CD pipelines to validate scheduling feasibility asynchronously.

### Goals

- Introduce an API endpoint (e.g. `/simulate` subresource) that evaluates a hypothetical workload against the scheduler's snapshot cache.
- Introduce `kueuectl simulate -f job.yaml` CLI command.
- Return structured reasons for admission failure (Quota, Flavor, Topology, etc.).
- Make zero changes to the actual cluster (etcd).

### Non-Goals

- Implementing a full cluster simulation framework (e.g. simulating 10,000 workloads into the future). This is purely a pre-submission check for a single incoming workload.
- Implementing an entirely new scheduler. We will reuse Kueue's existing scheduler/snapshot logic.

## Proposal

### User Stories

#### Story 1: Data Scientist submitting an AI job
A data scientist wants to run a 64-GPU Llama training job. Before clogging the queue, they run `kueuectl simulate -f training.yaml`. The CLI tells them that 64 GPUs cannot be satisfied in any single topology domain, saving them hours of debugging.

#### Story 2: AI Agent Negotiation
An AI Agent wants to run a job. It generates a 128-GPU job and calls the simulate endpoint. The endpoint rejects it due to quota limits. The agent automatically negotiates and downgrades the request to 64 GPUs, simulates again, gets a success, and then actually submits the job.

#### Story 3: CI/CD Pipeline
A platform team uses `kueuectl simulate` in their GitOps pipeline to ensure that default tenant workloads are syntactically and structurally feasible against the staging cluster's quotas before merging the PR.

### Risks and Mitigations

- **State Drift**: The simulation checks the `Snapshot` cache at a single point in time. By the time the user actually submits the job, the cluster state may have changed, and the job could still pend. 
  - *Mitigation*: The documentation must clearly state that a successful simulation is a *feasibility check*, not a *reservation* or a *guarantee* of scheduling.
- **Performance impact**: Running complex simulations (especially with preemption) could consume CPU resources on the Kueue controller if spammed.
  - *Mitigation*: Define an enforceable load-control contract for the simulator endpoint. This will include configurable limits for request rate, concurrent simulations, execution time, and workload complexity, enforced by the Kueue API server before the workload reaches the simulation logic.

## Design Details

The core implementation requires:

1. **API Exposure & Authorization:** We need an endpoint to receive the workload. Since the workload isn't persisted, standard webhooks won't work. We will use an extension API server or a custom HTTP server exposed by the Kueue controller manager. The API will implement Kubernetes RBAC enforcement using a `SubjectAccessReview` pattern (similar to KueueViz) to authenticate the requester and filter/reject simulation results appropriately before returning them.
2. **Hooking into Scheduler Cache:** The endpoint will parse the incoming manifest into a `Workload` struct. (Note: The command will accept Job-like resources like `Job` and `JobSet` which will be converted to `Workload` using standard defaulting and validation rules before simulation). It will then pass it to the existing `flavorassigner` and preemption logic. Because preemption and usage simulation temporarily mutate snapshot state before restoring it, we must ensure **per-request snapshot isolation**. Each request will either obtain its own independent `schdcache.Snapshot` and simulator state, or the entire evaluate-and-revert sequence will be strictly serialized to prevent concurrent requests from observing temporary state.
3. **CLI command:** `kueuectl simulate` will wrap the API call and format the output nicely for the user.

### Test Plan

[ ] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Unit tests

- `pkg/scheduler`: Ensure per-request snapshot isolation. Verify that concurrent simulation requests cannot observe temporary snapshot mutations from one another during evaluate-and-revert sequences.
- `cmd/kueuectl/app/simulate`: Command logic.

#### Integration tests

- Start an envtest cluster with a specific queue configuration. Seed the cluster with representative existing Workloads and other Kueue resources. Record their identities, resource versions, and contents. Send a simulate request via the API for an oversized job -> expect rejection. Send for a valid job -> expect success. Assert that the pre-existing resources are completely unchanged (by comparing resource versions and contents) and assert that the simulated workload was not persisted to the API server.

### Graduation Criteria

- **Alpha:** 
  - `kueuectl simulate` is available via a feature gate.
  - Supports basic quota and flavor assignment simulation.
- **Beta:** 
  - Supports advanced simulation including Topology Aware Scheduling and Preemption.
  - End-to-end tests are written and stable.
- **Stable:**
  - Feature is enabled by default.
  - Documentation is complete.

## Implementation History

- 2026-09-15: Initial KEP draft created.

## Alternatives

- **Using standard `--dry-run=server`:** As discussed, standard dry-run only hits the mutating/validating webhooks. It does not hit the asynchronous scheduler controller, so it cannot check quotas or preemption.
- **Creating a standalone simulator project:** While useful for capacity planning, a standalone simulator requires duplicating the live cluster's state. Building the simulation directly into the active Kueue controller ensures the simulation accurately reflects the real-time cache.
