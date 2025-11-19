# KEP-6757: Failure Recovery

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [Replacing Pods Which Are Still Running](#replacing-pods-which-are-still-running)
- [Design Details](#design-details)
  - [Affected Pods](#affected-pods)
  - [Grace Period](#grace-period)
  - [Observability](#observability)
  - [Implementation Overview](#implementation-overview)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration Tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
  - [Beta](#beta)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Do Nothing](#do-nothing)
  - [Control All Pods Managed By Kueue](#control-all-pods-managed-by-kueue)
  - [Introduce a <code>FailureRecoveryPolicy</code> API](#introduce-a-failurerecoverypolicy-api)
  - [Leveraging Core Kubernetes Non-Graceful Node Shutdown Handling](#leveraging-core-kubernetes-non-graceful-node-shutdown-handling)
<!-- /toc -->

## Summary

This KEP introduces an opt-in mechanism of timeout-based graceful handling of zombie pods,
i.e. pods that are stuck in the `Pending`/`Running` state due to a node-level failure.

## Motivation

Currently, a network partition or a malfunction of the `kubelet` (or a more general node failure) results in the pods running on that node to become stuck.
When the `kubelet` fails to send its regular heartbeat to the control plane within `node-monitor-grace-period`, the node is deemed unhealthy and is assigned the `node.kubernetes.io/unreachable` taint. By default, Kubernetes [automatically adds a 5-minute toleration](https://kubernetes.io/docs/concepts/scheduling-eviction/taint-and-toleration/#taint-based-evictions) for pods running in the affected node (this can be explicitly set by a different value by the user/controller).
If the heartbeat is resumed within this toleration period, the control plane removes the taints and the pods continue running.
On the other hand, if it is not resumed, then the pods are marked for termination (i.e. their `deletionTimestamp` is set in `etcd`).

The complete flow of pod termination in this case is as follows:
1. `node-a` stops sending a heartbeat to the control plane.
1. **After `node-monitor-grace-period` elapses ([50 seconds by default](https://kubernetes.io/docs/reference/node/node-status/#condition))**, the `node.kubernetes.io/unreachable` taint is added to `node-a`.
1. A toleration ([5 minutes by default](https://kubernetes.io/docs/concepts/scheduling-eviction/taint-and-toleration/#taint-based-evictions)) for the `node.kubernetes.io/unreachable` is added to pods running on `node-a`.
1. **After the toleration time elapses**, the control plane marks the pods for termination by setting their `deletionTimestamp`
and `deletionGracePeriodSeconds` ([30 seconds by default](https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/#pod-termination-flow)).
1. **After the deletion grace period elapses**, the `kubelet` should send the `SIGKILL` signal to the pods to terminate them and set their phase accordingly.
Regardless of whether this happens or not, the control plane will not see this update because of the lack of communication with the `kubelet`.
1. The pod remains in a `Terminating` state.

The pod can also become "stuck" if the loss of communication occurs **after** the pod was marked for termination:

1. `pod-a` running on `node-a` is marked for termination. Its `deletionTimestamp` and `deletionGracePeriodSeconds` are set.
1. `node-a` stops sending a heartbeat to the control plane.
1. **After the deletion grace period elapses**, the `kubelet` should send the `SIGKILL` signal to the pods to terminate them and set their phase accordingly.
Regardless of whether this happens or not, the control plane will not see this update because of the lack of communication with the `kubelet`.
1. The pod remains in a `Terminating` state.

Without a functioning `kubelet`, the pods have no way of progressing beyond that point on their own.
Such pods, colloquially called "zombie pods", remain terminating until the node is healed or they are manually removed.
This is a crucial safety measure, as in that scenario the control plane has no way to confirm that the pods actually stopped
and released all their resources. It is especially relevant for stateful applications and might result in data corruption.
For example, the stuck pod might still be writing to a `PersistentVolume` when a replacement is started
(which is likely in the case of network partitions, where the node is functioning correctly, just unable to communicate with the control plane).

Nevertheless, it became clear that in some cases having a mechanism that would unblock replacement pods from starting would be beneficial.
In particular, `Job`-based `Workload`s with `podReplacementPolicy: Failed` are unable to make progress when this issue occurs and require manual administration intervention.

### Goals

* Maximize the quota usage for `Workload`s by recovering from a common failure pattern that:
  * Prevents `Job`s using `podReplacementPolicy: Failed` from making progress.

### Non-Goals

* Detecting whether a zombie pod was terminated properly and released all it's resources.

## Proposal

* Introduce a controller that moves zombie `Pod`s into the `Failed` phase.
  * The controller is enabled via a feature gate.
* Introduce a `kueue.x-k8s.io/safe-to-forcefully-terminate` annotation that limits which pods are affected by the new controller.

### User Stories (Optional)

#### Story 1

I'm a user running a distributed Machine Learning workload based on `Job`.
I set `podReplacementPolicy: Failed` to prevent duplicate task registration errors during node drains or preemptions,
as my framework expects exactly one pod per worker index.

Some nodes in my cluster often go offline for long periods of time, during which my job is unable to make progress -
replacement pods will not be scheduled as long as the node remains offline. I'd like for my jobs to be automatically unblocked
as soon as possible, so I can make best use of the available resources (since the training process is synchronized,
there is no risk of data corruption in my case).

### Risks and Mitigations

#### Replacing Pods Which Are Still Running

Starting a replacement pod without a guarantee that the original one was properly terminated carries an inherent risk
and might result in inconsistencies, as discussed above.
If this risk is not communicated to the user properly and the feature is not sufficiently explicit in its behavior,
the users might mistake it for a general issue within Kueue/the broader Kubernetes ecosystem.

As this risk stems from the behavior of the `kubelet` and control plane themselves, it cannot be fully mitigated without major changes in how Kubernetes operates.
Instead, it should be adequately documented to prevent users without a compatible use-case from using it.
Moreover, enabling this feature should require opt-in both from:
1. The Administrator - by enabling the feature globally.
2. The User - by opting specific workloads into the failure recovery mechanism.

Controlling the covered workloads, instead of applying the recovery globally, will guarantee that only pods that were deemed "safe to forcefully terminate" are affected.

## Design Details

### Affected Pods

In order to allow the first adopters to control which pods are affected by the new controller, a new `Pod` annotation will be introduced:
```yaml
kueue.x-k8s.io/safe-to-forcefully-terminate: "true"
```

Only pods containing the new annotation will be affected by the controller, for example:
```yaml
apiVersion: batch/v1
kind: Job
metadata:
  generateName: sample-job-
  namespace: default
  labels:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  parallelism: 3
  completions: 3
  podReplacementPolicy: Failed
  template:
    metadata:
      annotations:
        kueue.x-k8s.io/safe-to-forcefully-terminate: "true"  // <- new annotation
    spec:
      containers:
      - name: dummy-job
        image: registry.k8s.io/e2e-test-images/agnhost:2.53
        command: [ "/bin/sh" ]
        args: [ "-c", "sleep 600" ]
        resources:
          requests:
            cpu: "1"
            memory: "200Mi"
      restartPolicy: Never
```

> The controller is not limited to pods managed by Kueue (i.e. having the `kueue.x-k8s.io/managed` or `kueue.x-k8s.io/podset` label). All pods in the cluster that have the new annotation will be affected.

### Grace Period

When a pod is marked for termination, it's assigned with a `deletionGracePeriodSeconds`.
This grace period defines the amount of time the pod has to terminate gracefully.
This KEP defines "pods stuck terminating" as pods that were not terminated **1 minute** after the `deletionGracePeriodSeconds` elapsed.
The value of 1 minute is chosen to get initial feedback about the feature and help inform the decision on the structure of the API and setting defaults makes sense when graduating to beta.

### Observability

Since this feature relies on interplay with existing Kubernetes mechanism, it should be possible for users to trace
pod terminations back to the new controller. This will allow for easier troubleshooting and attribution, which is crucial
when gathering initial user feedback.

To address that, a new `ForcefullyTerminated` `PodCondition` object will be assigned to the pod when it is terminated by the new controller.

```yaml
apiVersion: v1
kind: Pod
spec:
  # ...
  terminationGracePeriodSeconds: 30
status:
  conditions:
  # grace period = `terminationGracePeriodSeconds` + 60s (default threshold in the KEP)
  - message: 'Pod forcefully terminated after 90s grace period due to unreachable node `node-a` (triggered by `kueue.x-k8s.io/safe-to-forcefully-terminate annotation`)'
    reason: KueueForcefullyTerminated
    status: "True"
    type: KueueFailureRecovery
    # ...
```

Additionally, an analogous `Warning` event will be emitted for the affected pod:

```yaml
apiVersion: v1
kind: Event
type: Warning
involvedObject:
  apiVersion: v1
  kind: Pod
  name: pod-a
  namespace: default
message: 'Pod forcefully terminated after 90s grace period due to unreachable node `node-a` (triggered by `kueue.x-k8s.io/safe-to-forcefully-terminate annotation`)'
reason: KueueForcefullyTerminated
reportingComponent: pod-termination-controller
source:
  component: pod-termination-controller
```

### Implementation Overview

Enabling the `FailureRecoveryPolicy` feature gate will turn on a controller, which manages the failed nodes.

The controller has to **ignore** updates to pods that:
1. Are not terminating.
    * `pod.DeletionTimestamp == nil`
1. Are in a terminal phase.
    * `pod.Status.Phase != corev1.PodRunning && pod.Status.Phase != corev1.PodPending`
1. Are not annotated with the new `kueue.x-k8s.io/safe-to-forcefully-terminate` annotation.
1. Are **not** scheduled on a node tainted with `node.kubernetes.io/unreachable`.
    * This explicitly ignores pods assigned to nodes that still have a running kubelet.
    For example, nodes with the `node.kubernetes.io/not-ready` taint experiencing resource pressure
    that makes pod termination take longer.

For relevant (not ignored) terminating pods, the reconciliation behaves in the following way:
1. It computes the amount of time elapsed since the the pod's `gracefulTerminationGracePeriod` elapsed:
    1. If it's below the default timeout of **1 minute**, the reconciler will requeue the object to be re-evaluated once the thershold is reached.
    1. Otherwise, if the threshold of **1 minute** was reached, the pod will be deemed "zombie" and transitioned into the `PodFailed` phase.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Unit Tests

The proposal will be covered with unit tests for:
1. Configuration parsing.
1. Controller behavior:
    1. Whether it ignores irrelevant pods (not terminating, already failed/succeeded, not annotated).
    1. Whether it correctly schedules a reconciliation for when the grace period elapses.
    1. Whether it updates the pod's phase to `Failed` after the grace period elapses.

#### Integration Tests

The proposal will be covered with integrations tests that check whether:
1. Adding a deletion timestamp to the pod requeues a reconciliation loop for when the grace period elapses.
1. After the grace period elapses, the pod is marked as `Failed`.
1. Replacement pods are scheduled in the place of the failed pod (optional, technically 1+2 is sufficient to prove this).

Existing integration tests should prove that this feature does not impact Kueue during normal operation.

### Graduation Criteria

#### Alpha

- Feature gate disabled by default.
- Positive feedback from the users.

### Beta

- Feature gate enabled by default.
- Re-evaluate the introduction of the `FailureRecoveryPolicy` API.

## Implementation History

2025-09-08: The [issue](https://github.com/kubernetes-sigs/kueue/issues/6757) is raised in Kueue.

2025-09-12: The [issue](https://github.com/kubernetes/kubernetes/issues/134038) is raised in core Kubernetes.

2025-10-17: First draft of the KEP.

## Drawbacks

* The same feature is discussed in core Kubernetes ([kubernetes/issues/134038](https://github.com/kubernetes/kubernetes/issues/134038)), so the underlying issue could potentially be fixed upstream. The timeline of an upstream change is long, but if the feature is deemed not time-critical, it could be fixed at the source instead of in Kueue.
* This feature introduces a potential footgun to Kueue users and could turn out to be to volatile/risky to use in most affected cases.
* It introduces yet another responsibility for Kueue - on top of quota management and scheduling, it will also start performing failure recovery.

## Alternatives

### Do Nothing

Since this feature spawned a lengthy discussion about its riskiness and there are other controllers in the ecosystem which deal with
node problem remediation (e.g. [medik8s](https://github.com/medik8s/self-node-remediation)), an alternative would be to do nothing,
wait for the upstream conversations to be resolved and propose an alternative solution (external to Kueue) to the affected users.

The biggest benefit of this approach is that it requires no implementation effort.

**Reasons for discarding/deferring**

1. Forces users to run another system alongside Kueue, making deploying Kueue more complex those cases.

### Control All Pods Managed By Kueue

Instead of limiting the affected pods with the new annotation, the controlled could cover all pods managed by Kueue.

**Reasons for discarding/deferring**

1. It will make the feature harder to test for the early adopters, since they would have to commit all the pods to test
an alpha feature.

### Introduce a `FailureRecoveryPolicy` API

The `Configuration` struct could be extended to add `FailureRecoveryPolicy`:

```go
type Configuration struct {
  // ...

  // FailureRecoveryPolicy is used to enable automatic failure recovery mechanisms.
  // +optional
  FailureRecoveryPolicy *FailureRecoveryPolicy `json:"failureRecoveryPolicy,omitempty"`
}

type FailureRecoveryPolicy struct {
  // Rules specifies the rules to be enabled for failure recovery.
  // Exactly one rule can be specified. We keep the API flexible to accommodate
  // setting more rules in the future.
  Rules []FailureRecoveryRule `json:"rules"`
}

type FailureRecoveryRule struct {
  // Exactly one of the fields below must be specified.

  // TerminatePod enables and contains configuration for the `TerminatePod` strategy.
  // This strategy recovers stuck pods by forcefully terminating them after a configured
  // grace period elapses.
  // Currently specifying the field is required. We keep the API flexible to allow
  // introducing other rule actions in the future.
  // +optional
  TerminatePod *TerminatePodConfig `json:"terminatePod,omitempty"`
}

type TerminatePodConfig struct {
  // PodLabelSelector specifies the scope of resources covered by `TerminatePod` failure recovery -
  // resources not matching the selector are ignored by the controller.
  PodLabelSelector metav1.LabelSelector `json:"podLabelSelector"`

  // ForcefulTerminationGracePeriod is the duration between when the pod's `deletionGracePeriodSeconds`
  // elapses and when the pod should be forcefully terminated.
  // Represented using metav1.Duration (e.g. "10m", "1h30m").
  ForcefulTerminationGracePeriod metav1.Duration `json:"forcefulTerminationGracePeriod"`
}
```

This API requires opt-in both from the administrators and user's end, as the workloads would
have to be labeled in order for them to be covered with failure recovery.

An example failure recovery configuration:

```yaml
failureRecoveryPolicy:
  rules:
  - terminatePod:
      podLabelSelector:
        matchExpressions:
        - key: example.com/pod-safe-to-fail
          operator: In
          values: [ "true" ]
      forcefulTerminationGracePeriod: 5m
```

With the new API, the proposal is to **not** set a default value for the forceful termination grace period.
This puts configuration over convention, as it will make the user consciously think about the
grace period the need for their specific use case, without risking a faulty default.

Since enabling the feature is inherently risky, this serves as an additional explicit
acknowledgement of this risk from the user's end.

Alternatively, a default value can be inferred from the `terminationGracePeriodSeconds`.
For example, it could be a multiple of that value. Nevertheless, this still runs the risk of
setting a very small value or even 0, depending on the user's configuration.

**Reasons for discarding/deferring**

1. `FailureRecoveryPolicy` would have to be added to the **beta** `Configuration` API.
To avoid committing to a specific structure (and the feature in general), it is more prudent to gather initial feedback
with a feature gate first, then decide how to best represent it in the API.
1. As mentioned in the [Do Nothing](#do-nothing) alternative, the issue this KEP is trying to mitigate might be solved at the core Kubernetes level. If so, deprecating an annotation (proposed in the KEP) is simpler than deprecating an API.

### Leveraging Core Kubernetes Non-Graceful Node Shutdown Handling

Core Kubernetes already contains logic for [non-graceful node shutdown handling](https://kubernetes.io/docs/concepts/cluster-administration/node-shutdown/#non-graceful-node-shutdown) and [automatic garbage collection](https://github.com/kubernetes/kubernetes/blob/4870d987d0a4aac2d9223d4c0b9f22858c0d1590/pkg/controller/podgc/gc_controller.go) of `Pod`s running on such nodes (tainted with the `node.kubernetes.io/out-of-service` taint).
It updates the `Pod`s status and deletes it from `etcd`. The recovery controller could use this fact to terminate stuck pods by automatically adding this taint to nodes which are unreachable for some configurable time.

Given the API proposal, this approach could potentially be implemented as an alternative
 recovery strategy in the future (see [Introduce a <code>FailureRecoveryPolicy</code> API](#introduce-a-failurerecoverypolicy-api)).

 ```go
 type FailureRecoveryRule struct {
  // ...

  // TaintNodeOutOfServiceConfig enables and contains configuration for the `TaintNodeOutOfService` strategy.
  // This strategy recovers stuck pods by tainting an unreachable node with the `out-of-service` taint, allowing
  // the pod garbage-collector to terminate the pods scheduled on that node.
  // +optional
  TaintNodeOutOfServiceConfig *TaintNodeOutOfServiceConfig `json:"taintNodeOutOfServiceConfig,omitempty"`
}

type TaintNodeOutOfServiceConfig struct {
  UnreachableNodeTimeout *time.Duration `json:"unreachableNodeTimeout"`
}
 ```

Compared to the final proposal, this approach has the following benefits:

1. It uses an existing Kubernetes mechanism.
2. Concerns are tidily separated - the recovery controller simply says the node is out of service, the garbage collector in the control plane can decide how and when to clean it up.


**Reasons for discarding/deferring**

1. The effect would be node-wide pod deletion, regardless of whether they were managed by Kueue or not, breaking any guarantees of isolation.
