# MultiKueue `CapacityProvider` Parameters (KEP-9988 Alpha2)

- **Status**: Initial review
- **Author**: JanKaczmarski
- **Date**: 2026-10-12

## Summary

KEP-9988 Alpha1 sums worker `ClusterQueue` quotas and writes them into the manager `ClusterQueue.spec.resourceGroups`. In Alpha2, a built-in KEP-12382 `CapacityProvider` publishes this sum, and a `DynamicQuotaOrchestrator` (DQO) distributes it into the manager `ClusterQueue.status.effectiveQuotas`.

The open point is what `CapacityProvider.spec.parameters` references. I propose a new cluster-scoped `MultiKueueCapacityProviderConfig` CRD (`v1alpha1`) with two required fields: `multiKueueConfigName` and `managerQuotaScope`.

Section 1 shows the full setup, Section 2 compares the options, and Section 3 has the API. After approval, I will update the KEP-9988 README with watches, validation, and feature gates.

## 1. Setup

### 1.1 Example

```yaml
# 1. CapacityProvider
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
    # Sum of the related worker ClusterQueues (see the setup below):
    # worker1 cq1 (6 CPU, 24Gi) + worker1 cq2 (7 CPU, 28Gi)
    # + worker2 worker-cq (8 CPU, 32Gi, nvidia.com/gpu: 5 in gpu-a4 + 7 in gpu-h100)
    - name: multikueue-worker-flavor
      resources:
        cpu: "21"
        memory: 84Gi
        nvidia.com/gpu: "12"
---
# 2. MultiKueueCapacityProviderConfig (proposed in Section 3)
apiVersion: kueue.x-k8s.io/v1alpha1
kind: MultiKueueCapacityProviderConfig
metadata:
  name: multikueue-cp-config
spec:
  multiKueueConfigName: multikueue-config
  managerQuotaScope:
    kind: ClusterQueue
    name: cq
---
# 3. DQO -> cq
apiVersion: kueue.x-k8s.io/v1alpha1
kind: DynamicQuotaOrchestrator
metadata:
  name: multikueue-dqo
spec:
  capacityDiscovery:
    providers:
    - name: multikueue-worker-capacity
  capacityDistribution:
    subtreeRootQuotaRef:
      kind: ClusterQueue
      name: cq
---
# 4. Manager ClusterQueue. spec nominalQuota values are DQO weights;
#    the result lands in status.effectiveQuotas.
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: cq
spec:
  admissionChecksStrategy:
    admissionChecks:
    - name: multikueue-ac
  resourceGroups:
  - coveredResources: ["cpu", "memory", "nvidia.com/gpu"]
    flavors:
    - name: multikueue-worker-flavor
      resources:
      - name: cpu
        nominalQuota: "1"
      - name: memory
        nominalQuota: "1"
      - name: nvidia.com/gpu
        nominalQuota: "1"
status:
  effectiveQuotas:
    orchestratorRef:
      apiGroup: kueue.x-k8s.io
      kind: DynamicQuotaOrchestrator
      name: multikueue-dqo
    resourceGroups:
    - coveredResources: ["cpu", "memory", "nvidia.com/gpu"]
      flavors:
      - name: multikueue-worker-flavor
        resources:
        - name: cpu
          nominalQuota: "21"
        - name: memory
          nominalQuota: 84Gi
        - name: nvidia.com/gpu
          nominalQuota: "12"
```

<details>
<summary>MultiKueue, LocalQueue, and worker setup</summary>

