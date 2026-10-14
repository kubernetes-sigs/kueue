# KEP-17149: Deployment Parent Suspension

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: LLM serving on GPU clusters](#story-1-llm-serving-on-gpu-clusters)
    - [Story 2: GitOps health reporting](#story-2-gitops-health-reporting)
  - [Design](#design)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Feature Gate](#feature-gate)
  - [Test Plan](#test-plan)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [Stable](#stable)
- [Implementation History](#implementation-history)
- [Alternatives](#alternatives)
  - [Pause in the Deployment webhook instead of the controller](#pause-in-the-deployment-webhook-instead-of-the-controller)
  - [Increase progressDeadlineSeconds](#increase-progressdeadlineseconds)
<!-- /toc -->

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

#### Story 1: LLM serving on GPU clusters

As a platform engineer running LLM inference Deployments on Kueue-managed GPU
quota, I want the Deployment to report a paused state instead of
`ProgressDeadlineExceeded` while its Pods wait for quota, so that my GitOps
tooling does not flag the service as failed.

Today the Deployment hits `ProgressDeadlineExceeded` after 10 minutes of
quota wait, turning ArgoCD red. With this feature, the Deployment shows
`DeploymentPaused` — a non-error state.

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: llm-inference
  labels:
    kueue.x-k8s.io/queue-name: gpu-queue
spec:
  replicas: 3
  selector:
    matchLabels:
      app: llm-inference
  template:
    metadata:
      labels:
        app: llm-inference
    spec:
      containers:
        - name: server
          image: registry.example.com/llm-server:latest
          resources:
            requests:
              nvidia.com/gpu: "1"
```

#### Story 2: GitOps health reporting

As an ArgoCD user, I want Kueue-paused Deployments to report a suspended
(non-error) state instead of degraded, so that quota waits do not trigger
alerts or sync issues.

ArgoCD reports paused Deployments as `Suspended`. Without this feature,
`ProgressDeadlineExceeded` is reported as `Degraded`.

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  labels:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  replicas: 2
  selector:
    matchLabels:
      app: web
  template:
    metadata:
      labels:
        app: web
    spec:
      containers:
        - name: nginx
          image: registry.k8s.io/nginx-slim:0.27
```

### Design

**Job framework capability:** The job framework gains an optional parent-suspension
behavior, following the same pattern as other optional job behaviors such as custom
stop and finalize. Only the Pod integration implements it; other integrations are
unaffected.

**When the pause happens:** While a Pod's Workload is suspended and not admitted
(waiting for quota), the Pod integration pauses the parent Deployment and records the
tracking annotation `kueue.x-k8s.io/paused-by-kueue`. This step is gated by the
`DeploymentParentSuspension` feature gate and is idempotent — an already-paused
Deployment is left untouched.

**When the unpause happens:** The unpause is attempted on every reconciliation in which
a Pod's Workload is admitted, and when a Pod is deleted. It traverses Pod → ReplicaSet →
Deployment via owner references (with UID validation at each hop), unpauses only if the
tracking annotation is present (user-initiated pauses are never touched), and only when
no other Pods of the Deployment remain scheduling-gated. The unpause paths are **not**
gated by the feature gate.

**Disabling the feature gate:** Because only the pause is gated, an admin disabling
`DeploymentParentSuspension` while Deployments are paused-by-Kueue does not create
orphaned pauses: each paused Deployment is unpaused as soon as any of its Pods is
admitted or deleted, since those paths always run. Users observe: already-paused
Deployments with still-gated Pods stay paused until their Pods are admitted or deleted;
newly gated Pods no longer pause their Deployment, so the pre-feature behavior —
`ProgressDeadlineExceeded` after `progressDeadlineSeconds` — returns.

**Template drift:** Kueue watches Deployments carrying the tracking annotation. If the
Deployment's pod template differs from the latest ReplicaSet's template (ignoring
`pod-template-hash`), the Deployment is unpaused so the Deployment controller can roll
out the change. New Pods are then gated by Kueue and the Deployment is re-paused if
they remain gated. Drift detection only inspects ReplicaSets owned by the paused
Deployment.

**RBAC:** The Kueue controller ClusterRole gains `patch` on Deployments (new) and `get`
on ReplicaSets.

### Risks and Mitigations

**Workload deletion while paused:** Each Pod has its own Workload. When a Workload is
deleted while its Deployment is paused, the Pod integration stops the Pod, which deletes
the gated Pod. The Deployment is unpaused only when no Pods matching its selector remain
gated — sibling Pods still waiting for quota keep the pause in place. Every Pod's
reconciliation attempts the resume when its Workload is admitted or when the Pod is
deleted, so the last Pod to leave the gated state is the one that removes the pause. A
replacement Pod created after an unpause is gated by Kueue again, gets a new Workload,
and re-pauses the Deployment. The pause is therefore never orphaned; the transient
unpause window lasts seconds, negligible against the default 10-minute progress deadline.
If removing the pause during finalization fails, the Pod's finalizer is not removed and
the resume is retried on subsequent reconciliations.

**Template drift while paused:** The Deployment controller cannot create a new ReplicaSet
while paused. Mitigated by the Deployment reconciler, which detects template drift and
unpauses. New pods are then gated by Kueue, and re-paused if they remain gated.

## Design Details

### Feature Gate

The feature is guarded by the `DeploymentParentSuspension` feature gate (default off).
Enable it in the Kueue manager configuration:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
featureGates:
  DeploymentParentSuspension: true
```

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Unit tests

- Pod integration: parent lookup and owner-reference traversal (including UID validation),
  pause, unpause, and the gated-siblings check.
- Deployment integration: template drift detection and template comparison.

#### Integration tests

- Pause on gate, unpause on admission, unpause on template drift, unpause on pod
  deletion, no-op when the gate is off, and workload deletion recovery: the gated Pod
  is deleted, the pause is removed only when no sibling Pods remain gated, and a
  replacement gated Pod re-pauses the Deployment.

#### e2e tests

The core scenario — a gated Deployment reports `DeploymentPaused` instead of
`ProgressDeadlineExceeded` and unpauses after admission — requires the real Deployment
controller and is validated in a follow-up PR before graduation to Beta. Remaining e2e
scenarios are added in Beta.

### Graduation Criteria

#### Alpha

- Feature gate `DeploymentParentSuspension` (default off).
- Pause the Deployment while its Pods are scheduling-gated; unpause after admission;
  never override user-initiated pauses.
- Cleanup of Kueue-managed pauses when the feature gate is disabled, including on Pod
deletion. A failure to resume during finalization blocks removal of the finalizer, so
the resume is retried on subsequent reconciliations.
- Automatic recovery when a Workload is deleted while the Deployment is paused: the pause
  is removed only once no sibling Pods remain gated (see Risks and Mitigations), covered
  by integration tests.
- Template drift detection scoped to ReplicaSets owned by the paused Deployment.
- Unit and integration tests.
- e2e coverage of the core pause/unpause scenario, delivered as a follow-up PR before
graduation to Beta.

#### Beta

- Feature gate default on.
- Remaining e2e scenarios and gomock coverage.

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

