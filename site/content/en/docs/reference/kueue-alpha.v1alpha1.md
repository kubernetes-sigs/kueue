---
title: Kueue Alpha API
content_type: tool-reference
package: kueue.x-k8s.io/v1alpha1
auto_generated: true
description: Generated API reference documentation for kueue.x-k8s.io/v1alpha1.
---


## Resource Types 


- [Topology](#kueue-x-k8s-io-v1alpha1-Topology)
  

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
  