```yaml
# Manager ResourceFlavor. No nodeLabels: the manager does not run the workloads.
apiVersion: kueue.x-k8s.io/v1beta2
kind: ResourceFlavor
metadata:
  name: multikueue-worker-flavor
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: MultiKueueConfig
metadata:
  name: multikueue-config
spec:
  clusters:
  - worker1
  - worker2
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: AdmissionCheck
metadata:
  name: multikueue-ac
spec:
  controllerName: kueue.x-k8s.io/multikueue
  parameters:
    apiGroup: kueue.x-k8s.io
    kind: MultiKueueConfig
    name: multikueue-config
---
# Manager LocalQueues -> cq
apiVersion: kueue.x-k8s.io/v1beta2
kind: LocalQueue
metadata:
  namespace: ns1
  name: lq1
spec:
  clusterQueue: cq
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: LocalQueue
metadata:
  namespace: ns2
  name: lq2
spec:
  clusterQueue: cq
---
# worker1: ns1/lq1 -> cq1, ns2/lq2 -> cq2
# worker2: ns1/lq1 and ns2/lq2 -> worker-cq
```

</details>

### 1.2 `CapacityProvider` Contract

Both options use the KEP-12382 `CapacityProvider` API unchanged.

- `spec.controllerName`: `kueue.x-k8s.io/multikueue`.
- `spec.orchestratedFlavors`: exactly one flavor. The controller sums all resources of all worker flavors into it. The name does not need to match a worker flavor.
- `spec.parameters`: required by this provider (optional in the core API). Only the MultiKueue controller reads it; the DQO does not.

The controller reports the `CapacitySynchronized` condition:

| Status | Reason | Description |
|---|---|---|
| `True` | `Synchronized` | At least one worker cluster is connected. `status.capacity.flavors[0]` holds the sum of the connected workers. The message lists the disconnected workers. |
| `False` | `Misconfigured` | `spec.parameters` is missing or has the wrong kind, a referenced object is missing, or `len(spec.orchestratedFlavors) != 1`. |
| `False` | `SourceUnavailable` | No worker cluster is connected. |

