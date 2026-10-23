---
title: Kueue v1alpha1 API
content_type: tool-reference
package: kueue.x-k8s.io/v1alpha1
auto_generated: true
description: Generated API reference documentation for kueue.x-k8s.io/v1alpha1.
---


## Resource Types 


- [CapacityProvider](#kueue-x-k8s-io-v1alpha1-CapacityProvider)
- [DynamicQuotaOrchestrator](#kueue-x-k8s-io-v1alpha1-DynamicQuotaOrchestrator)
- [PreemptionConfig](#kueue-x-k8s-io-v1alpha1-PreemptionConfig)
  

## `CapacityProvider`     {#kueue-x-k8s-io-v1alpha1-CapacityProvider}
    

**Appears in:**



<p>CapacityProvider reports capacity for ResourceFlavors.
DQO uses this capacity only when CapacitySynchronized=True.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
<tr><td><code>apiVersion</code><br/>string</td><td><code>kueue.x-k8s.io/v1alpha1</code></td></tr>
<tr><td><code>kind</code><br/>string</td><td><code>CapacityProvider</code></td></tr>
    
  
<tr><td><code>spec</code><br/>
<a href="#kueue-x-k8s-io-v1alpha1-CapacityProviderSpec"><code>CapacityProviderSpec</code></a>
</td>
<td>
   <p>spec defines the desired state of the CapacityProvider.</p>
</td>
</tr>
<tr><td><code>status</code><br/>
<a href="#kueue-x-k8s-io-v1alpha1-CapacityProviderStatus"><code>CapacityProviderStatus</code></a>
</td>
<td>
   <p>status defines the capacity observed and published by the provider.</p>
</td>
</tr>
</tbody>
</table>

## `DynamicQuotaOrchestrator`     {#kueue-x-k8s-io-v1alpha1-DynamicQuotaOrchestrator}
    

**Appears in:**



<p>DynamicQuotaOrchestrator is the Schema for the dynamicquotaorchestrators API</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
<tr><td><code>apiVersion</code><br/>string</td><td><code>kueue.x-k8s.io/v1alpha1</code></td></tr>
<tr><td><code>kind</code><br/>string</td><td><code>DynamicQuotaOrchestrator</code></td></tr>
    
  
<tr><td><code>spec</code><br/>
<a href="#kueue-x-k8s-io-v1alpha1-DynamicQuotaOrchestratorSpec"><code>DynamicQuotaOrchestratorSpec</code></a>
</td>
<td>
   <p>spec defines the desired state of the DynamicQuotaOrchestrator.</p>
</td>
</tr>
<tr><td><code>status</code><br/>
<a href="#kueue-x-k8s-io-v1alpha1-DynamicQuotaOrchestratorStatus"><code>DynamicQuotaOrchestratorStatus</code></a>
</td>
<td>
   <p>status defines the observed state of the DynamicQuotaOrchestrator.</p>
</td>
</tr>
</tbody>
</table>

## `PreemptionConfig`     {#kueue-x-k8s-io-v1alpha1-PreemptionConfig}
    

**Appears in:**



<p>PreemptionConfig is the Schema for the preemptionconfigs API</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
<tr><td><code>apiVersion</code><br/>string</td><td><code>kueue.x-k8s.io/v1alpha1</code></td></tr>
<tr><td><code>kind</code><br/>string</td><td><code>PreemptionConfig</code></td></tr>
    
  
<tr><td><code>spec</code><br/>
<a href="#kueue-x-k8s-io-v1alpha1-PreemptionConfigSpec"><code>PreemptionConfigSpec</code></a>
</td>
<td>
   <p>spec defines the preemption rules of the PreemptionConfig.</p>
</td>
</tr>
</tbody>
</table>

## `CapacityDiscovery`     {#kueue-x-k8s-io-v1alpha1-CapacityDiscovery}
    

**Appears in:**

- [DynamicQuotaOrchestratorSpec](#kueue-x-k8s-io-v1alpha1-DynamicQuotaOrchestratorSpec)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>providers</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1alpha1-CapacityDiscoveryProviderContribution"><code>[]CapacityDiscoveryProviderContribution</code></a>
</td>
<td>
   <p>providers lists CapacityProvider objects consumed by this DQO.</p>
</td>
</tr>
</tbody>
</table>

## `CapacityDiscoveryProviderContribution`     {#kueue-x-k8s-io-v1alpha1-CapacityDiscoveryProviderContribution}
    

**Appears in:**

- [CapacityDiscovery](#kueue-x-k8s-io-v1alpha1-CapacityDiscovery)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1alpha1-CapacityProviderName"><code>CapacityProviderName</code></a>
</td>
<td>
   <p>name identifies a CapacityProvider.</p>
</td>
</tr>
<tr><td><code>effectiveCapacityMultiplier</code><br/>
<a href="https://pkg.go.dev/k8s.io/apimachinery/pkg/api/resource#Quantity"><code>k8s.io/apimachinery/pkg/api/resource.Quantity</code></a>
</td>
<td>
   <p>effectiveCapacityMultiplier specifies the multiplier applied to the
discovered capacity from this provider. It defaults to 1.</p>
</td>
</tr>
</tbody>
</table>

## `CapacityDistribution`     {#kueue-x-k8s-io-v1alpha1-CapacityDistribution}
    

**Appears in:**

- [DynamicQuotaOrchestratorSpec](#kueue-x-k8s-io-v1alpha1-DynamicQuotaOrchestratorSpec)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>subtreeRootQuotaRef,omitzero</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1alpha1-CapacityDistributionSubtreeRootRef"><code>CapacityDistributionSubtreeRootRef</code></a>
</td>
<td>
   <p>subtreeRootQuotaRef identifies the root of the quota subtree.</p>
</td>
</tr>
</tbody>
</table>

## `CapacityDistributionSubtreeRootRef`     {#kueue-x-k8s-io-v1alpha1-CapacityDistributionSubtreeRootRef}
    

**Appears in:**

- [CapacityDistribution](#kueue-x-k8s-io-v1alpha1-CapacityDistribution)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>kind</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1alpha1-SubtreeRootRefKind"><code>SubtreeRootRefKind</code></a>
</td>
<td>
   <p>kind indicates the kind of the quota node, i.e. ClusterQueue or Cohort.</p>
</td>
</tr>
<tr><td><code>name</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>name indicates the name of the quota node, i.e. ClusterQueue or Cohort.</p>
</td>
</tr>
</tbody>
</table>

## `CapacityProviderControllerName`     {#kueue-x-k8s-io-v1alpha1-CapacityProviderControllerName}
    
(Alias of `string`)

**Appears in:**

- [CapacityProviderSpec](#kueue-x-k8s-io-v1alpha1-CapacityProviderSpec)





## `CapacityProviderName`     {#kueue-x-k8s-io-v1alpha1-CapacityProviderName}
    
(Alias of `string`)

**Appears in:**

- [CapacityDiscoveryProviderContribution](#kueue-x-k8s-io-v1alpha1-CapacityDiscoveryProviderContribution)





## `CapacityProviderNormalizedCapacity`     {#kueue-x-k8s-io-v1alpha1-CapacityProviderNormalizedCapacity}
    

**Appears in:**

- [CapacityProviderStatus](#kueue-x-k8s-io-v1alpha1-CapacityProviderStatus)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>flavors</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1alpha1-CapacityProviderNormalizedCapacityFlavor"><code>[]CapacityProviderNormalizedCapacityFlavor</code></a>
</td>
<td>
   <p>flavors reports capacity for each flavor and resource.
The list must contain at least one flavor.
It should contain the same flavors as spec.orchestratedFlavors.
An empty resources map means zero capacity for all resources of the flavor.</p>
</td>
</tr>
</tbody>
</table>

## `CapacityProviderNormalizedCapacityFlavor`     {#kueue-x-k8s-io-v1alpha1-CapacityProviderNormalizedCapacityFlavor}
    

**Appears in:**

- [CapacityProviderNormalizedCapacity](#kueue-x-k8s-io-v1alpha1-CapacityProviderNormalizedCapacity)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1alpha1-ResourceFlavorReference"><code>ResourceFlavorReference</code></a>
</td>
<td>
   <p>name identifies the ResourceFlavor whose capacity is reported.</p>
</td>
</tr>
<tr><td><code>resources</code> <B>[Required]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#resourcelist-v1-core"><code>k8s.io/api/core/v1.ResourceList</code></a>
</td>
<td>
   <p>resources contains total capacity by resource name.
The provider orchestrates all resources of the flavor: a resource that is
not listed, including when the map is empty, has zero capacity.</p>
</td>
</tr>
</tbody>
</table>

## `CapacityProviderOrchestratedFlavor`     {#kueue-x-k8s-io-v1alpha1-CapacityProviderOrchestratedFlavor}
    

**Appears in:**

- [CapacityProviderSpec](#kueue-x-k8s-io-v1alpha1-CapacityProviderSpec)


<p>CapacityProviderOrchestratedFlavor identifies a flavor managed by a provider.
The container allows the mapping to be extended in a future API version.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1alpha1-ResourceFlavorReference"><code>ResourceFlavorReference</code></a>
</td>
<td>
   <p>name identifies the ResourceFlavor managed by this provider.</p>
</td>
</tr>
</tbody>
</table>

## `CapacityProviderParametersReference`     {#kueue-x-k8s-io-v1alpha1-CapacityProviderParametersReference}
    

**Appears in:**

- [CapacityProviderSpec](#kueue-x-k8s-io-v1alpha1-CapacityProviderSpec)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>apiGroup</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>apiGroup is the group for the resource being referenced.</p>
</td>
</tr>
<tr><td><code>kind</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>kind is the type of the resource being referenced.</p>
</td>
</tr>
<tr><td><code>name</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>name is the name of the resource being referenced.</p>
</td>
</tr>
</tbody>
</table>

## `CapacityProviderSpec`     {#kueue-x-k8s-io-v1alpha1-CapacityProviderSpec}
    

**Appears in:**

- [CapacityProvider](#kueue-x-k8s-io-v1alpha1-CapacityProvider)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>orchestratedFlavors</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1alpha1-CapacityProviderOrchestratedFlavor"><code>[]CapacityProviderOrchestratedFlavor</code></a>
</td>
<td>
   <p>orchestratedFlavors identifies the ResourceFlavors for which this provider may
publish capacity. DQO ignores entries in status.capacity.flavors whose
names are not listed here. The provider orchestrates all resources of the
listed flavors: capacity that is not reported for a listed flavor is zero.</p>
</td>
</tr>
<tr><td><code>controllerName</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1alpha1-CapacityProviderControllerName"><code>CapacityProviderControllerName</code></a>
</td>
<td>
   <p>controllerName identifies the controller publishing capacity.
This field is immutable.</p>
</td>
</tr>
<tr><td><code>parameters</code><br/>
<a href="#kueue-x-k8s-io-v1alpha1-CapacityProviderParametersReference"><code>CapacityProviderParametersReference</code></a>
</td>
<td>
   <p>parameters optionally references implementation-specific configuration.
DQO does not read or validate the referenced object.</p>
</td>
</tr>
</tbody>
</table>

## `CapacityProviderStatus`     {#kueue-x-k8s-io-v1alpha1-CapacityProviderStatus}
    

**Appears in:**

- [CapacityProvider](#kueue-x-k8s-io-v1alpha1-CapacityProvider)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>conditions</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#condition-v1-meta"><code>[]k8s.io/apimachinery/pkg/apis/meta/v1.Condition</code></a>
</td>
<td>
   <p>conditions represents the current state of this provider.</p>
</td>
</tr>
<tr><td><code>capacity</code><br/>
<a href="#kueue-x-k8s-io-v1alpha1-CapacityProviderNormalizedCapacity"><code>CapacityProviderNormalizedCapacity</code></a>
</td>
<td>
   <p>capacity is the normalized capacity published by the provider.</p>
</td>
</tr>
</tbody>
</table>

## `DynamicQuotaOrchestratorSpec`     {#kueue-x-k8s-io-v1alpha1-DynamicQuotaOrchestratorSpec}
    

**Appears in:**

- [DynamicQuotaOrchestrator](#kueue-x-k8s-io-v1alpha1-DynamicQuotaOrchestrator)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>capacityDiscovery,omitzero</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1alpha1-CapacityDiscovery"><code>CapacityDiscovery</code></a>
</td>
<td>
   <p>capacityDiscovery specifies capacity aggregation.</p>
</td>
</tr>
<tr><td><code>capacityDistribution</code><br/>
<a href="#kueue-x-k8s-io-v1alpha1-CapacityDistribution"><code>CapacityDistribution</code></a>
</td>
<td>
   <p>capacityDistribution specifies how aggregated capacity is distributed.
When omitted, the DQO is discovery-only: it reports aggregated capacity
but does not write effectiveQuotas status.</p>
</td>
</tr>
</tbody>
</table>

## `DynamicQuotaOrchestratorStatus`     {#kueue-x-k8s-io-v1alpha1-DynamicQuotaOrchestratorStatus}
    

**Appears in:**

- [DynamicQuotaOrchestrator](#kueue-x-k8s-io-v1alpha1-DynamicQuotaOrchestrator)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>conditions</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#condition-v1-meta"><code>[]k8s.io/apimachinery/pkg/apis/meta/v1.Condition</code></a>
</td>
<td>
   <p>conditions represents the current state of the DQO.</p>
</td>
</tr>
<tr><td><code>effectiveCapacity</code><br/>
<a href="#kueue-x-k8s-io-v1alpha1-EffectiveCapacity"><code>EffectiveCapacity</code></a>
</td>
<td>
   <p>effectiveCapacity is the combined capacity from the referenced providers.
Each provider's capacity is multiplied by its effectiveCapacityMultiplier.
DQO clears this field if any referenced provider is missing or does not
report CapacitySynchronized=True for its current generation.</p>
</td>
</tr>
</tbody>
</table>

## `EffectiveCapacity`     {#kueue-x-k8s-io-v1alpha1-EffectiveCapacity}
    

**Appears in:**

- [DynamicQuotaOrchestratorStatus](#kueue-x-k8s-io-v1alpha1-DynamicQuotaOrchestratorStatus)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>flavors</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1alpha1-EffectiveCapacityFlavor"><code>[]EffectiveCapacityFlavor</code></a>
</td>
<td>
   <p>flavors contains the total capacity for each flavor and resource.
It includes all flavors listed in spec.orchestratedFlavors of the
referenced providers, up to 128 distinct flavors. If the referenced
providers together orchestrate more distinct flavors, the DQO sets
EffectiveCapacityComputed=False with reason AggregationFailed and
clears status.effectiveCapacity.
An empty resources map means zero capacity for all resources of the flavor.</p>
</td>
</tr>
</tbody>
</table>

## `EffectiveCapacityFlavor`     {#kueue-x-k8s-io-v1alpha1-EffectiveCapacityFlavor}
    

**Appears in:**

- [EffectiveCapacity](#kueue-x-k8s-io-v1alpha1-EffectiveCapacity)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1alpha1-ResourceFlavorReference"><code>ResourceFlavorReference</code></a>
</td>
<td>
   <p>name identifies the ResourceFlavor whose capacity is reported.</p>
</td>
</tr>
<tr><td><code>resources</code> <B>[Required]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#resourcelist-v1-core"><code>k8s.io/api/core/v1.ResourceList</code></a>
</td>
<td>
   <p>resources contains total capacity by resource name, up to 64 distinct
resources. If the referenced providers together report more distinct
resources for the flavor, the DQO sets EffectiveCapacityComputed=False
with reason AggregationFailed and clears status.effectiveCapacity.
A resource that is not listed, including when the map is empty, has zero
capacity for the flavor.</p>
</td>
</tr>
</tbody>
</table>

## `NumericComparison`     {#kueue-x-k8s-io-v1alpha1-NumericComparison}
    
(Alias of `string`)

**Appears in:**

- [PreemptionConfigNumericLabelConstraint](#kueue-x-k8s-io-v1alpha1-PreemptionConfigNumericLabelConstraint)

- [PreemptionConfigPriorityConstraint](#kueue-x-k8s-io-v1alpha1-PreemptionConfigPriorityConstraint)


<p>NumericComparison defines how a specified numeric property (e.g., priority or custom numeric
label value) of the candidate compares to the same property of the preemptor.
Possible values are:</p>
<ul>
<li>&quot;LessThan&quot;: permits preemption if candidate field value &lt; preemptor field value</li>
<li>&quot;GreaterThan&quot;: permits preemption if candidate field value &gt; preemptor field value</li>
<li>&quot;LessThanOrEqual&quot;: permits preemption if candidate field value &lt;= preemptor field value</li>
<li>&quot;GreaterThanOrEqual&quot;: permits preemption if candidate field value &gt;= preemptor field value</li>
</ul>




## `PreemptionConfigActivationPolicy`     {#kueue-x-k8s-io-v1alpha1-PreemptionConfigActivationPolicy}
    

**Appears in:**

- [PreemptionConfigPreemptionRule](#kueue-x-k8s-io-v1alpha1-PreemptionConfigPreemptionRule)


<p>PreemptionConfigActivationPolicy defines when a preemption rule contributes candidates.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>trigger</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1alpha1-PreemptionConfigActivationTrigger"><code>PreemptionConfigActivationTrigger</code></a>
</td>
<td>
   <p>trigger specifies the prerequisite for contributing candidates.</p>
<p>Possible values are:</p>
<ul>
<li>Always: contributes matching candidates unconditionally.</li>
<li>InsufficientQuota: contributes matching candidates only if preempting baseline candidates
does not yield sufficient quota to admit the preemptor workload.</li>
<li>QuotaFeasibleAndInsufficientTopology: contributes matching candidates only if quota
is feasible for the entire preemptor under at least one eligible flavor assignment
(after preempting baseline candidates and any candidates from InsufficientQuota rules),
but the workload cannot be admitted because no eligible flavor assignment satisfies
its topology requirements.</li>
</ul>
<p>Baseline candidates are the deduplicated union of:</p>
<ul>
<li>candidates selected by the preemptor's ClusterQueue.spec.preemption policy;</li>
<li>candidates selected by applicable rules in the referenced PreemptionConfig
whose activationPolicy.trigger is Always.</li>
</ul>
</td>
</tr>
</tbody>
</table>

## `PreemptionConfigActivationTrigger`     {#kueue-x-k8s-io-v1alpha1-PreemptionConfigActivationTrigger}
    
(Alias of `string`)

**Appears in:**

- [PreemptionConfigActivationPolicy](#kueue-x-k8s-io-v1alpha1-PreemptionConfigActivationPolicy)


<p>PreemptionConfigActivationTrigger specifies when preemption rule should be treated as active.</p>




## `PreemptionConfigNumericLabelConstraint`     {#kueue-x-k8s-io-v1alpha1-PreemptionConfigNumericLabelConstraint}
    

**Appears in:**

- [PreemptionConfigPreemptionCandidateSelector](#kueue-x-k8s-io-v1alpha1-PreemptionConfigPreemptionCandidateSelector)


<p>PreemptionConfigNumericLabelConstraint describes the rule for filtering a custom numerical label.
For example, this can be used to filter candidates based on the label describing the
required topology domain size, such as the &quot;number of TPUs&quot;.
If a user has a label &quot;number-of-tpus&quot; that describes the number of TPUs required in a single cube,
it can be used to create a rule that selects only workloads requiring smaller cube slices
by defining comparison: &quot;LessThan&quot;. Such a configuration would allow preemption of &quot;smaller&quot;
workloads, to achieve better cluster utilization and decrease fragmentation.
Please note that those labels are not copied out of the box from job-like objects.
You should remember to append the designated labels to the list of labels
copied to the workload via the Kueue main configuration if you wish to use a custom label.
As Kubernetes label values cannot start with '-', integer labels are always non-negative.
A negative fallbackValue can thus ensure workloads without the label compare smaller than any
labeled workload if this is desired.
If neither comparison, minValue, nor maxValue are specified, the constraint checks only that
candidate workloads possess the designated label key with a valid integer.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>key</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>key is the label key that stores the integer value in the workload that will
be used for candidate selection.</p>
</td>
</tr>
<tr><td><code>fallbackValue</code><br/>
<code>int32</code>
</td>
<td>
   <p>fallbackValue is used when a workload does not have the label key
or the value under the key cannot be parsed as an integer.
If not specified, workloads without the label or
with a label value not parsable as int are treated as incomparable,
and therefore excluded from preemption candidates.</p>
</td>
</tr>
<tr><td><code>comparison</code><br/>
<a href="#kueue-x-k8s-io-v1alpha1-NumericComparison"><code>NumericComparison</code></a>
</td>
<td>
   <p>comparison defines how the candidate's label value compares to the preemptor's.</p>
</td>
</tr>
<tr><td><code>minValue</code><br/>
<code>int32</code>
</td>
<td>
   <p>minValue specifies the lowest label value a candidate workload can have to be
considered for preemption.
If not specified, no lower bound is enforced.</p>
</td>
</tr>
<tr><td><code>maxValue</code><br/>
<code>int32</code>
</td>
<td>
   <p>maxValue specifies the highest label value a candidate workload can have to be
considered for preemption.
If not specified, no upper bound is enforced.</p>
</td>
</tr>
</tbody>
</table>

## `PreemptionConfigPreemptionCandidateSelector`     {#kueue-x-k8s-io-v1alpha1-PreemptionConfigPreemptionCandidateSelector}
    

**Appears in:**

- [PreemptionConfigPreemptionRule](#kueue-x-k8s-io-v1alpha1-PreemptionConfigPreemptionRule)


<p>PreemptionConfigPreemptionCandidateSelector defines the selection criteria for workloads that are candidates for preemption.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>scope</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1alpha1-PreemptionConfigPreemptionQueueScope"><code>PreemptionConfigPreemptionQueueScope</code></a>
</td>
<td>
   <p>scope specifies the queue or cohort relation boundary of candidates to the preemptor workload.</p>
</td>
</tr>
<tr><td><code>clusterQueueSelector</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#labelselector-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.LabelSelector</code></a>
</td>
<td>
   <p>clusterQueueSelector defines label selector constraints on candidate ClusterQueues.
Accepts all if not set.</p>
</td>
</tr>
<tr><td><code>labelSelector</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#labelselector-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.LabelSelector</code></a>
</td>
<td>
   <p>labelSelector defines label selector constraints on candidate Workloads.
Accepts all if not set.</p>
</td>
</tr>
<tr><td><code>numericLabels</code><br/>
<a href="#kueue-x-k8s-io-v1alpha1-PreemptionConfigNumericLabelConstraint"><code>[]PreemptionConfigNumericLabelConstraint</code></a>
</td>
<td>
   <p>numericLabels defines rules for filtering candidates using custom numeric labels on the Workload resource.
Multiple numeric labels are joined using AND-rule (all have to be satisfied).
Accepts all if not set.</p>
</td>
</tr>
<tr><td><code>priority</code><br/>
<a href="#kueue-x-k8s-io-v1alpha1-PreemptionConfigPriorityConstraint"><code>PreemptionConfigPriorityConstraint</code></a>
</td>
<td>
   <p>priority defines the requirements for the priority of candidates.
Workloads not matching those requirements will not be considered as preemption candidates.
If nil, no priority requirements are enforced.</p>
</td>
</tr>
</tbody>
</table>

## `PreemptionConfigPreemptionQueueScope`     {#kueue-x-k8s-io-v1alpha1-PreemptionConfigPreemptionQueueScope}
    
(Alias of `string`)

**Appears in:**

- [PreemptionConfigPreemptionCandidateSelector](#kueue-x-k8s-io-v1alpha1-PreemptionConfigPreemptionCandidateSelector)


<p>PreemptionConfigPreemptionQueueScope specifies the relational boundary between
the preempting workload's queue and candidate workloads' queues.
Possible values are:</p>
<ul>
<li>&quot;WithinLocalQueue&quot;: restricts preemption candidates to workloads submitted to the exact same LocalQueue (matching name and namespace).</li>
<li>&quot;WithinClusterQueue&quot;: restricts preemption candidates to workloads submitted to the same ClusterQueue as the preemptor.</li>
<li>&quot;WithinParentCohort&quot;: restricts preemption candidates to workloads in ClusterQueues that share the exact same immediate direct Cohort, as well as workloads in the preemptor's own ClusterQueue (even if standalone).</li>
<li>&quot;WithinCohortTree&quot;: restricts preemption candidates to workloads in ClusterQueues that belong to the same Cohort Tree (sharing the same root ancestor Cohort), as well as workloads in the preemptor's own ClusterQueue (even if standalone).</li>
<li>&quot;AnyClusterQueue&quot;: places no relationship restrictions on preemption candidates.</li>
</ul>




## `PreemptionConfigPreemptionRule`     {#kueue-x-k8s-io-v1alpha1-PreemptionConfigPreemptionRule}
    

**Appears in:**

- [PreemptionConfigSpec](#kueue-x-k8s-io-v1alpha1-PreemptionConfigSpec)


<p>PreemptionConfigPreemptionRule defines a single rule under which preemptions can be triggered
and the candidate workloads eligible for preemption.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>name is the identifier of the preemption rule.</p>
</td>
</tr>
<tr><td><code>preemptorSelector</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#labelselector-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.LabelSelector</code></a>
</td>
<td>
   <p>preemptorSelector is a label selector indicating which workloads can trigger preemptions
using this rule. Accepts all workloads if not set.</p>
</td>
</tr>
<tr><td><code>activationPolicy,omitzero</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1alpha1-PreemptionConfigActivationPolicy"><code>PreemptionConfigActivationPolicy</code></a>
</td>
<td>
   <p>activationPolicy determines when this rule contributes matching
candidates to preemption evaluation.</p>
</td>
</tr>
<tr><td><code>candidateSelectors</code><br/>
<a href="#kueue-x-k8s-io-v1alpha1-PreemptionConfigPreemptionCandidateSelector"><code>[]PreemptionConfigPreemptionCandidateSelector</code></a>
</td>
<td>
   <p>candidateSelectors specifies the selection rules for workloads that are candidates for preemption.
Candidates resulting from multiple selectors are summed into one set.
No selectors result in an empty candidate set, thereby disallowing any preemptions with this rule.</p>
</td>
</tr>
</tbody>
</table>

## `PreemptionConfigPriorityConstraint`     {#kueue-x-k8s-io-v1alpha1-PreemptionConfigPriorityConstraint}
    

**Appears in:**

- [PreemptionConfigPreemptionCandidateSelector](#kueue-x-k8s-io-v1alpha1-PreemptionConfigPreemptionCandidateSelector)


<p>PreemptionConfigPriorityConstraint defines the requirements for the priority of preemption candidates.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>mode</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1alpha1-PreemptionConfigPriorityMode"><code>PreemptionConfigPriorityMode</code></a>
</td>
<td>
   <p>mode specifies whether priority comparison uses base or boosted (effective) priority.</p>
</td>
</tr>
<tr><td><code>comparison</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1alpha1-NumericComparison"><code>NumericComparison</code></a>
</td>
<td>
   <p>comparison defines how the candidate's priority compares to the preemptor's priority.
For example, &quot;LessThan&quot; means that only workloads with lower
priority will be allowed as preemption candidates.</p>
</td>
</tr>
</tbody>
</table>

## `PreemptionConfigPriorityMode`     {#kueue-x-k8s-io-v1alpha1-PreemptionConfigPriorityMode}
    
(Alias of `string`)

**Appears in:**

- [PreemptionConfigPriorityConstraint](#kueue-x-k8s-io-v1alpha1-PreemptionConfigPriorityConstraint)


<p>PreemptionConfigPriorityMode defines whether base or boosted (effective) priority is used when comparing candidates against the preemptor.
Possible values are:</p>
<ul>
<li>&quot;Base&quot;: uses the raw priority value as assigned in the Workload resource (<code>spec.priority</code>) for both the candidate and preemptor, ignoring any priority boost.</li>
<li>&quot;Boosted&quot;: uses the effective priority value, adjusted by the priority boost mechanism (if enabled), for both the candidate and preemptor.</li>
</ul>




## `PreemptionConfigSpec`     {#kueue-x-k8s-io-v1alpha1-PreemptionConfigSpec}
    

**Appears in:**

- [PreemptionConfig](#kueue-x-k8s-io-v1alpha1-PreemptionConfig)


<p>PreemptionConfigSpec defines the desired state of PreemptionConfig</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>rules</code><br/>
<a href="#kueue-x-k8s-io-v1alpha1-PreemptionConfigPreemptionRule"><code>[]PreemptionConfigPreemptionRule</code></a>
</td>
<td>
   <p>rules specifies preemption candidate selection rules.</p>
</td>
</tr>
</tbody>
</table>

## `ResourceFlavorReference`     {#kueue-x-k8s-io-v1alpha1-ResourceFlavorReference}
    
(Alias of `string`)

**Appears in:**

- [CapacityProviderNormalizedCapacityFlavor](#kueue-x-k8s-io-v1alpha1-CapacityProviderNormalizedCapacityFlavor)

- [CapacityProviderOrchestratedFlavor](#kueue-x-k8s-io-v1alpha1-CapacityProviderOrchestratedFlavor)

- [EffectiveCapacityFlavor](#kueue-x-k8s-io-v1alpha1-EffectiveCapacityFlavor)


<p>TODO: once we graduate this API to beta, we should leverage beta API's ResourceFlavorReference type instead of Alpha one.
ResourceFlavorReference is the name of the ResourceFlavor.</p>




## `SubtreeRootRefKind`     {#kueue-x-k8s-io-v1alpha1-SubtreeRootRefKind}
    
(Alias of `string`)

**Appears in:**

- [CapacityDistributionSubtreeRootRef](#kueue-x-k8s-io-v1alpha1-CapacityDistributionSubtreeRootRef)




  