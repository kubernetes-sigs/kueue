---
title: Kueue v1beta2 API
content_type: tool-reference
package: kueue.x-k8s.io/v1beta2
auto_generated: true
description: Generated API reference documentation for kueue.x-k8s.io/v1beta2.
---


## Resource Types 


- [AdmissionCheck](#kueue-x-k8s-io-v1beta2-AdmissionCheck)
- [ClusterQueue](#kueue-x-k8s-io-v1beta2-ClusterQueue)
- [Cohort](#kueue-x-k8s-io-v1beta2-Cohort)
- [LocalQueue](#kueue-x-k8s-io-v1beta2-LocalQueue)
- [MultiKueueCluster](#kueue-x-k8s-io-v1beta2-MultiKueueCluster)
- [MultiKueueConfig](#kueue-x-k8s-io-v1beta2-MultiKueueConfig)
- [ProvisioningRequestConfig](#kueue-x-k8s-io-v1beta2-ProvisioningRequestConfig)
- [ResourceFlavor](#kueue-x-k8s-io-v1beta2-ResourceFlavor)
- [Topology](#kueue-x-k8s-io-v1beta2-Topology)
- [Workload](#kueue-x-k8s-io-v1beta2-Workload)
- [WorkloadPriorityClass](#kueue-x-k8s-io-v1beta2-WorkloadPriorityClass)
  

## `AdmissionCheck`     {#kueue-x-k8s-io-v1beta2-AdmissionCheck}
    

**Appears in:**



<p>AdmissionCheck is the Schema for the admissionchecks API</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
<tr><td><code>apiVersion</code><br/>string</td><td><code>kueue.x-k8s.io/v1beta2</code></td></tr>
<tr><td><code>kind</code><br/>string</td><td><code>AdmissionCheck</code></td></tr>
    
  
<tr><td><code>spec</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-AdmissionCheckSpec"><code>AdmissionCheckSpec</code></a>
</td>
<td>
   <p>spec is the specification of the AdmissionCheck.</p>
</td>
</tr>
<tr><td><code>status</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-AdmissionCheckStatus"><code>AdmissionCheckStatus</code></a>
</td>
<td>
   <p>status is the status of the AdmissionCheck.</p>
</td>
</tr>
</tbody>
</table>

## `ClusterQueue`     {#kueue-x-k8s-io-v1beta2-ClusterQueue}
    

**Appears in:**



<p>ClusterQueue is the Schema for the clusterQueue API.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
<tr><td><code>apiVersion</code><br/>string</td><td><code>kueue.x-k8s.io/v1beta2</code></td></tr>
<tr><td><code>kind</code><br/>string</td><td><code>ClusterQueue</code></td></tr>
    
  
<tr><td><code>spec</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-ClusterQueueSpec"><code>ClusterQueueSpec</code></a>
</td>
<td>
   <p>spec is the specification of the ClusterQueue.</p>
</td>
</tr>
<tr><td><code>status</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-ClusterQueueStatus"><code>ClusterQueueStatus</code></a>
</td>
<td>
   <p>status is the status of the ClusterQueue.</p>
</td>
</tr>
</tbody>
</table>

## `Cohort`     {#kueue-x-k8s-io-v1beta2-Cohort}
    

**Appears in:**



<p>Cohort defines the Cohorts API.</p>
<p>Hierarchical Cohorts (any Cohort which has a parent) are compatible
with Fair Sharing as of v0.11. Using these features together in
V0.9 and V0.10 is unsupported, and results in undefined behavior.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
<tr><td><code>apiVersion</code><br/>string</td><td><code>kueue.x-k8s.io/v1beta2</code></td></tr>
<tr><td><code>kind</code><br/>string</td><td><code>Cohort</code></td></tr>
    
  
<tr><td><code>spec</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-CohortSpec"><code>CohortSpec</code></a>
</td>
<td>
   <p>spec is the specification of the Cohort.</p>
</td>
</tr>
<tr><td><code>status</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-CohortStatus"><code>CohortStatus</code></a>
</td>
<td>
   <p>status is the status of the Cohort.</p>
</td>
</tr>
</tbody>
</table>

## `LocalQueue`     {#kueue-x-k8s-io-v1beta2-LocalQueue}
    

**Appears in:**



<p>LocalQueue is the Schema for the localQueues API</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
<tr><td><code>apiVersion</code><br/>string</td><td><code>kueue.x-k8s.io/v1beta2</code></td></tr>
<tr><td><code>kind</code><br/>string</td><td><code>LocalQueue</code></td></tr>
    
  
<tr><td><code>spec</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-LocalQueueSpec"><code>LocalQueueSpec</code></a>
</td>
<td>
   <p>spec is the specification of the LocalQueue.</p>
</td>
</tr>
<tr><td><code>status</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-LocalQueueStatus"><code>LocalQueueStatus</code></a>
</td>
<td>
   <p>status is the status of the LocalQueue.</p>
</td>
</tr>
</tbody>
</table>

## `MultiKueueCluster`     {#kueue-x-k8s-io-v1beta2-MultiKueueCluster}
    

**Appears in:**



<p>MultiKueueCluster is the Schema for the multikueue API</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
<tr><td><code>apiVersion</code><br/>string</td><td><code>kueue.x-k8s.io/v1beta2</code></td></tr>
<tr><td><code>kind</code><br/>string</td><td><code>MultiKueueCluster</code></td></tr>
    
  
<tr><td><code>spec,omitempty,omitzero</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-MultiKueueClusterSpec"><code>MultiKueueClusterSpec</code></a>
</td>
<td>
   <p>spec is the specification of the MultiKueueCluster.</p>
</td>
</tr>
<tr><td><code>status</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-MultiKueueClusterStatus"><code>MultiKueueClusterStatus</code></a>
</td>
<td>
   <p>status is the status of the MultiKueueCluster.</p>
</td>
</tr>
</tbody>
</table>

## `MultiKueueConfig`     {#kueue-x-k8s-io-v1beta2-MultiKueueConfig}
    

**Appears in:**



<p>MultiKueueConfig is the Schema for the multikueue API</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
<tr><td><code>apiVersion</code><br/>string</td><td><code>kueue.x-k8s.io/v1beta2</code></td></tr>
<tr><td><code>kind</code><br/>string</td><td><code>MultiKueueConfig</code></td></tr>
    
  
<tr><td><code>spec</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-MultiKueueConfigSpec"><code>MultiKueueConfigSpec</code></a>
</td>
<td>
   <p>spec is the specification of the MultiKueueConfig.</p>
</td>
</tr>
</tbody>
</table>

## `ProvisioningRequestConfig`     {#kueue-x-k8s-io-v1beta2-ProvisioningRequestConfig}
    

**Appears in:**



<p>ProvisioningRequestConfig is the Schema for the provisioningrequestconfig API</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
<tr><td><code>apiVersion</code><br/>string</td><td><code>kueue.x-k8s.io/v1beta2</code></td></tr>
<tr><td><code>kind</code><br/>string</td><td><code>ProvisioningRequestConfig</code></td></tr>
    
  
<tr><td><code>spec</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-ProvisioningRequestConfigSpec"><code>ProvisioningRequestConfigSpec</code></a>
</td>
<td>
   <p>spec is the specification of the ProvisioningRequestConfig.</p>
</td>
</tr>
</tbody>
</table>

## `ResourceFlavor`     {#kueue-x-k8s-io-v1beta2-ResourceFlavor}
    

**Appears in:**



<p>ResourceFlavor is the Schema for the resourceflavors API.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
<tr><td><code>apiVersion</code><br/>string</td><td><code>kueue.x-k8s.io/v1beta2</code></td></tr>
<tr><td><code>kind</code><br/>string</td><td><code>ResourceFlavor</code></td></tr>
    
  
<tr><td><code>spec</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-ResourceFlavorSpec"><code>ResourceFlavorSpec</code></a>
</td>
<td>
   <p>spec is the specification of the ResourceFlavor.</p>
</td>
</tr>
</tbody>
</table>

## `Topology`     {#kueue-x-k8s-io-v1beta2-Topology}
    

**Appears in:**



<p>Topology is the Schema for the topology API</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
<tr><td><code>apiVersion</code><br/>string</td><td><code>kueue.x-k8s.io/v1beta2</code></td></tr>
<tr><td><code>kind</code><br/>string</td><td><code>Topology</code></td></tr>
    
  
<tr><td><code>spec</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-TopologySpec"><code>TopologySpec</code></a>
</td>
<td>
   <p>spec is the specification of the Topology.</p>
</td>
</tr>
</tbody>
</table>

## `Workload`     {#kueue-x-k8s-io-v1beta2-Workload}
    

**Appears in:**



<p>Workload is the Schema for the workloads API</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
<tr><td><code>apiVersion</code><br/>string</td><td><code>kueue.x-k8s.io/v1beta2</code></td></tr>
<tr><td><code>kind</code><br/>string</td><td><code>Workload</code></td></tr>
    
  
<tr><td><code>spec</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-WorkloadSpec"><code>WorkloadSpec</code></a>
</td>
<td>
   <p>spec is the specification of the Workload.</p>
</td>
</tr>
<tr><td><code>status</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-WorkloadStatus"><code>WorkloadStatus</code></a>
</td>
<td>
   <p>status is the status of the Workload.</p>
</td>
</tr>
</tbody>
</table>

## `WorkloadPriorityClass`     {#kueue-x-k8s-io-v1beta2-WorkloadPriorityClass}
    

**Appears in:**



<p>WorkloadPriorityClass is the Schema for the workloadPriorityClass API</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
<tr><td><code>apiVersion</code><br/>string</td><td><code>kueue.x-k8s.io/v1beta2</code></td></tr>
<tr><td><code>kind</code><br/>string</td><td><code>WorkloadPriorityClass</code></td></tr>
    
  
<tr><td><code>value</code> <B>[Required]</B><br/>
<code>int32</code>
</td>
<td>
   <p>value represents the integer value of this workloadPriorityClass. This is the actual priority that workloads
receive when jobs have the name of this class in their workloadPriorityClass label.
Changing the value of workloadPriorityClass doesn't affect the priority of workloads that were already created.</p>
</td>
</tr>
<tr><td><code>description</code><br/>
<code>string</code>
</td>
<td>
   <p>description is an arbitrary string that usually provides guidelines on
when this workloadPriorityClass should be used.
The description is limited to a maximum of 2048 characters.</p>
</td>
</tr>
</tbody>
</table>

## `Admission`     {#kueue-x-k8s-io-v1beta2-Admission}
    

**Appears in:**

- [WorkloadStatus](#kueue-x-k8s-io-v1beta2-WorkloadStatus)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>clusterQueue</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-ClusterQueueReference"><code>ClusterQueueReference</code></a>
</td>
<td>
   <p>clusterQueue is the name of the ClusterQueue that admitted this workload.</p>
</td>
</tr>
<tr><td><code>podSetAssignments</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-PodSetAssignment"><code>[]PodSetAssignment</code></a>
</td>
<td>
   <p>podSetAssignments hold the admission results for each of the .spec.podSets entries.</p>
</td>
</tr>
</tbody>
</table>

## `AdmissionCheckParametersReference`     {#kueue-x-k8s-io-v1beta2-AdmissionCheckParametersReference}
    

**Appears in:**

- [AdmissionCheckSpec](#kueue-x-k8s-io-v1beta2-AdmissionCheckSpec)



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

## `AdmissionCheckReference`     {#kueue-x-k8s-io-v1beta2-AdmissionCheckReference}
    
(Alias of `string`)

**Appears in:**

- [AdmissionCheckState](#kueue-x-k8s-io-v1beta2-AdmissionCheckState)

- [AdmissionCheckStrategyRule](#kueue-x-k8s-io-v1beta2-AdmissionCheckStrategyRule)


<p>AdmissionCheckReference is the name of an AdmissionCheck.</p>




## `AdmissionCheckSpec`     {#kueue-x-k8s-io-v1beta2-AdmissionCheckSpec}
    

**Appears in:**

- [AdmissionCheck](#kueue-x-k8s-io-v1beta2-AdmissionCheck)


<p>AdmissionCheckSpec defines the desired state of AdmissionCheck</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>controllerName</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>controllerName identifies the controller that processes the AdmissionCheck,
not necessarily a Kubernetes Pod or Deployment name. Cannot be empty.</p>
</td>
</tr>
<tr><td><code>parameters</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-AdmissionCheckParametersReference"><code>AdmissionCheckParametersReference</code></a>
</td>
<td>
   <p>parameters identifies a configuration with additional parameters for the
check.</p>
</td>
</tr>
</tbody>
</table>

## `AdmissionCheckState`     {#kueue-x-k8s-io-v1beta2-AdmissionCheckState}
    

**Appears in:**

- [WorkloadStatus](#kueue-x-k8s-io-v1beta2-WorkloadStatus)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta2-AdmissionCheckReference"><code>AdmissionCheckReference</code></a>
</td>
<td>
   <p>name identifies the admission check.</p>
</td>
</tr>
<tr><td><code>state</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta2-CheckState"><code>CheckState</code></a>
</td>
<td>
   <p>state of the admissionCheck, one of Pending, Ready, Retry, Rejected</p>
</td>
</tr>
<tr><td><code>lastTransitionTime,omitempty,omitzero</code> <B>[Required]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#time-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.Time</code></a>
</td>
<td>
   <p>lastTransitionTime is the last time the condition transitioned from one status to another.
This should be when the underlying condition changed.  If that is not known, then using the time when the API field changed is acceptable.</p>
</td>
</tr>
<tr><td><code>message</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>message is a human readable message indicating details about the transition.
This may be an empty string.</p>
</td>
</tr>
<tr><td><code>requeueAfterSeconds</code><br/>
<code>int32</code>
</td>
<td>
   <p>requeueAfterSeconds indicates how long to wait at least before
retrying to admit the workload.
The admission check controllers can set this field when State=Retry
to implement delays between retry attempts.</p>
<p>If nil when State=Retry, Kueue will retry immediately.
If set, Kueue will add the workload back to the queue after
lastTransitionTime + RequeueAfterSeconds is over.</p>
</td>
</tr>
<tr><td><code>retryCount</code><br/>
<code>int32</code>
</td>
<td>
   <p>retryCount tracks retry attempts for this admission check.
Kueue automatically increments the counter whenever the
state transitions to Retry.</p>
</td>
</tr>
<tr><td><code>podSetUpdates</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-PodSetUpdate"><code>[]PodSetUpdate</code></a>
</td>
<td>
   <p>podSetUpdates contains a list of pod set modifications suggested by AdmissionChecks.</p>
</td>
</tr>
</tbody>
</table>

## `AdmissionCheckStatus`     {#kueue-x-k8s-io-v1beta2-AdmissionCheckStatus}
    

**Appears in:**

- [AdmissionCheck](#kueue-x-k8s-io-v1beta2-AdmissionCheck)


<p>AdmissionCheckStatus defines the observed state of AdmissionCheck</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>conditions</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#condition-v1-meta"><code>[]k8s.io/apimachinery/pkg/apis/meta/v1.Condition</code></a>
</td>
<td>
   <p>conditions hold the latest available observations of the AdmissionCheck
current state.
This is limited to at most 16 separate conditions.</p>
</td>
</tr>
</tbody>
</table>

## `AdmissionCheckStrategyRule`     {#kueue-x-k8s-io-v1beta2-AdmissionCheckStrategyRule}
    

**Appears in:**

- [AdmissionChecksStrategy](#kueue-x-k8s-io-v1beta2-AdmissionChecksStrategy)


<p>AdmissionCheckStrategyRule defines rules for a single AdmissionCheck</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta2-AdmissionCheckReference"><code>AdmissionCheckReference</code></a>
</td>
<td>
   <p>name is an AdmissionCheck's name.</p>
</td>
</tr>
<tr><td><code>onFlavors</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-ResourceFlavorReference"><code>[]ResourceFlavorReference</code></a>
</td>
<td>
   <p>onFlavors is a list of ResourceFlavors' names that this AdmissionCheck should run for.
If empty, the AdmissionCheck will run for all workloads submitted to the ClusterQueue.</p>
</td>
</tr>
</tbody>
</table>

## `AdmissionChecksStrategy`     {#kueue-x-k8s-io-v1beta2-AdmissionChecksStrategy}
    

**Appears in:**

- [ClusterQueueSpec](#kueue-x-k8s-io-v1beta2-ClusterQueueSpec)


<p>AdmissionChecksStrategy defines a strategy for a AdmissionCheck.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>admissionChecks</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta2-AdmissionCheckStrategyRule"><code>[]AdmissionCheckStrategyRule</code></a>
</td>
<td>
   <p>admissionChecks is a list of strategies for AdmissionChecks</p>
</td>
</tr>
</tbody>
</table>

## `AdmissionMode`     {#kueue-x-k8s-io-v1beta2-AdmissionMode}
    
(Alias of `string`)

**Appears in:**

- [AdmissionScope](#kueue-x-k8s-io-v1beta2-AdmissionScope)





## `AdmissionScope`     {#kueue-x-k8s-io-v1beta2-AdmissionScope}
    

**Appears in:**

- [ClusterQueueSpec](#kueue-x-k8s-io-v1beta2-ClusterQueueSpec)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>admissionMode</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta2-AdmissionMode"><code>AdmissionMode</code></a>
</td>
<td>
   <p>admissionMode indicates which mode for AdmissionFairSharing should be used
in the AdmissionScope. Possible values are:</p>
<ul>
<li>UsageBasedAdmissionFairSharing</li>
<li>NoAdmissionFairSharing</li>
</ul>
</td>
</tr>
</tbody>
</table>

## `BorrowWithinCohort`     {#kueue-x-k8s-io-v1beta2-BorrowWithinCohort}
    

**Appears in:**

- [ClusterQueuePreemption](#kueue-x-k8s-io-v1beta2-ClusterQueuePreemption)


<p>BorrowWithinCohort contains configuration which allows to preempt workloads
within cohort while borrowing. It only works with Classical Preemption,
<strong>not</strong> with Fair Sharing.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>policy</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-BorrowWithinCohortPolicy"><code>BorrowWithinCohortPolicy</code></a>
</td>
<td>
   <p>policy determines the policy for preemption to reclaim quota within cohort while borrowing.
Possible values are:</p>
<ul>
<li><code>Never</code> (default): do not allow for preemption, in other
ClusterQueues within the cohort, for a borrowing workload.</li>
<li><code>LowerPriority</code>: allow preemption, in other ClusterQueues
within the cohort, for a borrowing workload, but only if
the preempted workloads are of lower priority.</li>
</ul>
</td>
</tr>
<tr><td><code>maxPriorityThreshold</code><br/>
<code>int32</code>
</td>
<td>
   <p>maxPriorityThreshold allows to restrict the set of workloads which
might be preempted by a borrowing workload, to only workloads with
priority less than or equal to the specified threshold priority.
When the threshold is not specified, then any workload satisfying the
policy can be preempted by the borrowing workload.</p>
</td>
</tr>
</tbody>
</table>

## `BorrowWithinCohortPolicy`     {#kueue-x-k8s-io-v1beta2-BorrowWithinCohortPolicy}
    
(Alias of `string`)

**Appears in:**

- [BorrowWithinCohort](#kueue-x-k8s-io-v1beta2-BorrowWithinCohort)





## `CheckState`     {#kueue-x-k8s-io-v1beta2-CheckState}
    
(Alias of `string`)

**Appears in:**

- [AdmissionCheckState](#kueue-x-k8s-io-v1beta2-AdmissionCheckState)





## `ClusterProfileReference`     {#kueue-x-k8s-io-v1beta2-ClusterProfileReference}
    

**Appears in:**

- [ClusterSource](#kueue-x-k8s-io-v1beta2-ClusterSource)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>name of the ClusterProfile.</p>
</td>
</tr>
</tbody>
</table>

## `ClusterQueuePreemption`     {#kueue-x-k8s-io-v1beta2-ClusterQueuePreemption}
    

**Appears in:**

- [ClusterQueueSpec](#kueue-x-k8s-io-v1beta2-ClusterQueueSpec)


<p>ClusterQueuePreemption contains policies to preempt Workloads from this
ClusterQueue or the ClusterQueue's cohort.</p>
<p>Preemption may be configured to work in the following scenarios:</p>
<ul>
<li>When a Workload fits within the nominal quota of the ClusterQueue, but
the quota is currently borrowed by other ClusterQueues in the cohort.
We preempt workloads in other ClusterQueues to allow this ClusterQueue to
reclaim its nominal quota. Configured using reclaimWithinCohort.</li>
<li>When a Workload doesn't fit within the nominal quota of the ClusterQueue
and there are admitted Workloads in the ClusterQueue with lower priority.
Configured using withinClusterQueue.</li>
<li>When a Workload may fit while both borrowing and preempting
low priority workloads in the Cohort. Configured using borrowWithinCohort.</li>
<li>When FairSharing is enabled, to maintain fair distribution of
unused resources. See FairSharing documentation.</li>
</ul>
<p>The preemption algorithm tries to find a minimal set of Workloads to
preempt to accomomdate the pending Workload, preempting Workloads with
lower priority first.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>reclaimWithinCohort</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-PreemptionPolicy"><code>PreemptionPolicy</code></a>
</td>
<td>
   <p>reclaimWithinCohort determines whether a pending Workload can preempt
Workloads from other ClusterQueues in the cohort that are using more than
their nominal quota. The possible values are:</p>
<ul>
<li><code>Never</code> (default): do not preempt Workloads in the cohort.</li>
<li><code>LowerPriority</code>: <strong>Classic Preemption</strong> if the pending Workload
fits within the nominal quota of its ClusterQueue, only preempt
Workloads in the cohort that have lower priority than the pending
Workload. <strong>Fair Sharing</strong> only preempt Workloads in the cohort that
have lower priority than the pending Workload and that satisfy the
Fair Sharing preemptionStategies.</li>
<li><code>Any</code>: <strong>Classic Preemption</strong> if the pending Workload fits within
the nominal quota of its ClusterQueue, preempt any Workload in the
cohort, irrespective of priority. <strong>Fair Sharing</strong> preempt Workloads
in the cohort that satisfy the Fair Sharing preemptionStrategies.</li>
</ul>
</td>
</tr>
<tr><td><code>borrowWithinCohort</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-BorrowWithinCohort"><code>BorrowWithinCohort</code></a>
</td>
<td>
   <p>borrowWithinCohort determines whether a pending Workload can preempt
Workloads from other ClusterQueues in the cohort if the workload requires borrowing.
May only be configured with Classical Preemption, and <strong>not</strong> with Fair Sharing.</p>
</td>
</tr>
<tr><td><code>withinClusterQueue</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-PreemptionPolicy"><code>PreemptionPolicy</code></a>
</td>
<td>
   <p>withinClusterQueue determines whether a pending Workload that doesn't fit
within the nominal quota for its ClusterQueue, can preempt active Workloads in
the ClusterQueue. The possible values are:</p>
<ul>
<li><code>Never</code> (default): do not preempt Workloads in the ClusterQueue.</li>
<li><code>LowerPriority</code>: only preempt Workloads in the ClusterQueue that have
lower priority than the pending Workload.</li>
<li><code>LowerOrNewerEqualPriority</code>: only preempt Workloads in the ClusterQueue that
either have a lower priority than the pending workload or equal priority
and are newer than the pending workload.</li>
</ul>
</td>
</tr>
</tbody>
</table>

## `ClusterQueueReference`     {#kueue-x-k8s-io-v1beta2-ClusterQueueReference}
    
(Alias of `string`)

**Appears in:**

- [Admission](#kueue-x-k8s-io-v1beta2-Admission)

- [LocalQueueSpec](#kueue-x-k8s-io-v1beta2-LocalQueueSpec)


<p>ClusterQueueReference is the name of the ClusterQueue.
It must be a DNS (RFC 1123) and has the maximum length of 253 characters.</p>




## `ClusterQueueSpec`     {#kueue-x-k8s-io-v1beta2-ClusterQueueSpec}
    

**Appears in:**

- [ClusterQueue](#kueue-x-k8s-io-v1beta2-ClusterQueue)


<p>ClusterQueueSpec defines the desired state of ClusterQueue</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>resourceGroups</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-ResourceGroup"><code>[]ResourceGroup</code></a>
</td>
<td>
   <p>resourceGroups describes groups of resources.
Each resource group defines the list of resources and a list of flavors
that provide quotas for these resources.
Each resource and each flavor can only form part of one resource group.
resourceGroups can be up to 16, with a max of 256 total flavors across all groups.</p>
</td>
</tr>
<tr><td><code>cohortName</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-CohortReference"><code>CohortReference</code></a>
</td>
<td>
   <p>cohortName that this ClusterQueue belongs to. CQs that belong to the
same cohort can borrow unused resources from each other.</p>
<p>A CQ can be a member of a single borrowing cohort. A workload submitted
to a queue referencing this CQ can borrow quota from any CQ in the cohort.
Only quota for the [resource, flavor] pairs listed in the CQ can be
borrowed.
If empty, this ClusterQueue cannot borrow from any other ClusterQueue and
vice versa.</p>
<p>A cohort is a name that links CQs together, but it doesn't reference any
object.</p>
</td>
</tr>
<tr><td><code>queueingStrategy</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-QueueingStrategy"><code>QueueingStrategy</code></a>
</td>
<td>
   <p>queueingStrategy indicates the queueing strategy of the workloads
across the queues in this ClusterQueue.
Current Supported Strategies:</p>
<ul>
<li>StrictFIFO: workloads are ordered strictly by creation time.
Older workloads that can't be admitted will block admitting newer
workloads even if they fit available quota.</li>
<li>BestEffortFIFO: workloads are ordered by creation time,
however older workloads that can't be admitted will not block
admitting newer workloads that fit existing quota.</li>
</ul>
</td>
</tr>
<tr><td><code>namespaceSelector</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#labelselector-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.LabelSelector</code></a>
</td>
<td>
   <p>namespaceSelector defines which namespaces are allowed to submit workloads to
this clusterQueue. Beyond this basic support for policy, a policy agent like
Gatekeeper should be used to enforce more advanced policies.
Defaults to null which is a nothing selector (no namespaces eligible).
If set to an empty selector <code>{}</code>, then all namespaces are eligible.</p>
</td>
</tr>
<tr><td><code>flavorFungibility</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-FlavorFungibility"><code>FlavorFungibility</code></a>
</td>
<td>
   <p>flavorFungibility defines whether a workload should try the next flavor
before borrowing or preempting in the flavor being evaluated.</p>
</td>
</tr>
<tr><td><code>preemption</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-ClusterQueuePreemption"><code>ClusterQueuePreemption</code></a>
</td>
<td>
   <p>preemption defines the preemption policies.</p>
</td>
</tr>
<tr><td><code>admissionChecksStrategy</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-AdmissionChecksStrategy"><code>AdmissionChecksStrategy</code></a>
</td>
<td>
   <p>admissionChecksStrategy defines a list of strategies to determine which ResourceFlavors require AdmissionChecks.</p>
</td>
</tr>
<tr><td><code>stopPolicy</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-StopPolicy"><code>StopPolicy</code></a>
</td>
<td>
   <p>stopPolicy - if set to a value different from None, the ClusterQueue is considered Inactive, no new reservation being
made.</p>
<p>Depending on its value, its associated workloads will:</p>
<ul>
<li>None - Workloads are admitted</li>
<li>HoldAndDrain - Admitted workloads are evicted and Reserving workloads will cancel the reservation.</li>
<li>Hold - Admitted workloads will run to completion and Reserving workloads will cancel the reservation.</li>
</ul>
</td>
</tr>
<tr><td><code>fairSharing</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-FairSharing"><code>FairSharing</code></a>
</td>
<td>
   <p>fairSharing defines the properties of the ClusterQueue when
participating in FairSharing.  The values are only relevant
if FairSharing is enabled in the Kueue configuration.</p>
</td>
</tr>
<tr><td><code>admissionScope</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-AdmissionScope"><code>AdmissionScope</code></a>
</td>
<td>
   <p>admissionScope indicates whether ClusterQueue uses the Admission Fair Sharing</p>
</td>
</tr>
</tbody>
</table>

## `ClusterQueueStatus`     {#kueue-x-k8s-io-v1beta2-ClusterQueueStatus}
    

**Appears in:**

- [ClusterQueue](#kueue-x-k8s-io-v1beta2-ClusterQueue)


<p>ClusterQueueStatus defines the observed state of ClusterQueue</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>conditions</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#condition-v1-meta"><code>[]k8s.io/apimachinery/pkg/apis/meta/v1.Condition</code></a>
</td>
<td>
   <p>conditions hold the latest available observations of the ClusterQueue
current state.
conditions are limited to 16 elements.</p>
</td>
</tr>
<tr><td><code>flavorsReservation</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-FlavorUsage"><code>[]FlavorUsage</code></a>
</td>
<td>
   <p>flavorsReservation are the reserved quotas, by flavor, currently in use by the
workloads assigned to this ClusterQueue.
flavorsReservation are limited to 64 elements.</p>
</td>
</tr>
<tr><td><code>flavorsUsage</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-FlavorUsage"><code>[]FlavorUsage</code></a>
</td>
<td>
   <p>flavorsUsage are the used quotas, by flavor, currently in use by the
workloads admitted in this ClusterQueue.</p>
</td>
</tr>
<tr><td><code>pendingWorkloads</code><br/>
<code>int32</code>
</td>
<td>
   <p>pendingWorkloads is the number of workloads currently waiting to be
admitted to this clusterQueue.</p>
</td>
</tr>
<tr><td><code>reservingWorkloads</code><br/>
<code>int32</code>
</td>
<td>
   <p>reservingWorkloads is the number of workloads currently reserving quota in this
clusterQueue.</p>
</td>
</tr>
<tr><td><code>admittedWorkloads</code><br/>
<code>int32</code>
</td>
<td>
   <p>admittedWorkloads is the number of workloads currently admitted to this
clusterQueue and haven't finished yet.</p>
</td>
</tr>
<tr><td><code>fairSharing</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-FairSharingStatus"><code>FairSharingStatus</code></a>
</td>
<td>
   <p>fairSharing contains the current state for this ClusterQueue
when participating in Fair Sharing.
This is recorded only when Fair Sharing is enabled in the Kueue configuration.</p>
</td>
</tr>
</tbody>
</table>

## `ClusterSource`     {#kueue-x-k8s-io-v1beta2-ClusterSource}
    

**Appears in:**

- [MultiKueueClusterSpec](#kueue-x-k8s-io-v1beta2-MultiKueueClusterSpec)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>kubeConfig,omitempty,omitzero</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-KubeConfig"><code>KubeConfig</code></a>
</td>
<td>
   <p>kubeConfig is information on how to connect to the cluster.</p>
</td>
</tr>
<tr><td><code>clusterProfileRef</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-ClusterProfileReference"><code>ClusterProfileReference</code></a>
</td>
<td>
   <p>clusterProfileRef is the reference to the ClusterProfile object used to connect to the cluster.</p>
</td>
</tr>
</tbody>
</table>

## `CohortReference`     {#kueue-x-k8s-io-v1beta2-CohortReference}
    
(Alias of `string`)

**Appears in:**

- [ClusterQueueSpec](#kueue-x-k8s-io-v1beta2-ClusterQueueSpec)

- [CohortSpec](#kueue-x-k8s-io-v1beta2-CohortSpec)


<p>CohortReference is the name of the Cohort.</p>
<p>Validation of a cohort name is equivalent to that of object names:
subdomain in DNS (RFC 1123).</p>




## `CohortSpec`     {#kueue-x-k8s-io-v1beta2-CohortSpec}
    

**Appears in:**

- [Cohort](#kueue-x-k8s-io-v1beta2-Cohort)


<p>CohortSpec defines the desired state of Cohort</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>parentName</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-CohortReference"><code>CohortReference</code></a>
</td>
<td>
   <p>parentName references the name of the Cohort's parent, if
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
<tr><td><code>resourceGroups</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-ResourceGroup"><code>[]ResourceGroup</code></a>
</td>
<td>
   <p>resourceGroups describes groupings of Resources and
Flavors.  Each ResourceGroup defines a list of Resources
and a list of Flavors which provide quotas for these
Resources. Each Resource and each Flavor may only form part
of one ResourceGroup.  There may be up to 16 ResourceGroups
within a Cohort.</p>
<p>Please note that nominalQuota defined at the Cohort level
represents additional resources on top of those defined by
ClusterQueues within the Cohort. The Cohort's nominalQuota
may be thought of as a shared pool for the ClusterQueues
within it. Additionally, this quota may also be lent out to
parent Cohort(s), subject to LendingLimit.</p>
<p>BorrowingLimit limits how much members of this Cohort
subtree can borrow from the parent subtree.</p>
<p>LendingLimit limits how much members of this Cohort subtree
can lend to the parent subtree.</p>
<p>Borrowing and Lending limits must only be set when the
Cohort has a parent.  Otherwise, the Cohort create/update
will be rejected by the webhook.</p>
</td>
</tr>
<tr><td><code>fairSharing</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-FairSharing"><code>FairSharing</code></a>
</td>
<td>
   <p>fairSharing defines the properties of the Cohort when
participating in FairSharing. The values are only relevant
if FairSharing is enabled in the Kueue configuration.</p>
</td>
</tr>
</tbody>
</table>

## `CohortStatus`     {#kueue-x-k8s-io-v1beta2-CohortStatus}
    

**Appears in:**

- [Cohort](#kueue-x-k8s-io-v1beta2-Cohort)


<p>CohortStatus defines the observed state of Cohort.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>fairSharing</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-FairSharingStatus"><code>FairSharingStatus</code></a>
</td>
<td>
   <p>fairSharing contains the current state for this Cohort
when participating in Fair Sharing.
The is recorded only when Fair Sharing is enabled in the Kueue configuration.</p>
</td>
</tr>
</tbody>
</table>

## `DelayedTopologyRequestState`     {#kueue-x-k8s-io-v1beta2-DelayedTopologyRequestState}
    
(Alias of `string`)

**Appears in:**

- [PodSetAssignment](#kueue-x-k8s-io-v1beta2-PodSetAssignment)


<p>DelayedTopologyRequestState indicates the state of the delayed TopologyRequest.</p>




## `EvictionUnderlyingCause`     {#kueue-x-k8s-io-v1beta2-EvictionUnderlyingCause}
    
(Alias of `string`)

**Appears in:**

- [WorkloadSchedulingStatsEviction](#kueue-x-k8s-io-v1beta2-WorkloadSchedulingStatsEviction)


<p>EvictionUnderlyingCause represents the underlying cause of a workload eviction.</p>




## `FairSharing`     {#kueue-x-k8s-io-v1beta2-FairSharing}
    

**Appears in:**

- [ClusterQueueSpec](#kueue-x-k8s-io-v1beta2-ClusterQueueSpec)

- [CohortSpec](#kueue-x-k8s-io-v1beta2-CohortSpec)

- [LocalQueueSpec](#kueue-x-k8s-io-v1beta2-LocalQueueSpec)


<p>FairSharing contains the properties of the ClusterQueue or Cohort,
when participating in FairSharing.</p>
<p>Fair Sharing is compatible with Hierarchical Cohorts (any Cohort
which has a parent) as of v0.11. Using these features together in
V0.9 and V0.10 is unsupported, and results in undefined behavior.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>weight</code><br/>
<a href="https://pkg.go.dev/k8s.io/apimachinery/pkg/api/resource#Quantity"><code>k8s.io/apimachinery/pkg/api/resource.Quantity</code></a>
</td>
<td>
   <p>weight gives a comparative advantage to this ClusterQueue
or Cohort when competing for unused resources in the
Cohort.  The share is based on the dominant resource usage
above nominal quotas for each resource, divided by the
weight.  Admission prioritizes scheduling workloads from
ClusterQueues and Cohorts with the lowest share and
preempting workloads from the ClusterQueues and Cohorts
with the highest share.  A zero weight implies infinite
share value, meaning that this Node will always be at
disadvantage against other ClusterQueues and Cohorts.
When not 0, Weight must be greater than 10^-9.</p>
</td>
</tr>
</tbody>
</table>

## `FairSharingStatus`     {#kueue-x-k8s-io-v1beta2-FairSharingStatus}
    

**Appears in:**

- [ClusterQueueStatus](#kueue-x-k8s-io-v1beta2-ClusterQueueStatus)

- [CohortStatus](#kueue-x-k8s-io-v1beta2-CohortStatus)


<p>FairSharingStatus contains the information about the current status of Fair Sharing.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>weightedShare</code> <B>[Required]</B><br/>
<code>int64</code>
</td>
<td>
   <p>weightedShare represents the maximum of the ratios of usage
above nominal quota to the lendable resources in the
Cohort, among all the resources provided by the Node, and
divided by the weight.  If zero, it means that the usage of
the Node is below the nominal quota.  If the Node has a
weight of zero and is borrowing, this will return
9223372036854775807, the maximum possible share value.</p>
</td>
</tr>
</tbody>
</table>

## `FlavorFungibility`     {#kueue-x-k8s-io-v1beta2-FlavorFungibility}
    

**Appears in:**

- [ClusterQueueSpec](#kueue-x-k8s-io-v1beta2-ClusterQueueSpec)


<p>FlavorFungibility determines whether a workload should try the next flavor
before borrowing or preempting in current flavor.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>whenCanBorrow</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-FlavorFungibilityPolicy"><code>FlavorFungibilityPolicy</code></a>
</td>
<td>
   <p>whenCanBorrow determines whether a workload should try the next flavor
before borrowing in current flavor. The possible values are:</p>
<ul>
<li><code>MayStopSearch</code> (default): stop the search for candidate flavors if workload
fits or requires borrowing to fit.</li>
<li><code>TryNextFlavor</code>: try next flavor if workload requires borrowing to fit.</li>
</ul>
</td>
</tr>
<tr><td><code>whenCanPreempt</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-FlavorFungibilityPolicy"><code>FlavorFungibilityPolicy</code></a>
</td>
<td>
   <p>whenCanPreempt determines whether a workload should try the next flavor
before preempting in current flavor. The possible values are:</p>
<ul>
<li><code>MayStopSearch</code>: stop the search for candidate flavors if workload fits or requires
preemption to fit.</li>
<li><code>TryNextFlavor</code> (default): try next flavor if workload requires preemption
to fit in current flavor.</li>
</ul>
</td>
</tr>
<tr><td><code>preference</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-FlavorFungibilityPreference"><code>FlavorFungibilityPreference</code></a>
</td>
<td>
   <p>preference guides the choosing of the flavor for admission in case all candidate flavors
require either preemption, borrowing, or both. The possible values are:</p>
<ul>
<li><code>BorrowingOverPreemption</code> (default): prefer to use borrowing rather than preemption
when such a choice is possible. More technically it minimizes the borrowing distance
in the cohort tree, and solves tie-breaks by preferring better preemption mode
(reclaim over preemption within ClusterQueue).</li>
<li><code>PreemptionOverBorrowing</code>: prefer to use preemption rather than borrowing
when such a choice is possible.  More technically it optimizes the preemption mode
(reclaim over preemption within ClusterQueue), and solves tie-breaks by minimizing
the borrowing distance in the cohort tree.</li>
</ul>
</td>
</tr>
</tbody>
</table>

## `FlavorFungibilityPolicy`     {#kueue-x-k8s-io-v1beta2-FlavorFungibilityPolicy}
    
(Alias of `string`)

**Appears in:**

- [FlavorFungibility](#kueue-x-k8s-io-v1beta2-FlavorFungibility)





## `FlavorFungibilityPreference`     {#kueue-x-k8s-io-v1beta2-FlavorFungibilityPreference}
    
(Alias of `string`)

**Appears in:**

- [FlavorFungibility](#kueue-x-k8s-io-v1beta2-FlavorFungibility)





## `FlavorQuotas`     {#kueue-x-k8s-io-v1beta2-FlavorQuotas}
    

**Appears in:**

- [ResourceGroup](#kueue-x-k8s-io-v1beta2-ResourceGroup)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta2-ResourceFlavorReference"><code>ResourceFlavorReference</code></a>
</td>
<td>
   <p>name of this flavor. The name should match the .metadata.name of a
ResourceFlavor. If a matching ResourceFlavor does not exist, the
ClusterQueue will have an Active condition set to False.</p>
</td>
</tr>
<tr><td><code>resources</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta2-ResourceQuota"><code>[]ResourceQuota</code></a>
</td>
<td>
   <p>resources is the list of quotas for this flavor per resource.
There could be up to 64 resources.</p>
</td>
</tr>
</tbody>
</table>

## `FlavorUsage`     {#kueue-x-k8s-io-v1beta2-FlavorUsage}
    

**Appears in:**

- [ClusterQueueStatus](#kueue-x-k8s-io-v1beta2-ClusterQueueStatus)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta2-ResourceFlavorReference"><code>ResourceFlavorReference</code></a>
</td>
<td>
   <p>name of the flavor.</p>
</td>
</tr>
<tr><td><code>resources</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta2-ResourceUsage"><code>[]ResourceUsage</code></a>
</td>
<td>
   <p>resources lists the quota usage for the resources in this flavor.</p>
</td>
</tr>
</tbody>
</table>

## `KubeConfig`     {#kueue-x-k8s-io-v1beta2-KubeConfig}
    

**Appears in:**

- [ClusterSource](#kueue-x-k8s-io-v1beta2-ClusterSource)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>location</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>location of the KubeConfig.</p>
<p>If LocationType is Secret then Location is the name of the secret inside the namespace in
which the kueue controller manager is running. The config should be stored in the &quot;kubeconfig&quot; key.</p>
</td>
</tr>
<tr><td><code>locationType</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-LocationType"><code>LocationType</code></a>
</td>
<td>
   <p>locationType of the KubeConfig.</p>
</td>
</tr>
</tbody>
</table>

## `LocalQueueAdmissionFairSharingStatus`     {#kueue-x-k8s-io-v1beta2-LocalQueueAdmissionFairSharingStatus}
    

**Appears in:**

- [LocalQueueFairSharingStatus](#kueue-x-k8s-io-v1beta2-LocalQueueFairSharingStatus)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>consumedResources</code> <B>[Required]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#resourcelist-v1-core"><code>k8s.io/api/core/v1.ResourceList</code></a>
</td>
<td>
   <p>consumedResources represents the aggregated usage of resources over time,
with decaying function applied.
The value is populated if usage consumption functionality is enabled in Kueue config.</p>
</td>
</tr>
<tr><td><code>lastUpdate</code> <B>[Required]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#time-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.Time</code></a>
</td>
<td>
   <p>lastUpdate is the time when share and consumed resources were updated.</p>
</td>
</tr>
</tbody>
</table>

## `LocalQueueFairSharingStatus`     {#kueue-x-k8s-io-v1beta2-LocalQueueFairSharingStatus}
    

**Appears in:**

- [LocalQueueStatus](#kueue-x-k8s-io-v1beta2-LocalQueueStatus)


<p>LocalQueueFairSharingStatus contains the information about the current status of Fair Sharing.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>weightedShare</code> <B>[Required]</B><br/>
<code>int64</code>
</td>
<td>
   <p>weightedShare represents the maximum of the ratios of usage
above nominal quota to the lendable resources in the
Cohort, among all the resources provided by the Node, and
divided by the weight.  If zero, it means that the usage of
the Node is below the nominal quota.  If the Node has a
weight of zero and is borrowing, this will return
9223372036854775807, the maximum possible share value.</p>
</td>
</tr>
<tr><td><code>admissionFairSharingStatus</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-LocalQueueAdmissionFairSharingStatus"><code>LocalQueueAdmissionFairSharingStatus</code></a>
</td>
<td>
   <p>admissionFairSharingStatus represents information relevant to the Admission Fair Sharing</p>
</td>
</tr>
</tbody>
</table>

## `LocalQueueFlavorUsage`     {#kueue-x-k8s-io-v1beta2-LocalQueueFlavorUsage}
    

**Appears in:**

- [LocalQueueStatus](#kueue-x-k8s-io-v1beta2-LocalQueueStatus)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta2-ResourceFlavorReference"><code>ResourceFlavorReference</code></a>
</td>
<td>
   <p>name of the flavor.</p>
</td>
</tr>
<tr><td><code>resources</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta2-LocalQueueResourceUsage"><code>[]LocalQueueResourceUsage</code></a>
</td>
<td>
   <p>resources lists the quota usage for the resources in this flavor.</p>
</td>
</tr>
</tbody>
</table>

## `LocalQueueName`     {#kueue-x-k8s-io-v1beta2-LocalQueueName}
    
(Alias of `string`)

**Appears in:**

- [WorkloadSpec](#kueue-x-k8s-io-v1beta2-WorkloadSpec)


<p>LocalQueueName is the name of the LocalQueue.
It must be a DNS (RFC 1123) and has the maximum length of 253 characters.</p>




## `LocalQueueResourceUsage`     {#kueue-x-k8s-io-v1beta2-LocalQueueResourceUsage}
    

**Appears in:**

- [LocalQueueFlavorUsage](#kueue-x-k8s-io-v1beta2-LocalQueueFlavorUsage)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#resourcename-v1-core"><code>k8s.io/api/core/v1.ResourceName</code></a>
</td>
<td>
   <p>name of the resource.</p>
</td>
</tr>
<tr><td><code>total</code><br/>
<a href="https://pkg.go.dev/k8s.io/apimachinery/pkg/api/resource#Quantity"><code>k8s.io/apimachinery/pkg/api/resource.Quantity</code></a>
</td>
<td>
   <p>total is the total quantity of used quota.</p>
</td>
</tr>
</tbody>
</table>

## `LocalQueueSpec`     {#kueue-x-k8s-io-v1beta2-LocalQueueSpec}
    

**Appears in:**

- [LocalQueue](#kueue-x-k8s-io-v1beta2-LocalQueue)


<p>LocalQueueSpec defines the desired state of LocalQueue</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>clusterQueue</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-ClusterQueueReference"><code>ClusterQueueReference</code></a>
</td>
<td>
   <p>clusterQueue is a reference to a clusterQueue that backs this localQueue.</p>
</td>
</tr>
<tr><td><code>stopPolicy</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-StopPolicy"><code>StopPolicy</code></a>
</td>
<td>
   <p>stopPolicy - if set to a value different from None, the LocalQueue is considered Inactive,
no new reservation being made.</p>
<p>Depending on its value, its associated workloads will:</p>
<ul>
<li>None - Workloads are admitted</li>
<li>HoldAndDrain - Admitted workloads are evicted and Reserving workloads will cancel the reservation.</li>
<li>Hold - Admitted workloads will run to completion and Reserving workloads will cancel the reservation.</li>
</ul>
</td>
</tr>
<tr><td><code>fairSharing</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-FairSharing"><code>FairSharing</code></a>
</td>
<td>
   <p>fairSharing defines the properties of the LocalQueue when
participating in AdmissionFairSharing.  The values are only relevant
if AdmissionFairSharing is enabled in the Kueue configuration.</p>
</td>
</tr>
</tbody>
</table>

## `LocalQueueStatus`     {#kueue-x-k8s-io-v1beta2-LocalQueueStatus}
    

**Appears in:**

- [LocalQueue](#kueue-x-k8s-io-v1beta2-LocalQueue)


<p>LocalQueueStatus defines the observed state of LocalQueue</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>conditions</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#condition-v1-meta"><code>[]k8s.io/apimachinery/pkg/apis/meta/v1.Condition</code></a>
</td>
<td>
   <p>conditions hold the latest available observations of the LocalQueue
current state.
conditions are limited to 16 items.</p>
</td>
</tr>
<tr><td><code>pendingWorkloads</code><br/>
<code>int32</code>
</td>
<td>
   <p>pendingWorkloads is the number of Workloads in the LocalQueue not yet admitted to a ClusterQueue</p>
</td>
</tr>
<tr><td><code>reservingWorkloads</code><br/>
<code>int32</code>
</td>
<td>
   <p>reservingWorkloads is the number of workloads in this LocalQueue
reserving quota in a ClusterQueue and that haven't finished yet.</p>
</td>
</tr>
<tr><td><code>admittedWorkloads</code><br/>
<code>int32</code>
</td>
<td>
   <p>admittedWorkloads is the number of workloads in this LocalQueue
admitted to a ClusterQueue and that haven't finished yet.</p>
</td>
</tr>
<tr><td><code>flavorsReservation</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-LocalQueueFlavorUsage"><code>[]LocalQueueFlavorUsage</code></a>
</td>
<td>
   <p>flavorsReservation are the reserved quotas, by flavor currently in use by the
workloads assigned to this LocalQueue.</p>
</td>
</tr>
<tr><td><code>flavorsUsage</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-LocalQueueFlavorUsage"><code>[]LocalQueueFlavorUsage</code></a>
</td>
<td>
   <p>flavorsUsage are the used quotas, by flavor currently in use by the
workloads assigned to this LocalQueue.</p>
</td>
</tr>
<tr><td><code>fairSharing</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-LocalQueueFairSharingStatus"><code>LocalQueueFairSharingStatus</code></a>
</td>
<td>
   <p>fairSharing contains the information about the current status of fair sharing.</p>
</td>
</tr>
</tbody>
</table>

## `LocationType`     {#kueue-x-k8s-io-v1beta2-LocationType}
    
(Alias of `string`)

**Appears in:**

- [KubeConfig](#kueue-x-k8s-io-v1beta2-KubeConfig)





## `MultiKueueClusterSpec`     {#kueue-x-k8s-io-v1beta2-MultiKueueClusterSpec}
    

**Appears in:**

- [MultiKueueCluster](#kueue-x-k8s-io-v1beta2-MultiKueueCluster)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>clusterSource</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta2-ClusterSource"><code>ClusterSource</code></a>
</td>
<td>
   <p>clusterSource is the source to connect to the cluster.</p>
</td>
</tr>
</tbody>
</table>

## `MultiKueueClusterStatus`     {#kueue-x-k8s-io-v1beta2-MultiKueueClusterStatus}
    

**Appears in:**

- [MultiKueueCluster](#kueue-x-k8s-io-v1beta2-MultiKueueCluster)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>conditions</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#condition-v1-meta"><code>[]k8s.io/apimachinery/pkg/apis/meta/v1.Condition</code></a>
</td>
<td>
   <p>conditions hold the latest available observations of the MultiKueueCluster
current state.
conditions are limited to 16 elements.</p>
</td>
</tr>
</tbody>
</table>

## `MultiKueueConfigSpec`     {#kueue-x-k8s-io-v1beta2-MultiKueueConfigSpec}
    

**Appears in:**

- [MultiKueueConfig](#kueue-x-k8s-io-v1beta2-MultiKueueConfig)


<p>MultiKueueConfigSpec defines the desired state of MultiKueueConfig</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>clusters,omitempty,omitzero</code> <B>[Required]</B><br/>
<code>[]string</code>
</td>
<td>
   <p>clusters is a list of MultiKueueClusters names where the workloads from the ClusterQueue should be distributed.</p>
</td>
</tr>
</tbody>
</table>

## `Parameter`     {#kueue-x-k8s-io-v1beta2-Parameter}
    
(Alias of `string`)

**Appears in:**

- [ProvisioningRequestConfigSpec](#kueue-x-k8s-io-v1beta2-ProvisioningRequestConfigSpec)


<p>Parameter is limited to 255 characters.</p>




## `PodSet`     {#kueue-x-k8s-io-v1beta2-PodSet}
    

**Appears in:**

- [WorkloadSpec](#kueue-x-k8s-io-v1beta2-WorkloadSpec)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-PodSetReference"><code>PodSetReference</code></a>
</td>
<td>
   <p>name is the PodSet name.</p>
</td>
</tr>
<tr><td><code>template</code> <B>[Required]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#podtemplatespec-v1-core"><code>k8s.io/api/core/v1.PodTemplateSpec</code></a>
</td>
<td>
   <p>template is the Pod template.</p>
<p>The only allowed fields in template.metadata are labels and annotations.</p>
<p>If requests are omitted for a container or initContainer,
they default to the limits if they are explicitly specified for the
container or initContainer.</p>
<p>During admission, the rules in nodeSelector and
nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution that match
the keys in the nodeLabels from the ResourceFlavors considered for this
Workload are used to filter the ResourceFlavors that can be assigned to
this podSet.</p>
</td>
</tr>
<tr><td><code>count</code><br/>
<code>int32</code>
</td>
<td>
   <p>count is the number of pods for the spec.</p>
</td>
</tr>
<tr><td><code>minCount</code><br/>
<code>int32</code>
</td>
<td>
   <p>minCount is the minimum number of pods for the spec acceptable
if the workload supports partial admission.</p>
<p>If not provided, partial admission for the current PodSet is not
enabled.</p>
<p>Only one podSet within the workload can use this.</p>
<p>This is an alpha field and requires enabling PartialAdmission feature gate.</p>
</td>
</tr>
<tr><td><code>topologyRequest</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-PodSetTopologyRequest"><code>PodSetTopologyRequest</code></a>
</td>
<td>
   <p>topologyRequest defines the topology request for the PodSet.</p>
</td>
</tr>
</tbody>
</table>

## `PodSetAssignment`     {#kueue-x-k8s-io-v1beta2-PodSetAssignment}
    

**Appears in:**

- [Admission](#kueue-x-k8s-io-v1beta2-Admission)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-PodSetReference"><code>PodSetReference</code></a>
</td>
<td>
   <p>name is the name of the podSet. It should match one of the names in .spec.podSets.</p>
</td>
</tr>
<tr><td><code>flavors</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-ResourceFlavorReference"><code>map[ResourceName]ResourceFlavorReference</code></a>
</td>
<td>
   <p>flavors are the flavors assigned to the workload for each resource.</p>
</td>
</tr>
<tr><td><code>resourceUsage</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#resourcelist-v1-core"><code>k8s.io/api/core/v1.ResourceList</code></a>
</td>
<td>
   <p>resourceUsage keeps track of the total resources all the pods in the podset need to run.</p>
<p>Beside what is provided in podSet's specs, this calculation takes into account
the LimitRange defaults and RuntimeClass overheads at the moment of admission.
This field will not change in case of quota reclaim.</p>
</td>
</tr>
<tr><td><code>count</code><br/>
<code>int32</code>
</td>
<td>
   <p>count is the number of pods taken into account at admission time.
This field will not change in case of quota reclaim.
Value could be missing for Workloads created before this field was added,
in that case spec.podSets[*].count value will be used.</p>
</td>
</tr>
<tr><td><code>topologyAssignment</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-TopologyAssignment"><code>TopologyAssignment</code></a>
</td>
<td>
   <p>topologyAssignment indicates the topology assignment divided into
topology domains corresponding to the lowest level of the topology.
The assignment specifies the number of Pods to be scheduled per topology
domain and specifies the node selectors for each topology domain, in the
following way:</p>
<ul>
<li><code>levels</code> specifies the node selector keys (same for all domains).
<ul>
<li>If the TopologySpec.Levels field contains &quot;kubernetes.io/hostname&quot; label,
topologyAssignment will contain data only for this label,
and omit higher levels in the topology.</li>
</ul>
</li>
<li><code>slices</code> specifies the node selector values and pod counts for all domains
(which may be partitioned into separate slices).
<ul>
<li>The node selector values are arranged first by topology level, only then by domain.
(This allows &quot;optimizing&quot; similar values; see below).</li>
</ul>
</li>
<li>The format of <code>slices</code> supports the following variations
(aimed to optimize the total bytesize for very large number of domains; see examples below):
<ul>
<li>When all node selector values (at a given topology level, in a given slice)
share a common prefix and/or suffix, these may be stored
in dedicated <code>prefix</code>/<code>suffix</code> fields.
If so, the array of <code>roots</code> will only store the remaining parts of these strings.</li>
<li>When all node selector values (at a given topology level, in a given slice)
are identical, this may be represented by <code>universal</code> value.</li>
<li>When all pod counts (in a given slice) are identical,
this may be represented by <code>universal</code> pod count.</li>
</ul>
</li>
</ul>
<p>Example 1:</p>
<p>The following represents an assignment in which:</p>
<ul>
<li>4 Pods are to be scheduled on nodes matching the node selector:
<ul>
<li>cloud.provider.com/topology-block: block-1</li>
<li>cloud.provider.com/topology-rack: rack-1</li>
</ul>
</li>
<li>2 Pods are to be scheduled on nodes matching the node selector:
<ul>
<li>cloud.provider.com/topology-block: block-1</li>
<li>cloud.provider.com/topology-rack: rack-2</li>
</ul>
</li>
</ul>
<p>topologyAssignment:
levels:</p>
<ul>
<li>cloud.provider.com/topology-block</li>
<li>cloud.provider.com/topology-rack
slices:</li>
<li>domainCount: 2
valuesPerLevel:
<ul>
<li>individual:
roots: [block-1, block-1]</li>
<li>individual:
roots: [rack-1, rack-2]
podCounts:
individual: [4, 2]</li>
</ul>
</li>
</ul>
<p>Example 2:</p>
<p>The following is equivalent to Example 1 - but using extracted prefix and universalValue.</p>
<p>topologyAssignment:
levels:</p>
<ul>
<li>cloud.provider.com/topology-block</li>
<li>cloud.provider.com/topology-rack
slices:</li>
<li>domainCount: 2
valuesPerLevel:
<ul>
<li>universal: block-1</li>
<li>individual:
prefix: rack-
roots: [1, 2]
podCounts:
individual: [4, 2]</li>
</ul>
</li>
</ul>
<p>Example 3:</p>
<p>Now suppose that:</p>
<ul>
<li>the Topology object defines kubernetes.io/hostname as the lowest level
(and hence, in the topologyAssignment, we omit all other levels
since the hostname label suffices to explicitly identify a proper node),</li>
<li>we assign 1 Pod per each node,</li>
<li>the node naming scheme is <code>block-{blockId}-rack-{rackId}-node-{nodeId}</code>.
Then, using the &quot;extraction of commons&quot;, the assignment from Examples 1-2 would look as follows:</li>
</ul>
<p>topologyAssignment:
levels:</p>
<ul>
<li>kubernetes.io/hostname
slices:</li>
<li>domainCount: 6
valuesPerLevel:
<ul>
<li>individual:
prefix: block-1-rack-
roots: [1-node-1, 1-node-2, 1-node-3, 1-node-4, 2-node-1, 2-node-2]
podCounts:
universal: 1</li>
</ul>
</li>
</ul>
<p>Example 4:</p>
<p>By using multiple slices, we can afford even longer common prefixes.
The assignment from Example 3 can be alternatively represented as follows:</p>
<p>topologyAssignment:
levels:</p>
<ul>
<li>kubernetes.io/hostname
slices:</li>
<li>domainCount: 4
valuesPerLevel:
<ul>
<li>individual:
prefix: block-1-rack-1-node-
roots: [1, 2, 3, 4]
podCounts:
universal: 1</li>
</ul>
</li>
<li>domainCount: 2
valuesPerLevel:
<ul>
<li>individual:
prefix: block-1-rack-2-node-
roots: [1, 2]
podCounts:
universal: 1</li>
</ul>
</li>
</ul>
</td>
</tr>
<tr><td><code>delayedTopologyRequest</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-DelayedTopologyRequestState"><code>DelayedTopologyRequestState</code></a>
</td>
<td>
   <p>delayedTopologyRequest indicates the topology assignment is delayed.
Topology assignment might be delayed in case there is ProvisioningRequest
AdmissionCheck used.
Kueue schedules the second pass of scheduling for each workload with at
least one PodSet which has delayedTopologyRequest=true and without
topologyAssignment.</p>
</td>
</tr>
</tbody>
</table>

## `PodSetReference`     {#kueue-x-k8s-io-v1beta2-PodSetReference}
    
(Alias of `string`)

**Appears in:**

- [PodSet](#kueue-x-k8s-io-v1beta2-PodSet)

- [PodSetAssignment](#kueue-x-k8s-io-v1beta2-PodSetAssignment)

- [PodSetRequest](#kueue-x-k8s-io-v1beta2-PodSetRequest)

- [PodSetUpdate](#kueue-x-k8s-io-v1beta2-PodSetUpdate)

- [ReclaimablePod](#kueue-x-k8s-io-v1beta2-ReclaimablePod)


<p>PodSetReference is the name of a PodSet.</p>




## `PodSetRequest`     {#kueue-x-k8s-io-v1beta2-PodSetRequest}
    

**Appears in:**

- [WorkloadStatus](#kueue-x-k8s-io-v1beta2-WorkloadStatus)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta2-PodSetReference"><code>PodSetReference</code></a>
</td>
<td>
   <p>name is the name of the podSet. It should match one of the names in .spec.podSets.</p>
</td>
</tr>
<tr><td><code>resources</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#resourcelist-v1-core"><code>k8s.io/api/core/v1.ResourceList</code></a>
</td>
<td>
   <p>resources is the total resources all the pods in the podset need to run.</p>
<p>Beside what is provided in podSet's specs, this value also takes into account
the LimitRange defaults and RuntimeClass overheads at the moment of consideration
and the application of resource.excludeResourcePrefixes and resource.transformations.</p>
</td>
</tr>
</tbody>
</table>

## `PodSetTopologyRequest`     {#kueue-x-k8s-io-v1beta2-PodSetTopologyRequest}
    

**Appears in:**

- [PodSet](#kueue-x-k8s-io-v1beta2-PodSet)


<p>PodSetTopologyRequest defines the topology request for a PodSet.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>required</code><br/>
<code>string</code>
</td>
<td>
   <p>required indicates the topology level required by the PodSet, as
indicated by the <code>kueue.x-k8s.io/podset-required-topology</code> PodSet
annotation.
This is limited to 63 characters.</p>
</td>
</tr>
<tr><td><code>preferred</code><br/>
<code>string</code>
</td>
<td>
   <p>preferred indicates the topology level preferred by the PodSet, as
indicated by the <code>kueue.x-k8s.io/podset-preferred-topology</code> PodSet
annotation.
This is limited to 63 characters.</p>
</td>
</tr>
<tr><td><code>unconstrained</code><br/>
<code>bool</code>
</td>
<td>
   <p>unconstrained indicates that Kueue has the freedom to schedule the PodSet within
the entire available capacity, without constraints on the compactness of the placement.
This is indicated by the <code>kueue.x-k8s.io/podset-unconstrained-topology</code> PodSet annotation.</p>
</td>
</tr>
<tr><td><code>podIndexLabel</code><br/>
<code>string</code>
</td>
<td>
   <p>podIndexLabel indicates the name of the label indexing the pods.
For example, in the context of</p>
<ul>
<li>kubernetes job this is: kubernetes.io/job-completion-index</li>
<li>JobSet: kubernetes.io/job-completion-index (inherited from Job)</li>
<li>Kubeflow: training.kubeflow.org/replica-index
This is limited to 317 characters.</li>
</ul>
</td>
</tr>
<tr><td><code>subGroupIndexLabel</code><br/>
<code>string</code>
</td>
<td>
   <p>subGroupIndexLabel indicates the name of the label indexing the instances of replicated Jobs (groups)
within a PodSet. For example, in the context of JobSet this is jobset.sigs.k8s.io/job-index.
This is limited to 317 characters.</p>
</td>
</tr>
<tr><td><code>subGroupCount</code><br/>
<code>int32</code>
</td>
<td>
   <p>subGroupCount indicates the count of replicated Jobs (groups) within a PodSet.
For example, in the context of JobSet this value is read from jobset.sigs.k8s.io/replicatedjob-replicas.</p>
</td>
</tr>
<tr><td><code>podSetGroupName</code><br/>
<code>string</code>
</td>
<td>
   <p>podSetGroupName indicates the name of the group of PodSets to which this PodSet belongs to.
PodSets with the same <code>PodSetGroupName</code> should be assigned the same ResourceFlavor</p>
</td>
</tr>
<tr><td><code>podSetSliceRequiredTopology</code><br/>
<code>string</code>
</td>
<td>
   <p>podSetSliceRequiredTopology indicates the topology level required by the PodSet slice, as
indicated by the <code>kueue.x-k8s.io/podset-slice-required-topology</code> annotation.</p>
<p>This is limited to 63</p>
</td>
</tr>
<tr><td><code>podSetSliceSize</code><br/>
<code>int32</code>
</td>
<td>
   <p>podSetSliceSize indicates the size of a subgroup of pods in a PodSet for which
Kueue finds a requested topology domain on a level defined
in <code>kueue.x-k8s.io/podset-slice-required-topology</code> annotation.</p>
</td>
</tr>
</tbody>
</table>

## `PodSetUpdate`     {#kueue-x-k8s-io-v1beta2-PodSetUpdate}
    

**Appears in:**

- [AdmissionCheckState](#kueue-x-k8s-io-v1beta2-AdmissionCheckState)


<p>PodSetUpdate contains a list of pod set modifications suggested by AdmissionChecks.
The modifications should be additive only - modifications of already existing keys
or having the same key provided by multiple AdmissionChecks is not allowed and will
result in failure during workload admission.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta2-PodSetReference"><code>PodSetReference</code></a>
</td>
<td>
   <p>name of the PodSet to modify. Should match to one of the Workload's PodSets.</p>
</td>
</tr>
<tr><td><code>labels</code><br/>
<code>map[string]string</code>
</td>
<td>
   <p>labels of the PodSet to modify.</p>
</td>
</tr>
<tr><td><code>annotations</code><br/>
<code>map[string]string</code>
</td>
<td>
   <p>annotations of the PodSet to modify.</p>
</td>
</tr>
<tr><td><code>nodeSelector</code><br/>
<code>map[string]string</code>
</td>
<td>
   <p>nodeSelector of the PodSet to modify.</p>
</td>
</tr>
<tr><td><code>tolerations</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#toleration-v1-core"><code>[]k8s.io/api/core/v1.Toleration</code></a>
</td>
<td>
   <p>tolerations of the PodSet to modify.</p>
</td>
</tr>
</tbody>
</table>

## `PreemptionPolicy`     {#kueue-x-k8s-io-v1beta2-PreemptionPolicy}
    
(Alias of `string`)

**Appears in:**

- [ClusterQueuePreemption](#kueue-x-k8s-io-v1beta2-ClusterQueuePreemption)





## `PriorityClassGroup`     {#kueue-x-k8s-io-v1beta2-PriorityClassGroup}
    
(Alias of `string`)

**Appears in:**

- [PriorityClassRef](#kueue-x-k8s-io-v1beta2-PriorityClassRef)


<p>PriorityClassGroup indicates the API group of the PriorityClass object.</p>




## `PriorityClassKind`     {#kueue-x-k8s-io-v1beta2-PriorityClassKind}
    
(Alias of `string`)

**Appears in:**

- [PriorityClassRef](#kueue-x-k8s-io-v1beta2-PriorityClassRef)


<p>PriorityClassKind is the kind of the PriorityClass object.</p>




## `PriorityClassRef`     {#kueue-x-k8s-io-v1beta2-PriorityClassRef}
    

**Appears in:**

- [WorkloadSpec](#kueue-x-k8s-io-v1beta2-WorkloadSpec)


<p>PriorityClassRef references a PriorityClass in a specific API group.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>group</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta2-PriorityClassGroup"><code>PriorityClassGroup</code></a>
</td>
<td>
   <p>group is the API group of the PriorityClass object.
Use &quot;kueue.x-k8s.io&quot; for WorkloadPriorityClass.
Use &quot;scheduling.k8s.io&quot; for Pod PriorityClass.</p>
</td>
</tr>
<tr><td><code>kind</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta2-PriorityClassKind"><code>PriorityClassKind</code></a>
</td>
<td>
   <p>kind is the kind of the PriorityClass object.</p>
</td>
</tr>
<tr><td><code>name</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>name is the name of the PriorityClass the Workload is associated with.
If specified, indicates the workload's priority.
&quot;system-node-critical&quot; and &quot;system-cluster-critical&quot; are two special
keywords which indicate the highest priorities with the former being
the highest priority. Any other name must be defined by creating a
PriorityClass object with that name. If not specified, the workload
priority will be default or zero if there is no default.</p>
</td>
</tr>
</tbody>
</table>

## `ProvisioningRequestConfigPodSetMergePolicy`     {#kueue-x-k8s-io-v1beta2-ProvisioningRequestConfigPodSetMergePolicy}
    
(Alias of `string`)

**Appears in:**

- [ProvisioningRequestConfigSpec](#kueue-x-k8s-io-v1beta2-ProvisioningRequestConfigSpec)





## `ProvisioningRequestConfigSpec`     {#kueue-x-k8s-io-v1beta2-ProvisioningRequestConfigSpec}
    

**Appears in:**

- [ProvisioningRequestConfig](#kueue-x-k8s-io-v1beta2-ProvisioningRequestConfig)


<p>ProvisioningRequestConfigSpec defines the desired state of ProvisioningRequestConfig</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>provisioningClassName</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>provisioningClassName describes the different modes of provisioning the resources.
Check autoscaling.x-k8s.io ProvisioningRequestSpec.ProvisioningClassName for details.</p>
</td>
</tr>
<tr><td><code>parameters</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-Parameter"><code>map[string]Parameter</code></a>
</td>
<td>
   <p>parameters contains all other parameters classes may require.</p>
</td>
</tr>
<tr><td><code>managedResources</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#resourcename-v1-core"><code>[]k8s.io/api/core/v1.ResourceName</code></a>
</td>
<td>
   <p>managedResources contains the list of resources managed by the autoscaling.</p>
<p>If empty, all resources are considered managed.</p>
<p>If not empty, the ProvisioningRequest will contain only the podsets that are
requesting at least one of them.</p>
<p>If none of the workloads podsets is requesting at least a managed resource,
the workload is considered ready.</p>
</td>
</tr>
<tr><td><code>retryStrategy</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-ProvisioningRequestRetryStrategy"><code>ProvisioningRequestRetryStrategy</code></a>
</td>
<td>
   <p>retryStrategy defines strategy for retrying ProvisioningRequest.
If null, then the default configuration is applied with the following parameter values:
backoffLimitCount:  3
backoffBaseSeconds: 60 - 1 min
backoffMaxSeconds:  1800 - 30 mins</p>
<p>To switch off retry mechanism
set retryStrategy.backoffLimitCount to 0.</p>
</td>
</tr>
<tr><td><code>podSetUpdates</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-ProvisioningRequestPodSetUpdates"><code>ProvisioningRequestPodSetUpdates</code></a>
</td>
<td>
   <p>podSetUpdates specifies the update of the workload's PodSetUpdates which
are used to target the provisioned nodes.</p>
</td>
</tr>
<tr><td><code>podSetMergePolicy</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-ProvisioningRequestConfigPodSetMergePolicy"><code>ProvisioningRequestConfigPodSetMergePolicy</code></a>
</td>
<td>
   <p>podSetMergePolicy specifies the policy for merging PodSets before being passed
to the cluster autoscaler.</p>
</td>
</tr>
</tbody>
</table>

## `ProvisioningRequestPodSetUpdates`     {#kueue-x-k8s-io-v1beta2-ProvisioningRequestPodSetUpdates}
    

**Appears in:**

- [ProvisioningRequestConfigSpec](#kueue-x-k8s-io-v1beta2-ProvisioningRequestConfigSpec)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>nodeSelector</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-ProvisioningRequestPodSetUpdatesNodeSelector"><code>[]ProvisioningRequestPodSetUpdatesNodeSelector</code></a>
</td>
<td>
   <p>nodeSelector specifies the list of updates for the NodeSelector.</p>
</td>
</tr>
</tbody>
</table>

## `ProvisioningRequestPodSetUpdatesNodeSelector`     {#kueue-x-k8s-io-v1beta2-ProvisioningRequestPodSetUpdatesNodeSelector}
    

**Appears in:**

- [ProvisioningRequestPodSetUpdates](#kueue-x-k8s-io-v1beta2-ProvisioningRequestPodSetUpdates)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>key</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>key specifies the key for the NodeSelector.</p>
</td>
</tr>
<tr><td><code>valueFromProvisioningClassDetail</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>valueFromProvisioningClassDetail specifies the key of the
ProvisioningRequest.status.provisioningClassDetails from which the value
is used for the update.</p>
</td>
</tr>
</tbody>
</table>

## `ProvisioningRequestRetryStrategy`     {#kueue-x-k8s-io-v1beta2-ProvisioningRequestRetryStrategy}
    

**Appears in:**

- [ProvisioningRequestConfigSpec](#kueue-x-k8s-io-v1beta2-ProvisioningRequestConfigSpec)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>backoffLimitCount</code><br/>
<code>int32</code>
</td>
<td>
   <p>backoffLimitCount defines the maximum number of re-queuing retries.
Once the number is reached, the workload is deactivated (<code>.spec.activate</code>=<code>false</code>).</p>
<p>Every backoff duration is about &quot;b*2^(n-1)+Rand&quot; where:</p>
<ul>
<li>&quot;b&quot; represents the base set by &quot;BackoffBaseSeconds&quot; parameter,</li>
<li>&quot;n&quot; represents the &quot;workloadStatus.requeueState.count&quot;,</li>
<li>&quot;Rand&quot; represents the random jitter.
During this time, the workload is taken as an inadmissible and
other workloads will have a chance to be admitted.
By default, the consecutive requeue delays are around: (60s, 120s, 240s, ...).</li>
</ul>
<p>Defaults to 3.</p>
</td>
</tr>
<tr><td><code>backoffBaseSeconds</code><br/>
<code>int32</code>
</td>
<td>
   <p>backoffBaseSeconds defines the base for the exponential backoff for
re-queuing an evicted workload.</p>
<p>Defaults to 60.</p>
</td>
</tr>
<tr><td><code>backoffMaxSeconds</code><br/>
<code>int32</code>
</td>
<td>
   <p>backoffMaxSeconds defines the maximum backoff time to re-queue an evicted workload.</p>
<p>Defaults to 1800.</p>
</td>
</tr>
</tbody>
</table>

## `QueueingStrategy`     {#kueue-x-k8s-io-v1beta2-QueueingStrategy}
    
(Alias of `string`)

**Appears in:**

- [ClusterQueueSpec](#kueue-x-k8s-io-v1beta2-ClusterQueueSpec)





## `ReclaimablePod`     {#kueue-x-k8s-io-v1beta2-ReclaimablePod}
    

**Appears in:**

- [WorkloadStatus](#kueue-x-k8s-io-v1beta2-WorkloadStatus)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta2-PodSetReference"><code>PodSetReference</code></a>
</td>
<td>
   <p>name is the PodSet name.</p>
</td>
</tr>
<tr><td><code>count</code> <B>[Required]</B><br/>
<code>int32</code>
</td>
<td>
   <p>count is the number of pods for which the requested resources are no longer needed.</p>
</td>
</tr>
</tbody>
</table>

## `RequeueState`     {#kueue-x-k8s-io-v1beta2-RequeueState}
    

**Appears in:**

- [WorkloadStatus](#kueue-x-k8s-io-v1beta2-WorkloadStatus)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>count</code><br/>
<code>int32</code>
</td>
<td>
   <p>count records the number of times a workload has been re-queued
When a deactivated (<code>.spec.activate</code>=<code>false</code>) workload is reactivated (<code>.spec.activate</code>=<code>true</code>),
this count would be reset to null.</p>
</td>
</tr>
<tr><td><code>requeueAt</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#time-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.Time</code></a>
</td>
<td>
   <p>requeueAt records the time when a workload will be re-queued.
When a deactivated (<code>.spec.activate</code>=<code>false</code>) workload is reactivated (<code>.spec.activate</code>=<code>true</code>),
this time would be reset to null.</p>
</td>
</tr>
</tbody>
</table>

## `ResourceFlavorReference`     {#kueue-x-k8s-io-v1beta2-ResourceFlavorReference}
    
(Alias of `string`)

**Appears in:**

- [AdmissionCheckStrategyRule](#kueue-x-k8s-io-v1beta2-AdmissionCheckStrategyRule)

- [FlavorQuotas](#kueue-x-k8s-io-v1beta2-FlavorQuotas)

- [FlavorUsage](#kueue-x-k8s-io-v1beta2-FlavorUsage)

- [LocalQueueFlavorUsage](#kueue-x-k8s-io-v1beta2-LocalQueueFlavorUsage)

- [PodSetAssignment](#kueue-x-k8s-io-v1beta2-PodSetAssignment)


<p>ResourceFlavorReference is the name of the ResourceFlavor.</p>




## `ResourceFlavorSpec`     {#kueue-x-k8s-io-v1beta2-ResourceFlavorSpec}
    

**Appears in:**

- [ResourceFlavor](#kueue-x-k8s-io-v1beta2-ResourceFlavor)


<p>ResourceFlavorSpec defines the desired state of the ResourceFlavor</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>nodeLabels</code><br/>
<code>map[string]string</code>
</td>
<td>
   <p>nodeLabels are labels that associate the ResourceFlavor with Nodes that
have the same labels.
When a Workload is admitted, its podsets can only get assigned
ResourceFlavors whose nodeLabels match the nodeSelector and nodeAffinity
fields.
Once a ResourceFlavor is assigned to a podSet, the ResourceFlavor's
nodeLabels should be injected into the pods of the Workload by the
controller that integrates with the Workload object.</p>
<p>nodeLabels can be up to 8 elements.</p>
</td>
</tr>
<tr><td><code>nodeTaints</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#taint-v1-core"><code>[]k8s.io/api/core/v1.Taint</code></a>
</td>
<td>
   <p>nodeTaints are taints that the nodes associated with this ResourceFlavor
have.
Workloads' podsets must have tolerations for these nodeTaints in order to
get assigned this ResourceFlavor during admission.
When this ResourceFlavor has also set the matching tolerations (in .spec.tolerations),
then the nodeTaints are not considered during admission.
Only the 'NoSchedule' and 'NoExecute' taint effects are evaluated,
while 'PreferNoSchedule' is ignored.</p>
<p>An example of a nodeTaint is
cloud.provider.com/preemptible=&quot;true&quot;:NoSchedule</p>
<p>nodeTaints can be up to 8 elements.</p>
</td>
</tr>
<tr><td><code>tolerations</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#toleration-v1-core"><code>[]k8s.io/api/core/v1.Toleration</code></a>
</td>
<td>
   <p>tolerations are extra tolerations that will be added to the pods admitted in
the quota associated with this resource flavor.</p>
<p>An example of a toleration is
cloud.provider.com/preemptible=&quot;true&quot;:NoSchedule</p>
<p>tolerations can be up to 8 elements.</p>
</td>
</tr>
<tr><td><code>topologyName</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-TopologyReference"><code>TopologyReference</code></a>
</td>
<td>
   <p>topologyName indicates topology for the TAS ResourceFlavor.
When specified, it enables scraping of the topology information from the
nodes matching to the Resource Flavor node labels.</p>
</td>
</tr>
</tbody>
</table>

## `ResourceGroup`     {#kueue-x-k8s-io-v1beta2-ResourceGroup}
    

**Appears in:**

- [ClusterQueueSpec](#kueue-x-k8s-io-v1beta2-ClusterQueueSpec)

- [CohortSpec](#kueue-x-k8s-io-v1beta2-CohortSpec)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>coveredResources</code> <B>[Required]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#resourcename-v1-core"><code>[]k8s.io/api/core/v1.ResourceName</code></a>
</td>
<td>
   <p>coveredResources is the list of resources covered by the flavors in this
group.
Examples: cpu, memory, vendor.com/gpu.
The list cannot be empty and it can contain up to 64 resources. With a total
of up to 256 covered resources across all resource groups in the ClusterQueue.</p>
</td>
</tr>
<tr><td><code>flavors</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta2-FlavorQuotas"><code>[]FlavorQuotas</code></a>
</td>
<td>
   <p>flavors is the list of flavors that provide the resources of this group.
Typically, different flavors represent different hardware models
(e.g., gpu models, cpu architectures) or pricing models (on-demand vs spot
cpus).
Each flavor MUST list all the resources listed for this group in the same
order as the .resources field.
The list cannot be empty and it can contain up to 64 flavors, with a max of
256 total flavors across all resource groups in the ClusterQueue.</p>
</td>
</tr>
</tbody>
</table>

## `ResourceQuota`     {#kueue-x-k8s-io-v1beta2-ResourceQuota}
    

**Appears in:**

- [FlavorQuotas](#kueue-x-k8s-io-v1beta2-FlavorQuotas)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#resourcename-v1-core"><code>k8s.io/api/core/v1.ResourceName</code></a>
</td>
<td>
   <p>name of this resource.</p>
</td>
</tr>
<tr><td><code>nominalQuota</code> <B>[Required]</B><br/>
<a href="https://pkg.go.dev/k8s.io/apimachinery/pkg/api/resource#Quantity"><code>k8s.io/apimachinery/pkg/api/resource.Quantity</code></a>
</td>
<td>
   <p>nominalQuota is the quantity of this resource that is available for
Workloads admitted by this ClusterQueue at a point in time.
The nominalQuota must be non-negative.
nominalQuota should represent the resources in the cluster available for
running jobs (after discounting resources consumed by system components
and pods not managed by kueue). In an autoscaled cluster, nominalQuota
should account for resources that can be provided by a component such as
Kubernetes cluster-autoscaler.</p>
<p>If the ClusterQueue belongs to a cohort, the sum of the quotas for each
(flavor, resource) combination defines the maximum quantity that can be
allocated by a ClusterQueue in the cohort.</p>
</td>
</tr>
<tr><td><code>borrowingLimit</code><br/>
<a href="https://pkg.go.dev/k8s.io/apimachinery/pkg/api/resource#Quantity"><code>k8s.io/apimachinery/pkg/api/resource.Quantity</code></a>
</td>
<td>
   <p>borrowingLimit is the maximum amount of quota for the [flavor, resource]
combination that this ClusterQueue is allowed to borrow from the unused
quota of other ClusterQueues in the same cohort.
In total, at a given time, Workloads in a ClusterQueue can consume a
quantity of quota equal to nominalQuota+borrowingLimit, assuming the other
ClusterQueues in the cohort have enough unused quota.
If null, it means that there is no borrowing limit.
If not null, it must be non-negative.
borrowingLimit must be null if spec.cohortName is empty.</p>
</td>
</tr>
<tr><td><code>lendingLimit</code><br/>
<a href="https://pkg.go.dev/k8s.io/apimachinery/pkg/api/resource#Quantity"><code>k8s.io/apimachinery/pkg/api/resource.Quantity</code></a>
</td>
<td>
   <p>lendingLimit is the maximum amount of unused quota for the [flavor, resource]
combination that this ClusterQueue can lend to other ClusterQueues in the same cohort.
In total, at a given time, ClusterQueue reserves for its exclusive use
a quantity of quota equals to nominalQuota - lendingLimit.
If null, it means that there is no lending limit, meaning that
all the nominalQuota can be borrowed by other clusterQueues in the cohort.
If not null, it must be non-negative.
lendingLimit must be null if spec.cohortName is empty.
This field is in beta stage and is enabled by default.</p>
</td>
</tr>
</tbody>
</table>

## `ResourceUsage`     {#kueue-x-k8s-io-v1beta2-ResourceUsage}
    

**Appears in:**

- [FlavorUsage](#kueue-x-k8s-io-v1beta2-FlavorUsage)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#resourcename-v1-core"><code>k8s.io/api/core/v1.ResourceName</code></a>
</td>
<td>
   <p>name of the resource</p>
</td>
</tr>
<tr><td><code>total</code><br/>
<a href="https://pkg.go.dev/k8s.io/apimachinery/pkg/api/resource#Quantity"><code>k8s.io/apimachinery/pkg/api/resource.Quantity</code></a>
</td>
<td>
   <p>total is the total quantity of used quota, including the amount borrowed
from the cohort.</p>
</td>
</tr>
<tr><td><code>borrowed</code><br/>
<a href="https://pkg.go.dev/k8s.io/apimachinery/pkg/api/resource#Quantity"><code>k8s.io/apimachinery/pkg/api/resource.Quantity</code></a>
</td>
<td>
   <p>borrowed is quantity of quota that is borrowed from the cohort. In other
words, it's the used quota that is over the nominalQuota.</p>
</td>
</tr>
</tbody>
</table>

## `SchedulingStats`     {#kueue-x-k8s-io-v1beta2-SchedulingStats}
    

**Appears in:**

- [WorkloadStatus](#kueue-x-k8s-io-v1beta2-WorkloadStatus)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>evictions</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-WorkloadSchedulingStatsEviction"><code>[]WorkloadSchedulingStatsEviction</code></a>
</td>
<td>
   <p>evictions tracks eviction statistics by reason and underlyingCause.</p>
</td>
</tr>
</tbody>
</table>

## `StopPolicy`     {#kueue-x-k8s-io-v1beta2-StopPolicy}
    
(Alias of `string`)

**Appears in:**

- [ClusterQueueSpec](#kueue-x-k8s-io-v1beta2-ClusterQueueSpec)

- [LocalQueueSpec](#kueue-x-k8s-io-v1beta2-LocalQueueSpec)





## `TopologyAssignment`     {#kueue-x-k8s-io-v1beta2-TopologyAssignment}
    

**Appears in:**

- [PodSetAssignment](#kueue-x-k8s-io-v1beta2-PodSetAssignment)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>levels</code> <B>[Required]</B><br/>
<code>[]string</code>
</td>
<td>
   <p>levels is an ordered list of keys denoting the levels of the assigned
topology (i.e. node label keys), from the highest to the lowest level of
the topology.</p>
</td>
</tr>
<tr><td><code>slices</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta2-TopologyAssignmentSlice"><code>[]TopologyAssignmentSlice</code></a>
</td>
<td>
   <p>slices represent topology assignments for subsets of pods of a workload.
The full assignment is obtained as a union of all slices.</p>
</td>
</tr>
</tbody>
</table>

## `TopologyAssignmentSlice`     {#kueue-x-k8s-io-v1beta2-TopologyAssignmentSlice}
    

**Appears in:**

- [TopologyAssignment](#kueue-x-k8s-io-v1beta2-TopologyAssignment)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>domainCount</code> <B>[Required]</B><br/>
<code>int32</code>
</td>
<td>
   <p>domainCount is the number of domains covered by this slice.</p>
</td>
</tr>
<tr><td><code>valuesPerLevel</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta2-TopologyAssignmentSliceLevelValues"><code>[]TopologyAssignmentSliceLevelValues</code></a>
</td>
<td>
   <p>valuesPerLevel has one entry for each of the Levels specified in the TopologyAssignment.
The entry corresponding to a particular level specifies the placement of pods at that level.</p>
</td>
</tr>
<tr><td><code>podCounts</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta2-TopologyAssignmentSlicePodCounts"><code>TopologyAssignmentSlicePodCounts</code></a>
</td>
<td>
   <p>podCounts specifies the number of pods allocated per each domain.</p>
</td>
</tr>
</tbody>
</table>

## `TopologyAssignmentSliceLevelIndividualValues`     {#kueue-x-k8s-io-v1beta2-TopologyAssignmentSliceLevelIndividualValues}
    

**Appears in:**

- [TopologyAssignmentSliceLevelValues](#kueue-x-k8s-io-v1beta2-TopologyAssignmentSliceLevelValues)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>prefix</code><br/>
<code>string</code>
</td>
<td>
   <p>prefix specifies a common prefix for all values in this slice assignment.
It must be either nil pointer or a non-empty string.</p>
</td>
</tr>
<tr><td><code>suffix</code><br/>
<code>string</code>
</td>
<td>
   <p>suffix specifies a common suffix for all values in this slice assignment.
It must be either nil pointer or a non-empty string.</p>
</td>
</tr>
<tr><td><code>roots</code> <B>[Required]</B><br/>
<code>[]string</code>
</td>
<td>
   <p>roots specifies the values in this assignment (excluding prefix and suffix, if non-empty).
Its length must be equal to the &quot;domainCount&quot; field of the TopologyAssignmentSlice.</p>
</td>
</tr>
</tbody>
</table>

## `TopologyAssignmentSliceLevelValues`     {#kueue-x-k8s-io-v1beta2-TopologyAssignmentSliceLevelValues}
    

**Appears in:**

- [TopologyAssignmentSlice](#kueue-x-k8s-io-v1beta2-TopologyAssignmentSlice)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>universal</code><br/>
<code>string</code>
</td>
<td>
   <p>universal - if set - specifies a single topology placement value (at a particular topology level)
that applies to all pods in the current TopologyAssignmentSlice.
Exactly one of universal, individual must be set.</p>
</td>
</tr>
<tr><td><code>individual</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-TopologyAssignmentSliceLevelIndividualValues"><code>TopologyAssignmentSliceLevelIndividualValues</code></a>
</td>
<td>
   <p>individual - if set - specifies multiple topology placement values (at a particular topology level)
that apply to the pods in the current TopologyAssignmentSlice.
Exactly one of universal, individual must be set.</p>
</td>
</tr>
</tbody>
</table>

## `TopologyAssignmentSlicePodCounts`     {#kueue-x-k8s-io-v1beta2-TopologyAssignmentSlicePodCounts}
    

**Appears in:**

- [TopologyAssignmentSlice](#kueue-x-k8s-io-v1beta2-TopologyAssignmentSlice)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>universal</code><br/>
<code>int32</code>
</td>
<td>
   <p>universal - if set - specifies the number of pods allocated in every domain in this slice.
Exactly one of universal, individual must be set.</p>
</td>
</tr>
<tr><td><code>individual</code><br/>
<code>[]int32</code>
</td>
<td>
   <p>individual - if set - specifies the number of pods allocated in each domain in this slice.
If set, its length must be equal to the &quot;domainCount&quot; field of the TopologyAssignmentSlice.
Exactly one of universal, individual must be set.</p>
</td>
</tr>
</tbody>
</table>

## `TopologyLevel`     {#kueue-x-k8s-io-v1beta2-TopologyLevel}
    

**Appears in:**

- [TopologySpec](#kueue-x-k8s-io-v1beta2-TopologySpec)


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

## `TopologyReference`     {#kueue-x-k8s-io-v1beta2-TopologyReference}
    
(Alias of `string`)

**Appears in:**

- [ResourceFlavorSpec](#kueue-x-k8s-io-v1beta2-ResourceFlavorSpec)



<p>TopologyReference is the name of the Topology.</p>




## `TopologySpec`     {#kueue-x-k8s-io-v1beta2-TopologySpec}
    

**Appears in:**

- [Topology](#kueue-x-k8s-io-v1beta2-Topology)


<p>TopologySpec defines the desired state of Topology</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>levels</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta2-TopologyLevel"><code>[]TopologyLevel</code></a>
</td>
<td>
   <p>levels define the levels of topology.</p>
</td>
</tr>
</tbody>
</table>

## `UnhealthyNode`     {#kueue-x-k8s-io-v1beta2-UnhealthyNode}
    

**Appears in:**

- [WorkloadStatus](#kueue-x-k8s-io-v1beta2-WorkloadStatus)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>name is the name of the unhealthy node.</p>
</td>
</tr>
</tbody>
</table>

## `WorkloadSchedulingStatsEviction`     {#kueue-x-k8s-io-v1beta2-WorkloadSchedulingStatsEviction}
    

**Appears in:**

- [SchedulingStats](#kueue-x-k8s-io-v1beta2-SchedulingStats)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>reason</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>reason specifies the programmatic identifier for the eviction cause.</p>
</td>
</tr>
<tr><td><code>underlyingCause</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta2-EvictionUnderlyingCause"><code>EvictionUnderlyingCause</code></a>
</td>
<td>
   <p>underlyingCause specifies a finer-grained explanation that complements the eviction reason.
This may be an empty string.</p>
</td>
</tr>
<tr><td><code>count</code> <B>[Required]</B><br/>
<code>int32</code>
</td>
<td>
   <p>count tracks the number of evictions for this reason and detailed reason.</p>
</td>
</tr>
</tbody>
</table>

## `WorkloadSpec`     {#kueue-x-k8s-io-v1beta2-WorkloadSpec}
    

**Appears in:**

- [Workload](#kueue-x-k8s-io-v1beta2-Workload)


<p>WorkloadSpec defines the desired state of Workload</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>podSets</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-PodSet"><code>[]PodSet</code></a>
</td>
<td>
   <p>podSets is a list of sets of homogeneous pods, each described by a Pod spec
and a count.
There must be at least one element and at most 8.
podSets cannot be changed.</p>
</td>
</tr>
<tr><td><code>queueName</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-LocalQueueName"><code>LocalQueueName</code></a>
</td>
<td>
   <p>queueName is the name of the LocalQueue the Workload is associated with.
queueName cannot be changed while .status.admission is not null.</p>
</td>
</tr>
<tr><td><code>priorityClassRef</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-PriorityClassRef"><code>PriorityClassRef</code></a>
</td>
<td>
   <p>priorityClassRef references a PriorityClass object that defines the workload's priority.</p>
</td>
</tr>
<tr><td><code>priority</code><br/>
<code>int32</code>
</td>
<td>
   <p>priority determines the order of access to the resources managed by the
ClusterQueue where the workload is queued.
The priority value is populated from the referenced PriorityClass (via priorityClassRef).
The higher the value, the higher the priority.
If priorityClassRef is specified, priority must not be null.</p>
</td>
</tr>
<tr><td><code>active</code> <B>[Required]</B><br/>
<code>bool</code>
</td>
<td>
   <p>active determines if a workload can be admitted into a queue.
Changing active from true to false will evict any running workloads.
Possible values are:</p>
<ul>
<li>false: indicates that a workload should never be admitted and evicts running workloads</li>
<li>true: indicates that a workload can be evaluated for admission into it's respective queue.</li>
</ul>
<p>Defaults to true</p>
</td>
</tr>
<tr><td><code>maximumExecutionTimeSeconds</code><br/>
<code>int32</code>
</td>
<td>
   <p>maximumExecutionTimeSeconds if provided, determines the maximum time, in seconds,
the workload can be admitted before it's automatically deactivated.</p>
<p>If unspecified, no execution time limit is enforced on the Workload.</p>
</td>
</tr>
</tbody>
</table>

## `WorkloadStatus`     {#kueue-x-k8s-io-v1beta2-WorkloadStatus}
    

**Appears in:**

- [Workload](#kueue-x-k8s-io-v1beta2-Workload)


<p>WorkloadStatus defines the observed state of Workload</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>conditions</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#condition-v1-meta"><code>[]k8s.io/apimachinery/pkg/apis/meta/v1.Condition</code></a>
</td>
<td>
   <p>conditions hold the latest available observations of the Workload
current state.</p>
<p>The type of the condition could be:</p>
<ul>
<li>Admitted: the Workload was admitted through a ClusterQueue.</li>
<li>Finished: the associated workload finished running (failed or succeeded).</li>
<li>PodsReady: at least <code>.spec.podSets[*].count</code> Pods are ready or have
succeeded.
conditions are limited to 16 items.</li>
</ul>
</td>
</tr>
<tr><td><code>admission</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-Admission"><code>Admission</code></a>
</td>
<td>
   <p>admission holds the parameters of the admission of the workload by a
ClusterQueue. admission can be set back to null, but its fields cannot be
changed once set.</p>
</td>
</tr>
<tr><td><code>requeueState</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-RequeueState"><code>RequeueState</code></a>
</td>
<td>
   <p>requeueState holds the re-queue state
when a workload meets Eviction with PodsReadyTimeout reason.</p>
</td>
</tr>
<tr><td><code>reclaimablePods</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-ReclaimablePod"><code>[]ReclaimablePod</code></a>
</td>
<td>
   <p>reclaimablePods keeps track of the number pods within a podset for which
the resource reservation is no longer needed.</p>
</td>
</tr>
<tr><td><code>admissionChecks</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-AdmissionCheckState"><code>[]AdmissionCheckState</code></a>
</td>
<td>
   <p>admissionChecks list all the admission checks required by the workload and the current status</p>
</td>
</tr>
<tr><td><code>resourceRequests</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-PodSetRequest"><code>[]PodSetRequest</code></a>
</td>
<td>
   <p>resourceRequests provides a detailed view of the resources that were
requested by a non-admitted workload when it was considered for admission.
If admission is non-null, resourceRequests will be empty because
admission.resourceUsage contains the detailed information.</p>
</td>
</tr>
<tr><td><code>accumulatedPastExecutionTimeSeconds</code><br/>
<code>int32</code>
</td>
<td>
   <p>accumulatedPastExecutionTimeSeconds holds the total time, in seconds, the workload spent
in Admitted state, in the previous <code>Admit</code> - <code>Evict</code> cycles.</p>
</td>
</tr>
<tr><td><code>schedulingStats</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-SchedulingStats"><code>SchedulingStats</code></a>
</td>
<td>
   <p>schedulingStats tracks scheduling statistics</p>
</td>
</tr>
<tr><td><code>nominatedClusterNames</code><br/>
<code>[]string</code>
</td>
<td>
   <p>nominatedClusterNames specifies the list of cluster names that have been nominated for scheduling.
This field is mutually exclusive with the <code>.status.clusterName</code> field, and is reset when
<code>status.clusterName</code> is set.
This field is optional.</p>
</td>
</tr>
<tr><td><code>clusterName</code><br/>
<code>string</code>
</td>
<td>
   <p>clusterName is the name of the cluster where the workload is currently assigned.</p>
<p>With ElasticJobs, this field may also indicate the cluster where the original (old) workload
was assigned, providing placement context for new scaled-up workloads. This supports
affinity or propagation policies across workload slices.</p>
<p>This field is reset after the Workload is evicted.</p>
</td>
</tr>
<tr><td><code>unhealthyNodes</code><br/>
<a href="#kueue-x-k8s-io-v1beta2-UnhealthyNode"><code>[]UnhealthyNode</code></a>
</td>
<td>
   <p>unhealthyNodes holds the failed nodes running at least one pod of this workload
when Topology-Aware Scheduling is used. This field should not be set by the users.
It indicates Kueue's scheduler is searching for replacements of the failed nodes.
Requires enabling the TASFailedNodeReplacement feature gate.</p>
</td>
</tr>
</tbody>
</table>
  