If any provider of a DQO is not `True`, the DQO stops distributing (`EffectiveCapacityComputed=False`, `Distributed=False`). The manager `ClusterQueue.status.effectiveQuotas` keeps its last value ([`discovery.go:57-66`](https://github.com/kubernetes-sigs/kueue/blob/436fa2bf7c39adb2e816cfbb247f904a6dbee582/pkg/controller/core/dqo/discovery.go#L57-L66), [`reconciler.go:226-233`](https://github.com/kubernetes-sigs/kueue/blob/436fa2bf7c39adb2e816cfbb247f904a6dbee582/pkg/controller/core/dqo/reconciler.go#L226-L233)).

## 2. Options

### 2.1 Option A: Reference `MultiKueueConfig`

Pros:
- No new CRD. It reuses the object that the MultiKueue `AdmissionCheck` already references.

Cons:
- Over-counting. Many `AdmissionCheck`s and manager `ClusterQueue`s can share one `MultiKueueConfig`. Suppose `cq-team-a` (10 CPU on the worker) and `cq-team-b` (30 CPU on the worker) share `multikueue-config`. A provider that points to `multikueue-config` sums both teams (40 CPU). If the DQO targets only `cq-team-a`, it gets 40 CPU instead of 10.
- Any future provider setting would have to go into `MultiKueueConfig` (a `v1beta2` dispatch API), or we would later have to change `spec.parameters.kind`, which is a breaking change.

### 2.2 Option B: Reference a new `MultiKueueCapacityProviderConfig` CRD

Pros:
- Matches KEP-12382: a provider may use its own configuration CRD through `spec.parameters` ([KEP-12382 L520-522](https://github.com/kubernetes-sigs/kueue/blob/436fa2bf7c39adb2e816cfbb247f904a6dbee582/keps/12382-dynamic-quota-orchestration/README.md#L520-L522)). Same pattern as `ProvisioningRequestConfig` and `MultiKueueConfig`.
- `managerQuotaScope` limits the sum to the manager `ClusterQueue` or `Cohort` subtree, even when other queues share the same `MultiKueueConfig`.
- Quota settings stay out of the `v1beta2` dispatch API.

Cons:
- One more small CRD and one more object to create.
- Users repeat the DQO target in `managerQuotaScope`. The provider cannot derive it: it does not know which DQOs reference it, and several DQOs can reference one provider.

## 3. Proposal

Option B. It fixes the over-counting of Option A and keeps quota settings out of the `v1beta2` API. The cost is one small `v1alpha1` CRD.

**Usage rule**: normally one MultiKueue `CapacityProvider` per DQO target, with `managerQuotaScope` equal to the DQO `subtreeRootQuotaRef` (a `ClusterQueue` or a `Cohort`). This is not validated; a mismatch silently over- or under-counts. If a DQO lists several MultiKueue providers (for example, for different `MultiKueueConfig`s in the same scope), their worker `ClusterQueue`s must not overlap, because the DQO adds them up.

### 3.1 Go API

`managerQuotaScope` reuses `CapacityDistributionSubtreeRootRef` from the DQO API ([`dynamicquotaorchestrator_types.go:79-93`](https://github.com/kubernetes-sigs/kueue/blob/436fa2bf7c39adb2e816cfbb247f904a6dbee582/apis/kueue/v1alpha1/dynamicquotaorchestrator_types.go#L79-L93)). See object 2 in Section 1.1 for a YAML example.

<details>
<summary>Go types</summary>

```go
// MultiKueueCapacityProviderConfig holds the settings of the built-in
// MultiKueue CapacityProvider.
//
// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:scope=Cluster
type MultiKueueCapacityProviderConfig struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is the standard object metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec is the specification of the MultiKueueCapacityProviderConfig.
	// +required
	Spec MultiKueueCapacityProviderConfigSpec `json:"spec,omitzero"`
}

type MultiKueueCapacityProviderConfigSpec struct {
	// multiKueueConfigName is the name of the MultiKueueConfig that defines
	// the worker clusters from which capacity is aggregated.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	MultiKueueConfigName string `json:"multiKueueConfigName,omitempty"`

	// managerQuotaScope bounds which manager ClusterQueues contribute
	// LocalQueues when discovering related worker ClusterQueues.
	// Only manager ClusterQueues in this scope that also reference a
	// MultiKueue AdmissionCheck backed by multiKueueConfigName are included.
	// +required
	ManagerQuotaScope CapacityDistributionSubtreeRootRef `json:"managerQuotaScope,omitzero"`
}

// +kubebuilder:object:root=true

// MultiKueueCapacityProviderConfigList contains a list of MultiKueueCapacityProviderConfig.
type MultiKueueCapacityProviderConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MultiKueueCapacityProviderConfig `json:"items"`
}
```

</details>

## 4. Migration Plan

TBD.

## 5. Consequences

- Discovery reuses the Alpha1 path ([`clusterqueue.go:186-245`](https://github.com/kubernetes-sigs/kueue/blob/436fa2bf7c39adb2e816cfbb247f904a6dbee582/pkg/controller/admissionchecks/multikueue/clusterqueue.go#L186-L245)). The controller now reconciles the `CapacityProvider` and writes `status.capacity` instead of `ClusterQueue.spec`. New: `Cohort` scope, each worker `ClusterQueue` counted once, and worker `status.effectiveQuotas` preferred over `spec.resourceGroups`.
- `MultiKueueConfig.spec.quotaManagement` is deprecated and ignored. It stays because it is a `v1beta2` field. The `MultiKueueManagerQuotaAutomation` condition is no longer written.
- One manager flavor, as in Alpha1. This is a KEP-9988 choice; the DQO itself works per `(flavor, resource)` ([`discovery.go:90-122`](https://github.com/kubernetes-sigs/kueue/blob/436fa2bf7c39adb2e816cfbb247f904a6dbee582/pkg/controller/core/dqo/discovery.go#L90-L122)). The manager does not send its flavor assignment to workers, so per-flavor totals may differ from what workers admit. A later optional field in `MultiKueueCapacityProviderConfig` can add a multi-flavor mode without a breaking change.
- Overbooking and headroom stay in the DQO. The provider reports the real worker total; users set `effectiveCapacityMultiplier` on the provider entry in the DQO `spec.capacityDiscovery.providers` (for example, `2` or `0.9`).
