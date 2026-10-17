# Alpha2 for MultiKueue Dynamic Quota

## Introduction

This document describes the “Alpha2” stage of [KEP-9988](https://github.com/kubernetes-sigs/kueue/tree/main/keps/9988-multikueue-manager-quota-automation) (MultiKueue Manager Quota Automation). Alpha2 uses the Dynamic Quota Orchestrator (DQO) from [KEP-12382](https://github.com/kubernetes-sigs/kueue/blob/main/keps/12382-dynamic-quota-orchestration/README.md). DQO reads the capacity from a list of CapacityProviders (providers). Then DQO distributes this capacity as a quota over a `ClusterQueue` or a `Cohort` tree.

In Alpha1, the MultiKueue controller writes the sum of the related worker quotas into the manager `ClusterQueue.spec`. In Alpha2, the controller writes this sum into `CapacityProvider.status.capacity`. Then DQO writes the quota into `ClusterQueue.status.effectiveQuotas`. The controller no longer changes `ClusterQueue.spec`.

### Components

Alpha2 uses two Kubernetes objects:

#### MultiKueue CapacityProvider

The YAML below shows the object. [The DQO KEP](https://github.com/kubernetes-sigs/kueue/blob/main/keps/12382-dynamic-quota-orchestration/README.md#capacityprovider) describes its fields. The main difference from a standard provider is `spec.parameters`. This field refers to the configuration of the provider, `MultiKueueCapacityProviderConfig` (below). The configuration has one field, `managerQuotaScope`. This field selects the manager ClusterQueues that the provider covers.

```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: CapacityProvider
metadata:
  name: multikueue-worker-capacity
spec:
  controllerName: kueue.x-k8s.io/multikueue
  orchestratedFlavors:
  - name: multikueue-worker-flavor
  parameters:
    apiGroup: kueue.x-k8s.io
    kind: MultiKueueCapacityProviderConfig
    name: multikueue-cp-config
status:
  capacity:
    flavors:
    # Sum of the related worker ClusterQueues
    - name: multikueue-worker-flavor
      resources:
        cpu: "21"
        memory: 84Gi
        nvidia.com/gpu: "12"
```

#### MultiKueueCapacityProviderConfig

```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: MultiKueueCapacityProviderConfig
metadata:
  name: multikueue-cp-config
spec:
  managerQuotaScope:
    kind: ClusterQueue   # or Cohort
    name: cq
```

### How the controller uses managerQuotaScope

`managerQuotaScope` is a single `ClusterQueue` or a single `Cohort` (with its full subtree). For each manager ClusterQueue in this scope, the controller finds the related worker ClusterQueues in these steps:

1. The controller gets the MultiKueue AdmissionCheck of the ClusterQueue. If the ClusterQueue has no MultiKueue AdmissionCheck, the controller skips it.
1. It gets the `MultiKueueConfig` of this AdmissionCheck. The `MultiKueueConfig` lists the worker clusters.
1. It gets the LocalQueues of the manager ClusterQueue.
1. On each connected worker cluster from step 2, it finds the LocalQueues with the same namespace and name.
1. It gets the ClusterQueues of these worker LocalQueues.

The controller sums the quotas of these worker ClusterQueues.

- **ClusterQueue** scope: the provider covers one ClusterQueue, as in Alpha1.
- **Cohort** scope: the provider covers all ClusterQueues in the Cohort. DQO divides the total between them.

### Problems

- `managerQuotaScope` must match the DQO `spec.capacityDistribution.subtreeRootQuotaRef`. If the two are different, DQO distributes the sum for `managerQuotaScope` to `subtreeRootQuotaRef`. The API cannot reject this mismatch. DQO does not read `spec.parameters`, and a CEL (Common Expression Language) rule cannot compare two objects.
- To solve the above the following might be considered: the MultiKueue controller finds the DQOs that refer to the provider. If a DQO has a different scope, the controller reports the mismatch. It uses a Warning event or a separate condition on the provider (to be decided).

### Details

#### CapacitySynchronized condition

| **Status** | **Reason** | **Description** |
| --- | --- | --- |
| True | Synchronized | At least one worker cluster of the in-scope ClusterQueues is connected. `status.capacity.flavors[0]` holds the sum of the connected worker clusters. The condition message lists the disconnected worker clusters. |
| False | Misconfigured | One of these conditions occurs:<br>• `spec.parameters` is missing or has the wrong kind.<br>• The MultiKueueCapacityProviderConfig does not exist.<br>• The MultiKueueConfig of an in-scope ClusterQueue does not exist.<br>• An in-scope ClusterQueue has more than one MultiKueue AdmissionCheck, or a per-flavor one.<br>• No ClusterQueue in scope uses MultiKueue.<br>• `len(spec.orchestratedFlavors) != 1`. |
| False | SourceUnavailable | No worker cluster of the in-scope ClusterQueues is connected. |

If a provider in a DQO does not have `CapacitySynchronized=True`, DQO skips the distribution. Then the manager `ClusterQueue.status.effectiveQuotas` keeps its last value. This is the DQO behavior from [KEP-12382](https://github.com/kubernetes-sigs/kueue/blob/main/keps/12382-dynamic-quota-orchestration/README.md) (see the [`Distributed` condition](https://github.com/kubernetes-sigs/kueue/blob/main/keps/12382-dynamic-quota-orchestration/README.md#condition-distributed-phase-2-quota-distribution) and [Stale Effective Quota](https://github.com/kubernetes-sigs/kueue/blob/main/keps/12382-dynamic-quota-orchestration/README.md#stale-effective-quota)). This proposal does not change it.

#### orchestratedFlavors

`spec.orchestratedFlavors` must contain exactly one flavor. The name does not need to match a worker flavor.

As in Alpha1, the controller sums all worker flavors into one manager flavor, because separate flavor totals would be wrong. The manager picks a flavor when it admits a workload. The worker does not use this choice and picks its own flavor. So a total for one manager flavor does not show where the workloads run.

#### Go types

```go
// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:scope=Cluster
type MultiKueueCapacityProviderConfig struct {
    metav1.TypeMeta `json:",inline"`
    // +optional
    metav1.ObjectMeta `json:"metadata,omitempty"`
    // +required
    Spec MultiKueueCapacityProviderConfigSpec `json:"spec,omitzero"`
}
type MultiKueueCapacityProviderConfigSpec struct {
    // +required
    ManagerQuotaScope CapacityDistributionSubtreeRootRef `json:"managerQuotaScope,omitzero"`
}
```

#### Alternatives

- `spec.parameters` refers to a MultiKueueConfig. A MultiKueueConfig lists worker clusters, not manager ClusterQueues. This causes two problems:
    - Many manager ClusterQueues can use the same MultiKueueConfig. The provider sums the worker quotas of all of them. A DQO that targets only one of them gets too much quota.
    - A Cohort can contain ClusterQueues with different MultiKueueConfigs. Each config then needs its own provider. If two configs list the same worker cluster, DQO counts its quota twice.
- `spec.parameters` refers to the ClusterQueue or Cohort directly. This gives the same scope as `managerQuotaScope`, but it has two problems:
    - There is no provider configuration object, so there is no place for future settings (for example, a multi-flavor mode). [KEP-12382](https://github.com/kubernetes-sigs/kueue/blob/main/keps/12382-dynamic-quota-orchestration/README.md#capacityprovider) intends `spec.parameters` to refer to a provider configuration CRD.
    - `spec.parameters.name` allows a maximum of 63 characters and no dots. ClusterQueue and Cohort names allow 253 characters with dots.

#### Double counting compared to Alpha1

Two manager ClusterQueues can send workloads to the same worker ClusterQueue. For example, `team-a` and `team-b` both use `worker-cq`, which has 100 CPU.

Alpha1 gives each manager ClusterQueue the full 100 CPU, so the manager has 200 CPU in total. KEP-9988 accepts this because each manager quota is only an upper limit.

In Alpha2, the result depends on the scope:

- **ClusterQueue scope:** Each manager ClusterQueue has its own provider. Each provider reports 100 CPU, which is the same as Alpha1.
- **Cohort scope:** One provider covers both manager ClusterQueues. It counts `worker-cq` once and reports 100 CPU. The DQO then splits the 100 CPU between `team-a` and `team-b`.

A Cohort scope removes duplicates only inside the Cohort. Suppose a manager ClusterQueue outside the Cohort also uses `worker-cq`. Its provider counts `worker-cq` again.

Use one provider for the full DQO target. The DQO adds up the capacity of all its providers. It cannot find duplicates, because each provider reports only totals.

#### Migration Plan

TBD.
