---
title: Kueue Alpha API
content_type: tool-reference
package: kueue.x-k8s.io/v1alpha1
auto_generated: true
description: Generated API reference documentation for kueue.x-k8s.io/v1alpha1.
---


## Resource Types 


- [DynamicResourceAllocationConfig](#kueue-x-k8s-io-v1alpha1-DynamicResourceAllocationConfig)
- [Topology](#kueue-x-k8s-io-v1alpha1-Topology)
  

## `DynamicResourceAllocationConfig`     {#kueue-x-k8s-io-v1alpha1-DynamicResourceAllocationConfig}
    

**Appears in:**



<p>DynamicResourceAllocationConfig is a singleton CRD that maps a logical resource name to one or more DeviceClasses
in the cluster. Only one instance named &quot;default&quot; in the kueue-system namespace
is allowed.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
<tr><td><code>apiVersion</code><br/>string</td><td><code>kueue.x-k8s.io/v1alpha1</code></td></tr>
<tr><td><code>kind</code><br/>string</td><td><code>DynamicResourceAllocationConfig</code></td></tr>
    
  
<tr><td><code>spec</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1alpha1-DynamicResourceAllocationConfigSpec"><code>DynamicResourceAllocationConfigSpec</code></a>
</td>
<td>
   <span class="text-muted">No description provided.</span></td>
</tr>
</tbody>
</table>

## `Topology`     {#kueue-x-k8s-io-v1alpha1-Topology}
    

**Appears in:**



<p>Topology is the Schema for the topology API</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
<tr><td><code>apiVersion</code><br/>string</td><td><code>kueue.x-k8s.io/v1alpha1</code></td></tr>
<tr><td><code>kind</code><br/>string</td><td><code>Topology</code></td></tr>
    
  
<tr><td><code>spec</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1alpha1-TopologySpec"><code>TopologySpec</code></a>
</td>
<td>
   <span class="text-muted">No description provided.</span></td>
</tr>
</tbody>
</table>

## `DynamicResource`     {#kueue-x-k8s-io-v1alpha1-DynamicResource}
    

**Appears in:**

- [DynamicResourceAllocationConfigSpec](#kueue-x-k8s-io-v1alpha1-DynamicResourceAllocationConfigSpec)


<p>DynamicResource describes a single logical resource and the DeviceClasses mapping. The resource name is used
to quota in ClusterQueue.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#resourcename-v1-core"><code>k8s.io/api/core/v1.ResourceName</code></a>
</td>
<td>
   <p>Name is referenced in ClusterQueue.nominalQuota and Workload status.</p>
</td>
</tr>
<tr><td><code>deviceClassNames</code> <B>[Required]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#resourcename-v1-core"><code>[]k8s.io/api/core/v1.ResourceName</code></a>
</td>
<td>
   <p>DeviceClassNames enumerates the DeviceClasses represented by this resource name.</p>
</td>
</tr>
</tbody>
</table>

## `DynamicResourceAllocationConfigSpec`     {#kueue-x-k8s-io-v1alpha1-DynamicResourceAllocationConfigSpec}
    

**Appears in:**

- [DynamicResourceAllocationConfig](#kueue-x-k8s-io-v1alpha1-DynamicResourceAllocationConfig)


<p>DynamicResourceAllocationConfigSpec holds all resource to DeviceClass mappings.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>resources</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1alpha1-DynamicResource"><code>[]DynamicResource</code></a>
</td>
<td>
   <p>Resources lists logical resources that Kueue will account.</p>
</td>
</tr>
</tbody>
</table>

## `TopologyLevel`     {#kueue-x-k8s-io-v1alpha1-TopologyLevel}
    

**Appears in:**

- [TopologySpec](#kueue-x-k8s-io-v1alpha1-TopologySpec)


<p>TopologyLevel defines the desired state of TopologyLevel</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>nodeLabel</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>nodeLabel indicates the name of the node label for a specific topology
level.</p>
<p>Examples:</p>
<ul>
<li>cloud.provider.com/topology-block</li>
<li>cloud.provider.com/topology-rack</li>
</ul>
</td>
</tr>
</tbody>
</table>

## `TopologySpec`     {#kueue-x-k8s-io-v1alpha1-TopologySpec}
    

**Appears in:**

- [Topology](#kueue-x-k8s-io-v1alpha1-Topology)


<p>TopologySpec defines the desired state of Topology</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>levels</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1alpha1-TopologyLevel"><code>[]TopologyLevel</code></a>
</td>
<td>
   <p>levels define the levels of topology.</p>
</td>
</tr>
</tbody>
</table>
  