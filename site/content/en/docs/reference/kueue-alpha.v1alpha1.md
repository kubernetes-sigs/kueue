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

## `Cohort`     {#kueue-x-k8s-io-v1alpha1-Cohort}
    

**Appears in:**



<p>Cohort is the Schema for the cohorts API. Using Hierarchical
Cohorts (any Cohort which has a parent) with Fair Sharing
results in undefined behavior in 0.9</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>spec</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1alpha1-CohortSpec"><code>CohortSpec</code></a>
</td>
<td>
   <span class="text-muted">No description provided.</span></td>
</tr>
</tbody>
</table>

## `CohortSpec`     {#kueue-x-k8s-io-v1alpha1-CohortSpec}
    

**Appears in:**

- [Cohort](#kueue-x-k8s-io-v1alpha1-Cohort)


<p>CohortSpec defines the desired state of Cohort</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>parent</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>Parent references the name of the Cohort's parent, if
any. It satisfies one of three cases:</p>
<ol>
<li>Unset. This Cohort is the root of its Cohort tree.</li>
<li>References a non-existent Cohort. We use default Cohort (no borrowing/lending limits).</li>
<li>References an existent Cohort.</li>
</ol>
<p>If a cycle is created, we disable all members of the
Cohort, including ClusterQueues, until the cycle is
removed.  We prevent further admission while the cycle
exists.</p>
</td>
</tr>
<tr><td><code>resourceGroups</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-ResourceGroup"><code>[]ResourceGroup</code></a>
</td>
<td>
   <p>ResourceGroups describes groupings of Resources and
Flavors.  Each ResourceGroup defines a list of Resources
and a list of Flavors which provide quotas for these
Resources. Each Resource and each Flavor may only form part
of one ResourceGroup.  There may be up to 16 ResourceGroups
within a Cohort.</p>
<p>BorrowingLimit limits how much members of this Cohort
subtree can borrow from the parent subtree.</p>
<p>LendingLimit limits how much members of this Cohort subtree
can lend to the parent subtree.</p>
<p>Borrowing and Lending limits must only be set when the
Cohort has a parent.  Otherwise, the Cohort create/update
will be rejected by the webhook.</p>
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
  