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
    
  
<tr><td><code>controllerName</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>controllerName is name of the controller which will actually perform
the checks. This is the name with which controller identifies with,
not necessarily a K8S Pod or Deployment name. Cannot be empty.</p>
</td>
</tr>
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
  