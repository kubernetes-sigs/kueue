---
title: Kueue Alpha API
content_type: tool-reference
package: kueue.x-k8s.io/v1alpha1
auto_generated: true
description: Generated API reference documentation for kueue.x-k8s.io/v1alpha1.
---


## Resource Types 


- [MultiKueueCluster](#kueue-x-k8s-io-v1alpha1-MultiKueueCluster)
- [MultiKueueConfig](#kueue-x-k8s-io-v1alpha1-MultiKueueConfig)
  

## `MultiKueueCluster`     {#kueue-x-k8s-io-v1alpha1-MultiKueueCluster}
    

**Appears in:**



<p>MultiKueueCluster is the Schema for the multikueue API</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
<tr><td><code>apiVersion</code><br/>string</td><td><code>kueue.x-k8s.io/v1alpha1</code></td></tr>
<tr><td><code>kind</code><br/>string</td><td><code>MultiKueueCluster</code></td></tr>
    
  
<tr><td><code>spec</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1alpha1-MultiKueueClusterSpec"><code>MultiKueueClusterSpec</code></a>
</td>
<td>
   <span class="text-muted">No description provided.</span></td>
</tr>
<tr><td><code>status</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1alpha1-MultiKueueClusterStatus"><code>MultiKueueClusterStatus</code></a>
</td>
<td>
   <span class="text-muted">No description provided.</span></td>
</tr>
</tbody>
</table>

## `MultiKueueConfig`     {#kueue-x-k8s-io-v1alpha1-MultiKueueConfig}
    

**Appears in:**



<p>MultiKueueConfig is the Schema for the multikueue API</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
<tr><td><code>apiVersion</code><br/>string</td><td><code>kueue.x-k8s.io/v1alpha1</code></td></tr>
<tr><td><code>kind</code><br/>string</td><td><code>MultiKueueConfig</code></td></tr>
    
  
<tr><td><code>spec</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1alpha1-MultiKueueConfigSpec"><code>MultiKueueConfigSpec</code></a>
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
<tr><td><code>status</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1alpha1-CohortStatus"><code>CohortStatus</code></a>
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

## `CohortStatus`     {#kueue-x-k8s-io-v1alpha1-CohortStatus}
    

**Appears in:**

- [Cohort](#kueue-x-k8s-io-v1alpha1-Cohort)


<p>CohortStatus defines the observed state of Cohort</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>conditions</code> <B>[Required]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#condition-v1-meta"><code>[]k8s.io/apimachinery/pkg/apis/meta/v1.Condition</code></a>
</td>
<td>
   <span class="text-muted">No description provided.</span></td>
</tr>
</tbody>
</table>

## `KubeConfig`     {#kueue-x-k8s-io-v1alpha1-KubeConfig}
    

**Appears in:**

- [MultiKueueClusterSpec](#kueue-x-k8s-io-v1alpha1-MultiKueueClusterSpec)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>location</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>Location of the KubeConfig.</p>
<p>If LocationType is Secret then Location is the name of the secret inside the namespace in
which the kueue controller manager is running. The config should be stored in the &quot;kubeconfig&quot; key.</p>
</td>
</tr>
<tr><td><code>locationType</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1alpha1-LocationType"><code>LocationType</code></a>
</td>
<td>
   <p>Type of the KubeConfig location.</p>
</td>
</tr>
</tbody>
</table>

## `LocationType`     {#kueue-x-k8s-io-v1alpha1-LocationType}
    
(Alias of `string`)

**Appears in:**

- [KubeConfig](#kueue-x-k8s-io-v1alpha1-KubeConfig)





## `MultiKueueClusterSpec`     {#kueue-x-k8s-io-v1alpha1-MultiKueueClusterSpec}
    

**Appears in:**

- [MultiKueueCluster](#kueue-x-k8s-io-v1alpha1-MultiKueueCluster)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>kubeConfig</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1alpha1-KubeConfig"><code>KubeConfig</code></a>
</td>
<td>
   <p>Information how to connect to the cluster.</p>
</td>
</tr>
</tbody>
</table>

## `MultiKueueClusterStatus`     {#kueue-x-k8s-io-v1alpha1-MultiKueueClusterStatus}
    

**Appears in:**

- [MultiKueueCluster](#kueue-x-k8s-io-v1alpha1-MultiKueueCluster)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>conditions</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#condition-v1-meta"><code>[]k8s.io/apimachinery/pkg/apis/meta/v1.Condition</code></a>
</td>
<td>
   <span class="text-muted">No description provided.</span></td>
</tr>
</tbody>
</table>

## `MultiKueueConfigSpec`     {#kueue-x-k8s-io-v1alpha1-MultiKueueConfigSpec}
    

**Appears in:**

- [MultiKueueConfig](#kueue-x-k8s-io-v1alpha1-MultiKueueConfig)


<p>MultiKueueConfigSpec defines the desired state of MultiKueueConfig</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>clusters</code> <B>[Required]</B><br/>
<code>[]string</code>
</td>
<td>
   <p>List of MultiKueueClusters names where the workloads from the ClusterQueue should be distributed.</p>
</td>
</tr>
</tbody>
</table>
  