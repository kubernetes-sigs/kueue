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
  - [API](#api)
  - [Validation](#validation)
  - [Handling](#handling)
  - [Controller Logic](#controller-logic)
  - [Interactions with WaitForPodsReady and Other Features](#interactions-with-waitforpodsready-and-other-features)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration Tests](#integration-tests)
    - [End-to-End (e2e) Tests](#end-to-end-e2e-tests)
- [Graduation Criteria](#graduation-criteria)
  - [Alpha (v0.20)](#alpha-v020)
  - [Beta (v0.21)](#beta-v021)
  - [Stable (v0.22+)](#stable-v022)
- [Future Work Ideas](#future-work-ideas)
- [Alternatives](#alternatives)
  - [Alternative 1: Rely solely on WaitForPodsReady](#alternative-1-rely-solely-on-waitforpodsready)
  - [Alternative 2: Immediately mark Workload Finished without deleting the Pod](#alternative-2-immediately-mark-workload-finished-without-deleting-the-pod)
  - [Alternative 3: ClusterQueue or Configuration-level default](#alternative-3-clusterqueue-or-configuration-level-default)
<!-- /toc -->

## Summary

This proposal introduces an opt-in fail-fast policy for standalone plain Pods managed by Kueue, gated by the alpha feature gate `PlainPodFailFast` (disabled by default in v0.20) and configured via the Pod annotation `kueue.x-k8s.io/pod-fail-fast: "true"`.

When enabled on a batch Pod with `spec.restartPolicy: Never`, Kueue detects when any regular container in `spec.containers` terminates with a non-zero exit code. Rather than leaving the Pod stalled and retaining cluster quota indefinitely (such as when other containers remain in a `Waiting` state), Kueue triggers graceful deletion of the Pod, ensures quota remains reserved during `terminationGracePeriodSeconds`, and marks the Workload failed once the Pod reaches a terminal state.

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
- Emit clear Workload and Pod events and conditions indicating which container failed and its exit code.
- Ensure MultiKueue worker clusters clean up failed attempts and propagate the finished status back to the manager cluster so quota accounting converges across clusters.

### Non-Goals

- Modifying Kubernetes core Pod lifecycle or kubelet behavior.
- Automatic retries or re-creating failed Pods (retries remain the responsibility of submitting workflow engines such as Argo, Airflow, or Kubeflow).
- Dependency provisioning or synchronizing missing Secrets/ConfigMaps across clusters.
- Supporting Pod groups (`kueue.x-k8s.io/pod-group-name`) or serving workloads in Alpha (see [Future work ideas](#future-work-ideas)).
- Supporting containers with individual restart policies or restart rules in Alpha.

## Proposal

### Feature Gate and Annotation

1. **Feature Gate**: Introduce `PlainPodFailFast` as an Alpha feature gate, disabled by default. When the feature gate is disabled, Kueue ignores the annotation and preserves existing plain-Pod behavior.
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
   - The matching `spec.containers` entry has no individual `restartPolicy` or `restartPolicyRules`; containers with either setting are outside the Alpha policy.

Native sidecar containers in `spec.initContainers` (defined with `restartPolicy: Always`) are reported in `initContainerStatuses` and are excluded by this check, as they are expected to restart or serve auxiliary functions throughout the Pod lifecycle. Regular init containers with non-zero exit codes already cause Kubernetes to fail the Pod directly, so they do not require fail-fast intervention.

Containers that terminate with `exitCode == 0` do not trigger fail-fast, allowing helper or setup containers in `spec.containers` to finish successfully while others continue running.

### Teardown and Quota Safety

When an unrecoverable failure is detected:
1. Kueue records a `Normal` event on the Pod:
   - Reason: `FailedFast`
   - Message: `Container "<name>" failed with exit code <exitCode>: unrecoverable failure under restartPolicy Never`
2. Kueue sets a status condition on the Pod using the existing `TerminationTarget` condition mechanism:
   - Type: `TerminationTarget`
   - Status: `True`
   - Reason: `UnrecoverableContainerFailure` (represented as a stop reason)
   - Message: `Container "<name>" terminated with exit code <exitCode>`
3. The condition status update is applied first. If the status-apply call fails, the reconciler returns an error to requeue; deletion is not attempted until status is safely applied.
4. Once the status update succeeds, Kueue issues a graceful delete request (`client.Delete(ctx, pod)`) against the Pod. If the delete call fails, the finalizer remains in place and controller-runtime requeues. Repeated reconciliations safely observe `DeletionTimestamp` and avoid re-issuing duplicate requests.
5. **Quota preservation during teardown**: The Pod retains its `kueue.x-k8s.io/managed` finalizer while deleting. Because the Workload is not marked finished, Kueue keeps quota reserved while any remaining running containers shut down, honoring `pod.Spec.TerminationGracePeriodSeconds`.
6. Once all containers terminate, kubelet transitions the Pod phase to `Failed`.
7. On subsequent reconciliation, `utilpod.IsTerminated(pod)` returns true. Because kubelet may leave `pod.Status.Message` empty on container failure, `Pod.Finished()` falls back to reading the `TerminationTarget` condition message or failed container status, ensuring the Workload condition identifies the failed container name and exit code. Kueue then marks the Workload `Finished=True, Reason=Failed`, removes the finalizer, and releases quota.

### MultiKueue Convergence

In MultiKueue deployments:
1. The workload and Pod are dispatched to the worker cluster.
2. The worker cluster's Kueue pod controller detects the container failure, triggers graceful deletion, and finalizes the worker Workload with `Finished=True, Reason=Failed`.
3. MultiKueue's `SyncJob` copies the worker Pod's terminal status while the management-cluster Pod is not terminating. If the management-cluster Pod is terminating, status synchronization is skipped.
4. MultiKueue's workload controller reads the worker Workload's `Finished` condition and marks the management cluster Workload `Finished=True` with matching Reason and Message.
5. The propagated `Finished` condition releases quota on both clusters even if management-cluster Pod status synchronization is skipped. The management-cluster Pod is finalized and cleaned up normally through its existing lifecycle.

## User Stories

### Story 1: Multi-Container Batch Job with Missing Worker Dependency

As a platform engineer dispatching multi-container batch Pods via MultiKueue, my application runs a data processing container alongside logging and sync helper containers. When the primary container fails to start due to a missing Secret on a worker cluster, the sync helper container exits with code 1. I want Kueue to detect this unrecoverable state, stop the stalled Pod gracefully, mark the Workload failed, and release quota so other jobs in the queue can run.

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: batch-job-multi-container
  namespace: batch-workloads
  annotations:
    kueue.x-k8s.io/pod-fail-fast: "true"
  labels:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  restartPolicy: Never
  containers:
  - name: main-processor
    image: custom-batch:v1
    command: ["run-processing"]
    envFrom:
    - secretRef:
        name: worker-dataset-credentials
  - name: sync-helper
    image: sync-helper:v1
    command: ["sh", "-c", "wait-for-main || exit 1"]
```

### Story 2: Standalone Plain Pod with Disabled WaitForPodsReady

As a cluster administrator running batch jobs where `DisableWaitForPodsReady=true` is set to avoid timing out large image pulls, I need Kueue to recognize when a container has permanently died with exit code 1 under `restartPolicy: Never`. Rather than holding GPU and CPU reservations until someone manually deletes the Pod, Kueue should fail fast and clear the queue reservation.

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: standalone-gpu-task
  namespace: batch-workloads
  annotations:
    kueue.x-k8s.io/pod-fail-fast: "true"
  labels:
    kueue.x-k8s.io/queue-name: gpu-queue
spec:
  restartPolicy: Never
  containers:
  - name: trainer
    image: gpu-model-trainer:v1
    resources:
      requests:
        nvidia.com/gpu: 2
  - name: telemetry-exporter
    image: telemetry:v1
    command: ["sh", "-c", "sleep 10; exit 1"]
```

## Design Details

### API

- **Feature Gate**: `PlainPodFailFast` in Kueue's feature gate registry (Alpha, disabled by default).
- **Pod Annotation**: `kueue.x-k8s.io/pod-fail-fast` on plain Pod metadata.

### Validation

- The annotation accepts only the exact string `"true"`. Values other than `"true"` (such as `"false"` or arbitrary text) are ignored by the controller and treated as disabled.
- The annotation is immutable after Pod creation. Modifying or adding the annotation to an already-admitted Pod is rejected during validation to maintain deterministic lifecycle guarantees.

### Handling

- When the `PlainPodFailFast` feature gate is disabled, Kueue completely ignores the `kueue.x-k8s.io/pod-fail-fast` annotation, preserving existing plain-Pod behavior without initiating deletion or changing workload completion.
- If the feature gate is disabled while Pods have an existing `TerminationTarget` condition, ongoing graceful deletion and finalizer cleanup proceed normally without newly triggering fail-fast on other running Pods.

### Controller Logic

The plain Pod reconciler checks for unrecoverable container failures during standard reconciliation:
1. Verify the feature gate is enabled and the Pod carries the opt-in annotation.
2. Confirm the Pod has `spec.restartPolicy: Never`, is not deleting (`deletionTimestamp.IsZero()`), and is not terminal.
3. For each container status in `status.containerStatuses`, match it to its corresponding entry in `spec.containers`. Exclude containers specifying container-level restart policies or restart rules.
4. If any matching container status reports `state.terminated` with a non-zero exit code:
   - Apply the `TerminationTarget` condition to the Pod status with reason `UnrecoverableContainerFailure` and message detailing the failed container.
   - Issue a graceful delete request against the Pod.
   - Preserve the finalizer to retain quota reservation during the termination grace period.

When the Pod reaches terminal phase, `Pod.Finished()` populates the Workload failure condition from the `TerminationTarget` condition or container status message, removes the finalizer, and releases quota.

### Interactions with WaitForPodsReady and Other Features

- **`waitForPodsReady`**: `PlainPodFailFast` functions independently of `waitForPodsReady`. When `waitForPodsReady` is enabled, fail-fast triggers immediately upon container failure without waiting for `timeout` to expire, avoiding unnecessary queue retention. When `waitForPodsReady` is disabled (`DisableWaitForPodsReady=true`), fail-fast acts as the primary safeguard against stuck failed attempts.
- **`ObjectRetentionPolicies`**: Unaffected. Workload and Pod retention countdowns begin only after the Workload reaches `Finished=True`.

### Test Plan

#### Unit Tests
- Plain Pod with `restartPolicy: Never`, one container Waiting, one container Terminated (`exitCode: 1`) triggers `TerminationTarget` and deletion.
- Plain Pod with `restartPolicy: Never`, container Terminated with `exitCode: 0` does not trigger fail-fast.
- Plain Pod with `restartPolicy: Always` does not trigger fail-fast.
- Plain Pod without `kueue.x-k8s.io/pod-fail-fast: "true"` annotation does not trigger fail-fast.
- Feature gate disabled does not trigger fail-fast even when annotated.
- Native sidecar in `initContainers` with `restartPolicy: Always` terminating with exit code 1 does not trigger fail-fast.
- Status update and delete failure error handling and retry safety.

#### Integration Tests
- **Single-Cluster**: Full lifecycle testing: admit annotated Pod, simulate container failure, verify `TerminationTarget` and `deletionTimestamp`, simulate container shutdown to `PodFailed`, verify Workload `Finished=True, Reason=Failed` and quota release.
- **MultiKueue**: Multi-cluster integration test: admit Workload via MultiKueue, trigger worker-side fail-fast deletion, verify management-cluster Pod handling, verify management Workload completion, and verify quota release on both manager and worker clusters.

#### End-to-End (e2e) Tests
Dedicated e2e tests are omitted because the combination of unit tests and integration tests with `envtest` fully validates the controller reconciliation flow, condition transitions, graceful deletion, and MultiKueue synchronization without requiring multi-node cloud clusters.

## Graduation Criteria

### Alpha (v0.20)
- Feature gate `PlainPodFailFast` introduced (disabled by default).
- Pod annotation `kueue.x-k8s.io/pod-fail-fast` implemented with validation.
- Unit and integration tests added, including MultiKueue lifecycle verification.

### Beta (v0.21)
- Feature gate enabled by default.
- Review the annotation for promotion to a formal API field in `PodSpec` or `WorkloadSpec`.
- Concrete decision on whether to support Pod groups based on user feedback.
- MultiKueue synchronization verified across releases.

### Stable (v0.22+)
- Feature gate locked to GA.
- Annotation fully supported or superseded by formal API field.

## Future Work Ideas

- **Pod Groups**: Evaluate extending fail-fast handling to non-retriable Pod groups (`kueue.x-k8s.io/pod-group-name`), where a failure in one member Pod could optionally fail the entire group attempt.
- **Cluster-Wide Configuration**: Evaluate adding an opt-in knob under `integrations.frameworkConfigs` for the `pod` integration to allow administrators to enable fail-fast across namespaces by default.

## Alternatives

### Alternative 1: Rely solely on WaitForPodsReady
Relying on `waitForPodsReady` requires configuring a generic timeout. If timeouts are set long (e.g. 15–30 minutes) to allow large container images to pull, quota remains blocked for that entire duration even when a container has permanently died within seconds. Furthermore, users running with `DisableWaitForPodsReady=true` have no protection.

### Alternative 2: Immediately mark Workload Finished without deleting the Pod
If Kueue marks the Workload finished immediately upon seeing a failed container without waiting for the Pod to terminate, Kueue would release quota while other containers in the Pod could still be running or could start if the blocking condition clears. This violates quota reservation guarantees. Calling `client.Delete` and waiting for terminal Pod phase ensures that resources are never used unreserved.

### Alternative 3: ClusterQueue or Configuration-level default
A cluster-wide or queue-wide setting could force fail-fast on all plain Pods. However, some batch workflows use multi-container Pods where secondary containers perform post-failure cleanup, log uploading, or result persistence after a primary container exits with an error. Forcing fail-fast globally would break those workflows. An opt-in annotation provides the safest and most flexible boundary for Alpha.
