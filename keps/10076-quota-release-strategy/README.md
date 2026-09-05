# KEP-10076: Configurable Quota Release Strategy

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Configuration Example](#configuration-example)
  - [User Stories](#user-stories)
    - [Story 1: TAS Bare-Metal GPU Cluster Administrator](#story-1-tas-bare-metal-gpu-cluster-administrator)
    - [Story 2: Standard Batch Workload Administrator](#story-2-standard-batch-workload-administrator)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Configuration API &amp; YAML Manifest](#configuration-api--yaml-manifest)
  - [Go API Surface](#go-api-surface)
  - [Implementation overview](#implementation-overview)
  - [Integration Termination Criteria](#integration-termination-criteria)
  - [Integration Release Behavior Summary](#integration-release-behavior-summary)
  - [Compatibility &amp; Defaulting](#compatibility--defaulting)
  - [Test Plan](#test-plan)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
- [Graduation Criteria](#graduation-criteria)
  - [Alpha (v0.20)](#alpha-v020)
  - [Beta](#beta)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This proposal introduces a per-integration configuration setting `.integrations.quotaReleaseStrategy` in Kueue's Configuration API (`apis/config/v1beta2`), gated by the `QuotaReleaseStrategy` Feature Gate (Alpha, disabled by default in v0.20). It allows cluster administrators to configure when Kueue releases workload quota reservation during eviction and preemption on a per-framework basis — either immediately upon deletion initiation / status change (`OnQuotaReleased`) or delayed until all underlying pods reach a terminal phase (`OnTerminal`).

## Motivation

In environments utilizing Topology-Aware Scheduling (TAS) or bare-metal GPU clusters, releasing quota immediately when a `Workload` is marked `Finished` causes severe capacity accounting issues (Issue #10076). 

When a workload terminates, its pods may remain in the `Terminating` phase for minutes (e.g. PyTorch checkpointing via `terminationGracePeriodSeconds`) or hours (e.g. hardware PCIe/GPU errors). If TAS capacity is released immediately upon workload termination, TAS assigns newly admitted workloads to the same host via hard `nodeSelector`. `kube-scheduler` cannot place the new pods because the `Terminating` pods physically still occupy the node resources, leading to persistent `FailedScheduling` loops.

### Goals

- Provide a per-integration Configuration API setting `.integrations.quotaReleaseStrategy` to control quota release timing across Kueue integrations during eviction and preemption, guarded by the `QuotaReleaseStrategy` feature gate.
- Support `OnQuotaReleased` (implicit default for all frameworks) for fast quota release upon workload eviction.
- Support `OnTerminal` in Alpha (v0.20) specifically for `batch/v1.Job` and `Pod` integrations to delay quota release until underlying pods physically transition to a terminal state (`Succeeded` or `Failed`) upon eviction.

### Non-Goals

- Changing Topology-Aware Scheduling (TAS) capacity release timing across all workload completion and deletion paths (deferred to a follow-up enhancement).
- Gating normal workload completion (`job.Finished`) or altering the `Cache.DeleteWorkload` lifecycle.
- Replacing pod failure recovery or node readiness controllers.
- Modifying kube-scheduler or kubelet eviction logic.

## Proposal

### Notes/Constraints/Caveats

- **`QuotaReleaseStrategy` Feature Gate**: The feature is guarded by the `QuotaReleaseStrategy` feature gate (Alpha, disabled by default in v0.20). When disabled, Kueue defaults to `OnQuotaReleased`.
- **Deprecation of `FastQuotaReleaseInPodIntegration`**: The `FastQuotaReleaseInPodIntegration` feature gate (introduced in KEP-6143 for Pod integration) is deprecated in v0.20 and superseded by `.quotaReleaseStrategy`. When `.quotaReleaseStrategy` is configured and enabled, it takes precedence over the legacy feature gate.

### Configuration Example

Administrators configure the strategy under `integrations` in the Kueue `Configuration` ConfigMap:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
namespace: kueue-system
integrations:
  frameworks:
  - "batch/job"
  - "pod"
  quotaReleaseStrategy:
    "batch/job": OnTerminal
waitForPodsReady:
  timeout: 30m
```

### User Stories

#### Story 1: TAS Bare-Metal GPU Cluster Administrator
As a cluster admin running 8x A100 GPU nodes with Topology-Aware Scheduling, I want Kueue to keep host capacity reserved while pods are terminating so that newly admitted workloads are not assigned to hosts currently occupied by terminating pods.

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
namespace: kueue-system
integrations:
  frameworks:
  - "batch/job"
  quotaReleaseStrategy:
    "batch/job": OnTerminal
```

#### Story 2: Standard Batch Workload Administrator
As a cluster admin running standard batch workloads, I want quota released immediately when deletion is initiated (`OnQuotaReleased`) so subsequent workloads can be admitted without waiting for pod teardown.

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
namespace: kueue-system
integrations:
  frameworks:
  - "batch/job"
  quotaReleaseStrategy:
    "batch/job": OnQuotaReleased
```

### Risks and Mitigations

- **Risk**: Under `OnTerminal`, if a pod gets stuck terminating indefinitely on a reachable node (e.g. due to hardware PCIe errors or kernel driver deadlocks where the node remains `Ready`) or too long gracefulTerminationPeriod, Kueue will hold the quota reservation indefinitely. Kueue's failure recovery does not apply to reachable nodes.
- **Mitigation / Trade-off**: This is an accepted trade-off for clusters opting into `OnTerminal`, which prioritize avoiding `FailedScheduling` loops on occupied nodes over aggressive quota reclamation. Quota is held until external remediation (e.g., node problem detector, node auto-repair, or admin intervention) removes the stuck pod. Clusters prioritizing rapid quota turnover can remain on the default `OnQuotaReleased` strategy.

## Design Details

### Configuration API & YAML Manifest

The `quotaReleaseStrategy` map is configured under `integrations` in the Kueue `Configuration` ConfigMap:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
namespace: kueue-system
integrations:
  frameworks:
  - "batch/job"
  - "pod"
  quotaReleaseStrategy:
    "batch/job": OnTerminal
waitForPodsReady:
  timeout: 30m
```

### Go API Surface

In `apis/config/v1beta2/configuration_types.go`:

```go
type QuotaReleaseStrategy string

const (
    // QuotaReleaseOnQuotaReleased releases quota as soon as deletion is initiated
    // or the workload status is updated.
    QuotaReleaseOnQuotaReleased QuotaReleaseStrategy = "OnQuotaReleased"

    // QuotaReleaseOnTerminal holds quota until all underlying pods
    // have reached a terminal phase (Succeeded or Failed).
    QuotaReleaseOnTerminal QuotaReleaseStrategy = "OnTerminal"
)

type Integrations struct {
    Frameworks []string `json:"frameworks,omitempty"`
    ExternalFrameworks []string `json:"externalFrameworks,omitempty"`
    LabelKeysToCopy []string `json:"labelKeysToCopy,omitempty"`

    // QuotaReleaseStrategy provides per-framework configuration options for controlling quota release timing.
    // Keys correspond to framework names in Frameworks (e.g. "batch/job", "pod").
    // In Alpha (v0.20), "OnTerminal" is supported only for "batch/job" and "pod".
    // All other frameworks implicitly default to "OnQuotaReleased".
    // +optional
    QuotaReleaseStrategy map[string]QuotaReleaseStrategy `json:"quotaReleaseStrategy,omitempty"`
}
```

### Implementation overview

The per-integration setting `.integrations.quotaReleaseStrategy[framework]` specifically governs the eviction and preemption lifecycle by configuring each integration's `job.IsActive(ctx, strategy)` behavior in the `JobReconciler`:

- If `QuotaReleaseStrategy` feature gate is disabled, `r.quotaReleaseStrategy` defaults to `OnQuotaReleased`.
- If enabled, each integration reconciler is initialized with its configured strategy (defaulting to `OnQuotaReleased` if omitted).
- **Eviction / Preemption path (`job.IsActive`)**: When a workload is evicted (eg. preempted), Kueue unsets the quota reservation once `!job.IsActive(ctx, strategy)`:
  - **`OnQuotaReleased`**: `IsActive(ctx, strategy)` returns `false` as soon as deletion is initiated or `job.Status.Active == 0`, allowing quota to be reclaimed promptly.
  - **`OnTerminal`**: `IsActive(ctx, strategy)` returns `true` as long as underlying pods remain active or terminating (e.g., `ptr.Deref(job.Status.Terminating, 0) > 0` for `batch/v1.Job`, or pods are still running/terminating for `PodGroup`). Quota reservation and TAS capacity remain held until all pods finish terminating.
- **Completion path (`job.Finished`)**: Normal job completion logic remains unchanged; `OnTerminal` does not alter `job.Finished(ctx)`.

### Integration Termination Criteria

In Alpha (v0.20), **`OnTerminal`** strategy support is specifically implemented for **`batch/v1.Job`** and **`Pod`** integrations. Other integrations default to **`OnQuotaReleased`**, with support for `OnTerminal` expanded to additional frameworks in Beta as child pod observation is supported.

- Supported in Alpha (v0.20):
  - **`batch/v1.Job`**: Evaluates `job.Status.Active` and `job.Status.Terminating`. Fully terminated when `ptr.Deref(job.Status.Active, 0) == 0` AND `ptr.Deref(job.Status.Terminating, 0) == 0`.
  - **Single `Pod`**: Evaluates `pod.Status.Phase`. Fully terminated when `pod.Status.Phase == corev1.PodSucceeded` OR `pod.Status.Phase == corev1.PodFailed`.
  - **`PodGroup` (StatefulSet, LeaderWorkerSet)**: Evaluates member Pod statuses. Fully terminated when all member Pods reach terminal phase (`Succeeded` or `Failed`).
- Planned for Beta expansion:
  - **`JobSet`**: Evaluates `ReplicatedJobsStatus`. Fully terminated when for all replicated jobs, `Active == 0` AND `Terminating == 0`.
  - **`Kubeflow` (PyTorchJob, MPIJob, TFJob, PaddleJob, JAXJob, XGBoostJob)**: Evaluates operator replica statuses.
  - **`Ray` (RayJob, RayCluster, RayService)**: Evaluates RayCluster pod phases.
  - **`AppWrapper` & `SparkApplication`**: Evaluates wrapped pod phases.

We didn't finalize approaches for the Integrations listed in the second phase to track the fully terminated state. We will revisit the evaluation way before Beta graduation.

### Integration Release Behavior Summary

The following table summarizes the effective default quota release behavior across all registered integrations prior to KEP-10076:

| Registered Integration / Execution Path | Effective Default Policy | Activity / Quota-Release Signal | Explicit Child-Pod Observation | Important Limitations |
| :--- | :--- | :--- | :--- | :--- |
| **pod (single Pod)** | `OnQuotaReleased` | `Pod.IsActive()` returns false for a non-group Pod | No | Admission can be cleared in the same reconciliation that starts Pod deletion. Terminal phase is not awaited. |
| **pod (PodGroup, default)** | `OnTerminal` | Active while at least one member Pod is Running and has not exceeded grace period | Yes (Pod API) | Only Running Pods count. Quota released after grace-period expiry even if Pod still reports Running. |
| **pod (PodGroup, FastQuotaReleaseInPodIntegration=true)** | `OnQuotaReleased` | Pod with deletionTimestamp ignored by `IsActive()` | Yes | Explicit non-default feature-gate configuration. |
| **deployment** | `OnQuotaReleased` | Each child Pod handled as single-Pod workload | No | No Deployment-level terminal check. Each Pod owns an independent Workload. |
| **statefulset (eviction path)** | `OnTerminal` | Inherits PodGroup `IsActive()` behavior | Yes | Applies only to eviction path and inherits grace-period cutoff. |
| **statefulset (replicas=0)** | `OnQuotaReleased` | Reconciler directly clears Workload quota reservation | No | Quota released before terminating StatefulSet Pods disappear. |
| **leaderworkerset (eviction path)** | `OnTerminal` | Each replica Workload inherits PodGroup `IsActive()` behavior | Yes | Applies per replica and inherits grace-period cutoff. |
| **leaderworkerset (scale-down/rollout)** | `OnQuotaReleased` | Reconciler directly deletes Workload | No | Workload deletion releases quota without waiting for replica Pods to reach terminal phase. |
| **batch/job** | `OnQuotaReleased` | `Job.status.active == 0` | No | K8s excludes terminating Pods from active count. Kueue does not independently inspect child Pods. |
| **jobset** | `OnQuotaReleased` | Every `ReplicatedJobsStatus[].Active == 0` | No | Relies on JobSet/Job status. Deleting child Jobs/Pods are not independently inspected. |
| **kubeflow (jaxjob, paddlejob, tfjob, xgboostjob, mpijob)** | `OnQuotaReleased` | Every replica `status.active == 0` | No | Relies entirely on operator status. |
| **kubeflow (pytorchjob)** | `OnQuotaReleased` | Every replica `status.active == 0` | No | Operator sets Active=0 after starting cleanup but before Pods physically disappear. |
| **trainer.kubeflow (trainjob)** | `OnQuotaReleased` | Every `JobsStatus[].Active == 0` | No | Relies on child Job status rather than observing child Pods. |
| **appwrapper** | `OnQuotaReleased` | `QuotaReserved` status condition no longer true | No | Timing delegated to AppWrapper controller. Kueue does not verify resource termination. |
| **ray (raycluster)** | `OnQuotaReleased` | `RayCluster.status.state != Ready` | No | Non-Ready state is not proof that every Ray Pod has terminated. |
| **ray (rayjob)** | `OnQuotaReleased` | `JobDeploymentStatus` becomes Suspended/New | No | Trusts RayJob status transition without inspecting underlying RayCluster Pods. |
| **ray (rayservice)** | `OnQuotaReleased` | `RayServiceReady` condition no longer true | No | False Ready condition is not proof that active/pending RayCluster has physically terminated. |
| **sparkapplication** | `OnQuotaReleased` | Application state no longer Running | No | Trusts SparkApplication status without inspecting driver or executor Pods. |

### Compatibility & Defaulting

- **Default Strategy**: If not explicitly configured for a framework, the strategy implicitly defaults to `OnQuotaReleased`.
- **Feature Gate**: Guarded by the `QuotaReleaseStrategy` feature gate (Alpha, disabled by default in v0.20).
- **Validation**: In Alpha (v0.20), configuration validation ensures that only supported frameworks (`"batch/job"` and `"pod"`) can be configured with `OnTerminal`. Attempting to configure `OnTerminal` for unsupported frameworks (e.g. `AppWrapper`) returns a validation error. As additional integrations support child pod observation in Beta, validation will be relaxed per framework.
- **Supported API Versions**: Supported in the `v1beta2` Configuration API. It is intentionally omitted from `v1beta1` to follow Kubernetes API evolution guidelines.
- **Upgrade Impact on PodGroups/StatefulSets/LWS**: For `PodGroup` (and by extension `StatefulSet` and `LeaderWorkerSet` eviction paths), defaulting to `OnQuotaReleased` is an accepted upgrade change that releases quota upon termination initiation (equivalent to enabling the legacy `FastQuotaReleaseInPodIntegration` feature gate). This eliminates head-of-line blocking during PodGroup preemption.
- **Opting into `OnTerminal`**: Cluster administrators who require delayed quota release until underlying pods reach terminal phase (`Succeeded`/`Failed`) can explicitly configure `integrations.quotaReleaseStrategy: { "batch/job": "OnTerminal" }`.

### Test Plan

#### Unit tests
- `pkg/config`: Validate `QuotaReleaseStrategy` configuration parsing, defaulting, and field validation.
- `pkg/controller/jobs/...`: Test `IsActive(ctx)` across **all** supported integrations under both `OnTerminating` and `OnTerminal` strategies:
  - `pod`: Pod integration (`IsActive` returns true until pod is Succeeded/Failed under `OnTerminal`).
  - `job`: `batch/v1.Job` integration (`IsActive` inspects `status.active` and `status.terminating`).
  - `jobset`: JobSet integration.
  - `kubeflow`: PyTorchJob, MPIJob, TFJob, PaddleJob, JAXJob, XGBoostJob integrations.
  - `ray`: RayJob, RayCluster integrations.
  - `appwrapper`: AppWrapper integration.
  - `pod-based`: Deployment and StatefulSet integrations.

#### Integration tests
- Verify quota release timing during eviction and preemption under both strategies.

## Graduation Criteria

### Alpha (v0.20)
- Introduced `QuotaReleaseStrategy` Feature Gate (Alpha, disabled by default in v0.20).
- Introduced `.integrations.quotaReleaseStrategy` in `v1beta2` Config API.
- Implemented `OnTerminal` strategy support and validation for core `batch/v1.Job` and `Pod` integrations.

### Beta
- Expand `OnTerminal` strategy support scope to additional integrations as child pod observation is supported.
- Relax configuration validation to allow `OnTerminal` on newly supported integrations.
- Gather user feedback from production TAS and bare-metal GPU clusters.

## Implementation History

- **2026-08-15**: Initial KEP provisional proposal submitted targeting v0.20 Alpha.

## Drawbacks

- Under `OnTerminal`, keeping `IsActive()` true until all underlying pods reach a terminal phase (`Succeeded`/`Failed`) means that if pods get stuck terminating or an integration controller fails to report terminal status (e.g., #14811), Kueue's eviction reconciliation exits waiting for status updates without scheduling a retry. In these scenarios, quota reservations can be held indefinitely rather than merely delayed, completely stalling quota recovery until manual or external remediation occurs. This is mitigated by defaulting `.quotaReleaseStrategy` to `OnQuotaReleased`.

## Alternatives

- **Feature gates instead of Configuration API**: Rejected because a Configuration API knob is required for administrators to explicitly select release behavior across cluster workloads.
- **Explicit parameter for `QuotaReleaseStrategy`**: Passing `QuotaReleaseStrategy` as an explicit parameter to `IsActive(ctx context.Context, strategy configapi.QuotaReleaseStrategy) bool` instead of `context.WithValue` keeps the method signature explicit, avoids hidden context dependencies, and simplifies unit testing across adapters.
- **`nonTasUsageCache` in scheduler**: Maintaining a separate scheduler-side cache for non-TAS / terminating pod usage was rejected because it introduces additional cache synchronization complexity in the scheduler rather than addressing the core activity lifecycle in job controllers.
- **Lazy TAS-pod-awareness in scheduler**: Having the TAS scheduler dynamically inspect underlying Pod objects during scheduling cycles was rejected because the scheduler must rely on cached `Workload` objects for high throughput and should not perform ad-hoc API queries during scheduling cycles.
- **Dedicated background controller (`PodUsageReconciler`)**: Introducing a separate controller to watch pods and adjust quota reservations was rejected because it introduces additional controller and informer overhead, potential race conditions with `Workload` status reconciliation, and unnecessary complexity when existing job framework reconcilers already manage job activity.
