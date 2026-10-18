# KEP-17297: Plain Pod Fail-Fast Policy

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Feature Gate and Annotation](#feature-gate-and-annotation)
  - [Failure Detection Semantics](#failure-detection-semantics)
  - [Teardown and Quota Safety](#teardown-and-quota-safety)
  - [MultiKueue Convergence](#multikueue-convergence)
- [User Stories](#user-stories)
  - [Story 1: Multi-Container Batch Job with Missing Worker Dependency](#story-1-multi-container-batch-job-with-missing-worker-dependency)
  - [Story 2: Standalone Plain Pod with Disabled WaitForPodsReady](#story-2-standalone-plain-pod-with-disabled-waitforpodsready)
- [Design Details](#design-details)
  - [API Changes](#api-changes)
    - [Feature Gate](#feature-gate)
    - [Pod Annotation](#pod-annotation)
  - [Controller Logic](#controller-logic)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration Tests](#integration-tests)
- [Graduation Criteria](#graduation-criteria)
  - [Alpha (v0.20)](#alpha-v020)
  - [Beta (v0.21)](#beta-v021)
  - [Stable (v0.22+)](#stable-v022)
- [Alternatives](#alternatives)
  - [Alternative 1: Rely solely on WaitForPodsReady](#alternative-1-rely-solely-on-waitforpodsready)
  - [Alternative 2: Immediately mark Workload Finished without deleting the Pod](#alternative-2-immediately-mark-workload-finished-without-deleting-the-pod)
  - [Alternative 3: ClusterQueue or Configuration-level default](#alternative-3-clusterqueue-or-configuration-level-default)
<!-- /toc -->

## Summary

This proposal introduces an opt-in fail-fast policy for standalone plain Pods managed by Kueue, gated by the alpha feature gate `PlainPodFailFast` (disabled by default in v0.20) and configured via the Pod annotation `kueue.x-k8s.io/pod-fail-fast: "true"`.

When enabled on a batch Pod with `spec.restartPolicy: Never`, Kueue detects when any regular container in `spec.containers` terminates with a non-zero exit code while other containers remain in a `Waiting` state (keeping the Pod phase in `Pending`). Rather than leaving the Pod stalled and retaining cluster quota indefinitely, Kueue triggers graceful deletion of the Pod, waits for running containers to shut down, marks the Workload failed, and releases reserved quota.

## Motivation

In batch workloads composed of multiple containers running under `spec.restartPolicy: Never`, container failures can stall the Pod in a non-terminal phase. For example:
- A multi-container batch Pod runs a main container alongside one or more helper containers.
- The main container cannot start because an environment variable references a missing Secret or ConfigMap on the cluster, leaving it in `Waiting` with reason `CreateContainerConfigError`.
- Helper containers start up, time out waiting for input from the main container, and terminate with exit code 1 (`Error`).
- In Kubernetes, `Pod.Status.Phase` remains `Pending` because the main container is still waiting and has not yet started or terminated.

Kueue's plain Pod integration checks whether a workload is complete via `Pod.Finished()`, which relies on `utilpod.IsTerminated()`. That check returns true only when the Pod phase reaches `Succeeded` or `Failed`. Because the Pod phase stays `Pending`, Kueue considers the workload active:
- Both `QuotaReserved` and `Admitted` remain `True`.
- The Workload never receives a `Finished` condition.
- In clusters where `waitForPodsReady` is disabled or configured with long timeouts, the Pod retains its CPU, memory, and accelerator quota indefinitely.
- In MultiKueue environments, where remote Pod and Workload statuses are synchronized back to the management cluster, quota remains blocked on both worker and manager clusters.

Under `restartPolicy: Never`, a container that exited with a non-zero code will never restart. The Pod can never succeed. Waiting for manual operator intervention or long generic timeouts wastefully blocks queue capacity for other queued jobs.

### Goals

- Provide an opt-in mechanism to detect unrecoverable container failures on plain Pods with `restartPolicy: Never` while the Pod phase is still non-terminal.
- Guard the behavior with an alpha feature gate (`PlainPodFailFast`) and a Pod annotation (`kueue.x-k8s.io/pod-fail-fast: "true"`).
- Restrict failure detection to regular containers (`spec.containers`), explicitly excluding native restartable sidecars (`initContainers` with `restartPolicy: Always`).
- Stop remaining Pod execution gracefully through standard Kubernetes deletion, honoring `terminationGracePeriodSeconds` before quota is released.
- Ensure quota remains reserved while containers are terminating, preventing unreserved resource usage on worker nodes.
- Emit clear Workload and Pod events/conditions indicating which container failed and its exit code.
- Ensure MultiKueue worker clusters clean up failed attempts and propagate the finished status back to the manager cluster so quota accounting converges across clusters.

### Non-Goals

- Modifying Kubernetes core Pod lifecycle or kubelet behavior.
- Automatic retries or re-creating failed Pods (retries remain the responsibility of submitting workflow engines such as Argo, Airflow, or Kubeflow).
- Dependency provisioning or synchronizing missing Secrets/ConfigMaps across clusters.
- Supporting Pod groups (`kueue.x-k8s.io/pod-group-name`) or serving workloads in Alpha.
- Supporting containers with restart policy overrides (e.g., `Always` or `OnFailure`) in Alpha.

## Proposal

### Feature Gate and Annotation

1. **Feature Gate**: Introduce `PlainPodFailFast` as an Alpha feature gate, disabled by default.
2. **Pod Annotation**: Introduce the annotation `kueue.x-k8s.io/pod-fail-fast: "true"` on plain Pods. The fail-fast evaluation only runs when both the feature gate is enabled and the Pod carries this annotation.

### Failure Detection Semantics

During Pod reconciliation, Kueue checks whether a Pod qualifies for fail-fast handling:
1. The Pod is a standalone plain Pod (not part of a Pod group: `utilpod.GetPodGroupName(pod) == ""`).
2. `pod.Spec.RestartPolicy == corev1.RestartPolicyNever`.
3. The Pod has not already started deleting (`pod.DeletionTimestamp.IsZero()`).
4. The Pod is admitted and its Workload is not finished.
5. The Pod phase is not terminal (`!utilpod.IsTerminated(pod)`).
6. Any regular container in `spec.containers` matches the following condition in `status.containerStatuses`:
   - `state.terminated != nil`
   - `state.terminated.exitCode != 0`
   - The container does not have an individual `restartPolicy` override (if supported by the cluster runtime).

Native sidecar containers in `spec.initContainers` (defined with `restartPolicy: Always`) are explicitly ignored by this check, as they are expected to restart or serve auxiliary functions throughout the Pod lifecycle. Regular init containers with non-zero exit codes already cause Kubernetes to fail the Pod directly, so they do not require fail-fast intervention.

Containers that terminate with `exitCode == 0` do not trigger fail-fast, allowing helper or setup containers in `spec.containers` to finish successfully while others continue running.

### Teardown and Quota Safety

When an unrecoverable failure is detected:
1. Kueue records a `Normal` event on the Pod:
   - Reason: `FailedFast`
   - Message: `Container "<name>" failed with exit code <exitCode>: unrecoverable failure under restartPolicy Never`
2. Kueue sets a status condition on the Pod:
   - Type: `TerminationTarget`
   - Status: `True`
   - Reason: `UnrecoverableContainerFailure`
   - Message: `Container "<name>" terminated with exit code <exitCode>`
3. Kueue issues a graceful delete request (`client.Delete(ctx, pod)`) against the Pod.
4. **Quota preservation during teardown**: The Pod retains its `kueue.x-k8s.io/managed` finalizer while deleting. Because the Workload is not marked finished, Kueue keeps quota reserved while any remaining running containers shut down, honoring `pod.Spec.TerminationGracePeriodSeconds`.
5. Once all containers terminate, kubelet transitions the Pod phase to `Failed`.
6. On subsequent reconciliation, `utilpod.IsTerminated(pod)` returns true. Kueue's existing `job.Finished()` path marks the Workload `Finished=True, Reason=Failed`, records the failure message, removes the finalizer, and releases quota.

### MultiKueue Convergence

In MultiKueue deployments:
1. The workload and Pod are dispatched to the worker cluster.
2. The worker cluster's Kueue pod controller detects the container failure, triggers graceful deletion, and finalizes the worker Workload with `Finished=True, Reason=Failed`.
3. MultiKueue's `SyncJob` replicates the worker Pod's terminal status to the management cluster Pod.
4. MultiKueue's workload controller reads the worker Workload's `Finished` condition and marks the management cluster Workload `Finished=True` with matching Reason and Message.
5. Quota is released on both manager and worker clusters without requiring changes to MultiKueue core logic.

## User Stories

### Story 1: Multi-Container Batch Job with Missing Worker Dependency

As a platform engineer dispatching multi-container batch Pods via MultiKueue, my application runs a data processing container alongside logging and sync sidecars. When the primary container fails to start due to a missing Secret on a worker cluster, the sync sidecar exits with code 1. I want Kueue to detect this unrecoverable state, stop the stalled Pod gracefully, mark the Workload failed, and release quota so other jobs in the queue can run.

### Story 2: Standalone Plain Pod with Disabled WaitForPodsReady

As a cluster administrator running batch jobs where `DisableWaitForPodsReady=true` is set to avoid timing out large image pulls, I need Kueue to recognize when a container has permanently died with exit code 1 under `restartPolicy: Never`. Rather than holding GPU and CPU reservations until someone manually deletes the Pod, Kueue should fail fast and clear the queue reservation.

## Design Details

### API Changes

#### Feature Gate
In `pkg/features/kube_features.go`:
```go
// PlainPodFailFast enables opt-in fail-fast handling for plain Pods with unrecoverable container failures.
PlainPodFailFast featuregate.Feature = "PlainPodFailFast"
```

#### Pod Annotation
In `pkg/controller/jobs/pod/constants/constants.go`:
```go
PodFailFastAnnotationKey   = "kueue.x-k8s.io/pod-fail-fast"
PodFailFastAnnotationValue = "true"
```

### Controller Logic

The check is added to the plain Pod reconciler in `pkg/controller/jobs/pod/pod_controller.go`:

```go
func (p *Pod) checkFailFast(ctx context.Context, c client.Client) (bool, error) {
    if !features.Enabled(features.PlainPodFailFast) {
        return false, nil
    }
    if p.pod.Annotations[constants.PodFailFastAnnotationKey] != constants.PodFailFastAnnotationValue {
        return false, nil
    }
    if p.pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
        return false, nil
    }
    if !p.pod.DeletionTimestamp.IsZero() {
        return false, nil
    }
    if utilpod.IsTerminated(&p.pod) {
        return false, nil
    }

    // Find any regular container that failed
    for _, cs := range p.pod.Status.ContainerStatuses {
        if cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0 {
            // Trigger graceful termination
            return true, p.stopForFailure(ctx, c, cs.Name, cs.State.Terminated.ExitCode, cs.State.Terminated.Reason)
        }
    }
    return false, nil
}
```

When `stopForFailure` is called:
- Sets Pod condition `TerminationTarget` with `Reason: "UnrecoverableContainerFailure"`.
- Issues `client.Delete(ctx, &p.pod)`.
- Leaves the finalizer intact so the Pod terminates cleanly before `p.Finished()` declares the Workload terminal.

### Test Plan

#### Unit Tests
- `TestPodFailFast`:
  - Plain Pod with `restartPolicy: Never`, one container Waiting (`CreateContainerConfigError`), one container Terminated (`exitCode: 1`) -> triggers stop and deletion.
  - Plain Pod with `restartPolicy: Never`, one container Terminated (`exitCode: 0`) -> does not trigger fail-fast.
  - Plain Pod with `restartPolicy: Always` -> does not trigger fail-fast.
  - Plain Pod without `kueue.x-k8s.io/pod-fail-fast: "true"` annotation -> does not trigger fail-fast.
  - Feature gate disabled -> does not trigger fail-fast.
  - Native sidecar in `initContainers` with `restartPolicy: Always` terminating with exit code 1 -> does not trigger fail-fast.

#### Integration Tests
- Verify full lifecycle in `test/integration/controller/jobs/pod`:
  - Create Pod with annotation and `restartPolicy: Never`.
  - Admit Workload and verify quota reservation.
  - Simulate container non-zero exit while another container is waiting.
  - Verify Pod receives `TerminationTarget` and `deletionTimestamp`.
  - Simulate container shutdown and transition to `PodFailed`.
  - Verify Workload transitions to `Finished=True, Reason=Failed` and quota is released.

## Graduation Criteria

### Alpha (v0.20)
- Feature gate `PlainPodFailFast` introduced (disabled by default).
- Pod annotation `kueue.x-k8s.io/pod-fail-fast` implemented.
- Unit and integration tests added.

### Beta (v0.21)
- Feature gate enabled by default.
- Evaluation of whether the policy should extend to Pod groups or be configurable at the `frameworkConfigs` level.

### Stable (v0.22+)
- Feature gate locked to GA.

## Alternatives

### Alternative 1: Rely solely on WaitForPodsReady
Relying on `waitForPodsReady` requires configuring a generic timeout. If timeouts are set long (e.g. 15–30 minutes) to allow large container images to pull, quota remains blocked for that entire duration even when a container has permanently died within seconds. Furthermore, users running with `DisableWaitForPodsReady=true` have no protection.

### Alternative 2: Immediately mark Workload Finished without deleting the Pod
If Kueue marks the Workload finished immediately upon seeing a failed container without waiting for the Pod to terminate, Kueue would release quota while other containers in the Pod could still be running or could start if the blocking condition clears. This violates quota reservation guarantees. Calling `client.Delete` and waiting for terminal Pod phase ensures that resources are never used unreserved.

### Alternative 3: ClusterQueue or Configuration-level default
A cluster-wide or queue-wide setting could force fail-fast on all plain Pods. However, some batch workflows use multi-container Pods where secondary containers perform post-failure cleanup, log uploading, or result persistence after a primary container exits with an error. Forcing fail-fast globally would break those workflows. An opt-in annotation provides the safest and most flexible boundary for Alpha.
