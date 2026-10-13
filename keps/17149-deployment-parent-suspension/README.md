# KEP-17149: Deployment Parent Suspension

## Summary

When Kueue manages Deployment-owned pods via the pod integration, it uses the
`kueue.x-k8s.io/admission` scheduling gate to hold pods while they wait for quota.
The Kubernetes Deployment controller continues counting down
`spec.progressDeadlineSeconds` (default 10m) and marks the Deployment
`ProgressDeadlineExceeded`. GitOps tools (e.g. ArgoCD) interpret this as a failure.

This KEP adds an alpha feature gate (`DeploymentParentSuspension`, default off) that
pauses the parent Deployment (`spec.paused = true`) while its pods are scheduling-gated.
After admission, the Deployment is automatically unpaused.

| State | `spec.paused` | `Progressing` condition | Pod |
|---|---|---|---|
| Waiting for quota (gate on) | `true` | `Unknown` / `DeploymentPaused` | `SchedulingGated` |
| After admission (gate on) | `false` | `True` / `NewReplicaSetAvailable` | `Running` |
| Waiting for quota (gate off) | `false` | `True` → `False` / `ProgressDeadlineExceeded` | `SchedulingGated` |

## Motivation

Long-running serving workloads (LLM inference via vLLM/SGLang, etc.) on multi-tenant GPU
clusters routinely wait for quota while other workloads release capacity. The Deployment
looks "failed" to GitOps tooling even though it is functioning as intended. The only
Kubernetes-native mechanism that stops the progress deadline timer is `spec.paused`.

### Goals

- Prevent `ProgressDeadlineExceeded` on Kueue-gated Deployments by pausing during quota wait.
- Automatically unpause after admission.
- Never override user-initiated pauses.
- Clean up existing pauses when the feature gate is disabled (no orphaned state).
- Detect template drift: if a user edits the pod template while paused, unpause so the
  Deployment controller can roll out the change.

### Non-Goals

- Full Deployment-level Kueue integration (gang admission, coordinated scaling). See
  [#6334](https://github.com/kubernetes-sigs/kueue/issues/6334).
- StatefulSet parent suspension (the interface supports it; implementation deferred).
- Modifying the upstream Kubernetes Deployment controller to understand scheduling gates.

## Proposal

### User Stories

**LLM serving on GPU clusters:** A platform engineer deploys LLM inference Deployments
with Kueue-managed GPU quota. When the cluster is at capacity, pods are gated. Today the
Deployment hits `ProgressDeadlineExceeded` after 10m, turning ArgoCD red. With this feature,
the Deployment shows `DeploymentPaused` — a non-error state.

**GitOps health:** ArgoCD reports paused Deployments as `Suspended` (non-error). Without
this feature, `ProgressDeadlineExceeded` is reported as `Degraded`.

### Design

**Interface:** A `JobWithParentSuspension` optional interface is added to the job framework
with `SuspendParent(ctx, client)` and `ResumeParent(ctx, client)`. This follows the existing
pattern (`JobWithCustomStop`, `JobWithFinalize`). Only the Pod type implements it.

**Reconciler call sites** in `pkg/controller/jobframework/reconciler.go`:

- **Section 7** (job suspended, workload not admitted): calls `SuspendParent`. Gated behind
  `DeploymentParentSuspension`.
- **Section 8** (workload admitted, job running): calls `ResumeParent`. Not gated — so
  disabling the feature gate cleans up existing pauses.
- **`finalize`** (job being deleted): calls `ResumeParent`. Not gated.

**Pod implementation** in `pkg/controller/jobs/pod/`:

`SuspendParent` checks the pod's `SuspendedByParentAnnotation`, traverses
Pod → ReplicaSet → Deployment via owner references (with UID validation at each step),
and patches `spec.paused = true` with a tracking annotation `kueue.x-k8s.io/paused-by-kueue`.
Idempotent — skips if already paused.

`ResumeParent` does the same traversal, checks for the tracking annotation (never unpauses
user-initiated pauses), verifies no sibling pods are still gated, then removes the
annotation and sets `spec.paused = false`.

**Deployment reconciler** in `pkg/controller/jobs/deployment/deployment_reconciler.go`:
watches Deployments with the `kueue.x-k8s.io/paused-by-kueue` annotation. If the
Deployment's pod template differs from the latest ReplicaSet's template (ignoring
`pod-template-hash`), it unpauses so the Deployment controller can roll out the change.

**RBAC:** Adds `patch` on `apps/deployments` (new) and `get` on `apps/replicasets` to the
Kueue controller ClusterRole.

### Risks and Mitigations

**Orphaned pause on Workload deletion:** If the Workload object is manually deleted while
the Deployment is paused, the Deployment may stay paused until the pod is also deleted
(triggering `finalize`) or the user manually removes the annotation. Acceptable for alpha;
for beta, evaluate adding `ResumeParent` to the workload-deletion path.

**Template drift while paused:** The Deployment controller cannot create a new ReplicaSet
while paused. Mitigated by the Deployment reconciler, which detects template drift and
unpauses. New pods are then gated by Kueue, and re-paused if they remain gated.

## Design Details

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Unit tests

- `pkg/controller/jobs/pod`: `TestFindParentDeployment`, `TestSuspendParent`,
  `TestResumeParent`, `TestHasGatedSiblings`
- `pkg/controller/jobs/deployment`: `TestReconcile`, `TestEqualIgnoreHash`

#### Integration tests

- `test/integration/singlecluster/controller/jobs/pod/deployment_parent_suspension_test.go`:
  pause on gate, unpause on admission, unpause on template drift, unpause on pod deletion,
  no-op when gate is off.

#### e2e tests

Deferred to beta.

### Graduation Criteria

#### Alpha

- Feature gate `DeploymentParentSuspension` (default off).
- `JobWithParentSuspension` interface and Pod implementation.
- Deployment reconciler for template drift.
- Unit and integration tests.

#### Beta

- Propagate `ResumeParent` errors in `finalize`.
- Evaluate `ResumeParent` on the workload-deletion path.
- Scope the Deployment reconciler's ReplicaSet list by owner labels.
- Add e2e tests and gomock coverage.
- Feature gate default on.

#### Stable

- Two releases with gate defaulting on and no regressions.
- StatefulSet implementation evaluated.

## Implementation History

- 2026-10-01: PR [#15354](https://github.com/kubernetes-sigs/kueue/pull/15354) opened.
- 2026-10-13: KEP filed as [#17149](https://github.com/kubernetes-sigs/kueue/issues/17149).

## Alternatives

### Pause in the Deployment webhook instead of the controller

The webhook fires on CREATE before the ReplicaSet and pods exist. A Deployment created
with `spec.paused = true` never creates its ReplicaSet, so the pause must happen after
pods exist — in the controller, not the webhook.

### Increase progressDeadlineSeconds

Setting `spec.progressDeadlineSeconds` to a large value works around the problem but
disables the safety net for genuine deployment failures, making it unusable for production
workloads. It also requires user action on every Deployment.

