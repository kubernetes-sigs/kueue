---
title: Kueue API
content_type: tool-reference
package: kueue.x-k8s.io/v1beta1
auto_generated: true
description: Generated API reference documentation for kueue.x-k8s.io/v1beta1.
---


## Resource Types 


- [AdmissionCheck](#kueue-x-k8s-io-v1beta1-AdmissionCheck)
- [ClusterQueue](#kueue-x-k8s-io-v1beta1-ClusterQueue)
- [LocalQueue](#kueue-x-k8s-io-v1beta1-LocalQueue)
- [ProvisioningRequestConfig](#kueue-x-k8s-io-v1beta1-ProvisioningRequestConfig)
- [ResourceFlavor](#kueue-x-k8s-io-v1beta1-ResourceFlavor)
- [Workload](#kueue-x-k8s-io-v1beta1-Workload)
- [WorkloadPriorityClass](#kueue-x-k8s-io-v1beta1-WorkloadPriorityClass)
  

## `AdmissionCheck`     {#kueue-x-k8s-io-v1beta1-AdmissionCheck}
    

**Appears in:**



<p>AdmissionCheck is the Schema for the admissionchecks API</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
<tr><td><code>apiVersion</code><br/>string</td><td><code>kueue.x-k8s.io/v1beta1</code></td></tr>
<tr><td><code>kind</code><br/>string</td><td><code>AdmissionCheck</code></td></tr>
    
  
<tr><td><code>spec</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-AdmissionCheckSpec"><code>AdmissionCheckSpec</code></a>
</td>
<td>
   <span class="text-muted">No description provided.</span></td>
</tr>
<tr><td><code>status</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-AdmissionCheckStatus"><code>AdmissionCheckStatus</code></a>
</td>
<td>
   <span class="text-muted">No description provided.</span></td>
</tr>
</tbody>
</table>

## `ClusterQueue`     {#kueue-x-k8s-io-v1beta1-ClusterQueue}
    

**Appears in:**



<p>ClusterQueue is the Schema for the clusterQueue API.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
<tr><td><code>apiVersion</code><br/>string</td><td><code>kueue.x-k8s.io/v1beta1</code></td></tr>
<tr><td><code>kind</code><br/>string</td><td><code>ClusterQueue</code></td></tr>
    
  
<tr><td><code>spec</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-ClusterQueueSpec"><code>ClusterQueueSpec</code></a>
</td>
<td>
   <span class="text-muted">No description provided.</span></td>
</tr>
<tr><td><code>status</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-ClusterQueueStatus"><code>ClusterQueueStatus</code></a>
</td>
<td>
   <span class="text-muted">No description provided.</span></td>
</tr>
</tbody>
</table>

## `LocalQueue`     {#kueue-x-k8s-io-v1beta1-LocalQueue}
    

**Appears in:**



<p>LocalQueue is the Schema for the localQueues API</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
<tr><td><code>apiVersion</code><br/>string</td><td><code>kueue.x-k8s.io/v1beta1</code></td></tr>
<tr><td><code>kind</code><br/>string</td><td><code>LocalQueue</code></td></tr>
    
  
<tr><td><code>spec</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-LocalQueueSpec"><code>LocalQueueSpec</code></a>
</td>
<td>
   <span class="text-muted">No description provided.</span></td>
</tr>
<tr><td><code>status</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-LocalQueueStatus"><code>LocalQueueStatus</code></a>
</td>
<td>
   <span class="text-muted">No description provided.</span></td>
</tr>
</tbody>
</table>

## `ProvisioningRequestConfig`     {#kueue-x-k8s-io-v1beta1-ProvisioningRequestConfig}
    

**Appears in:**



<p>ProvisioningRequestConfig is the Schema for the provisioningrequestconfig API</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
<tr><td><code>apiVersion</code><br/>string</td><td><code>kueue.x-k8s.io/v1beta1</code></td></tr>
<tr><td><code>kind</code><br/>string</td><td><code>ProvisioningRequestConfig</code></td></tr>
    
  
<tr><td><code>spec</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-ProvisioningRequestConfigSpec"><code>ProvisioningRequestConfigSpec</code></a>
</td>
<td>
   <span class="text-muted">No description provided.</span></td>
</tr>
</tbody>
</table>

## `ResourceFlavor`     {#kueue-x-k8s-io-v1beta1-ResourceFlavor}
    

**Appears in:**



<p>ResourceFlavor is the Schema for the resourceflavors API.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
<tr><td><code>apiVersion</code><br/>string</td><td><code>kueue.x-k8s.io/v1beta1</code></td></tr>
<tr><td><code>kind</code><br/>string</td><td><code>ResourceFlavor</code></td></tr>
    
  
<tr><td><code>spec</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-ResourceFlavorSpec"><code>ResourceFlavorSpec</code></a>
</td>
<td>
   <span class="text-muted">No description provided.</span></td>
</tr>
</tbody>
</table>

## `Workload`     {#kueue-x-k8s-io-v1beta1-Workload}
    

**Appears in:**



<p>Workload is the Schema for the workloads API</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
<tr><td><code>apiVersion</code><br/>string</td><td><code>kueue.x-k8s.io/v1beta1</code></td></tr>
<tr><td><code>kind</code><br/>string</td><td><code>Workload</code></td></tr>
    
  
<tr><td><code>spec</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-WorkloadSpec"><code>WorkloadSpec</code></a>
</td>
<td>
   <span class="text-muted">No description provided.</span></td>
</tr>
<tr><td><code>status</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-WorkloadStatus"><code>WorkloadStatus</code></a>
</td>
<td>
   <span class="text-muted">No description provided.</span></td>
</tr>
</tbody>
</table>

## `WorkloadPriorityClass`     {#kueue-x-k8s-io-v1beta1-WorkloadPriorityClass}
    

**Appears in:**



<p>WorkloadPriorityClass is the Schema for the workloadPriorityClass API</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
<tr><td><code>apiVersion</code><br/>string</td><td><code>kueue.x-k8s.io/v1beta1</code></td></tr>
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
when this workloadPriorityClass should be used.</p>
</td>
</tr>
</tbody>
</table>

## `Admission`     {#kueue-x-k8s-io-v1beta1-Admission}
    

**Appears in:**

- [WorkloadStatus](#kueue-x-k8s-io-v1beta1-WorkloadStatus)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>clusterQueue</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-ClusterQueueReference"><code>ClusterQueueReference</code></a>
</td>
<td>
   <p>clusterQueue is the name of the ClusterQueue that admitted this workload.</p>
</td>
</tr>
<tr><td><code>podSetAssignments</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-PodSetAssignment"><code>[]PodSetAssignment</code></a>
</td>
<td>
   <p>PodSetAssignments hold the admission results for each of the .spec.podSets entries.</p>
</td>
</tr>
</tbody>
</table>

## `AdmissionCheckParametersReference`     {#kueue-x-k8s-io-v1beta1-AdmissionCheckParametersReference}
    

**Appears in:**

- [AdmissionCheckSpec](#kueue-x-k8s-io-v1beta1-AdmissionCheckSpec)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>apiGroup</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>ApiGroup is the group for the resource being referenced.</p>
</td>
</tr>
<tr><td><code>kind</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>Kind is the type of the resource being referenced.</p>
</td>
</tr>
<tr><td><code>name</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>Name is the name of the resource being referenced.</p>
</td>
</tr>
</tbody>
</table>

## `AdmissionCheckSpec`     {#kueue-x-k8s-io-v1beta1-AdmissionCheckSpec}
    

**Appears in:**

- [AdmissionCheck](#kueue-x-k8s-io-v1beta1-AdmissionCheck)


<p>AdmissionCheckSpec defines the desired state of AdmissionCheck</p>


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
<tr><td><code>retryDelayMinutes</code><br/>
<code>int64</code>
</td>
<td>
   <p>RetryDelayMinutes specifies how long to keep the workload suspended
after a failed check (after it transitioned to False).
After that the check state goes to &quot;Unknown&quot;.
The default is 15 min.</p>
</td>
</tr>
<tr><td><code>parameters</code><br/>
<a href="#kueue-x-k8s-io-v1beta1-AdmissionCheckParametersReference"><code>AdmissionCheckParametersReference</code></a>
</td>
<td>
   <p>Parameters identifies the resource providing additional check parameters.</p>
</td>
</tr>
</tbody>
</table>

## `AdmissionCheckState`     {#kueue-x-k8s-io-v1beta1-AdmissionCheckState}
    

**Appears in:**

- [WorkloadStatus](#kueue-x-k8s-io-v1beta1-WorkloadStatus)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>name identifies the admission check.</p>
</td>
</tr>
<tr><td><code>state</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-CheckState"><code>CheckState</code></a>
</td>
<td>
   <p>state of the admissionCheck, one of Pending, Ready, Retry, Rejected</p>
</td>
</tr>
<tr><td><code>lastTransitionTime</code> <B>[Required]</B><br/>
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
<tr><td><code>podSetUpdates</code><br/>
<a href="#kueue-x-k8s-io-v1beta1-PodSetUpdate"><code>[]PodSetUpdate</code></a>
</td>
<td>
   <span class="text-muted">No description provided.</span></td>
</tr>
</tbody>
</table>

## `AdmissionCheckStatus`     {#kueue-x-k8s-io-v1beta1-AdmissionCheckStatus}
    

**Appears in:**

- [AdmissionCheck](#kueue-x-k8s-io-v1beta1-AdmissionCheck)


<p>AdmissionCheckStatus defines the observed state of AdmissionCheck</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>conditions</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#condition-v1-meta"><code>[]k8s.io/apimachinery/pkg/apis/meta/v1.Condition</code></a>
</td>
<td>
   <p>conditions hold the latest available observations of the AdmissionCheck
current state.</p>
</td>
</tr>
</tbody>
</table>

## `CheckState`     {#kueue-x-k8s-io-v1beta1-CheckState}
    
(Alias of `string`)

**Appears in:**

- [AdmissionCheckState](#kueue-x-k8s-io-v1beta1-AdmissionCheckState)





## `ClusterQueuePendingWorkload`     {#kueue-x-k8s-io-v1beta1-ClusterQueuePendingWorkload}
    

**Appears in:**

- [ClusterQueuePendingWorkloadsStatus](#kueue-x-k8s-io-v1beta1-ClusterQueuePendingWorkloadsStatus)


<p>ClusterQueuePendingWorkload contains the information identifying a pending workload
in the cluster queue.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>Name indicates the name of the pending workload.</p>
</td>
</tr>
<tr><td><code>namespace</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>Namespace indicates the name of the pending workload.</p>
</td>
</tr>
</tbody>
</table>

## `ClusterQueuePendingWorkloadsStatus`     {#kueue-x-k8s-io-v1beta1-ClusterQueuePendingWorkloadsStatus}
    

**Appears in:**

- [ClusterQueueStatus](#kueue-x-k8s-io-v1beta1-ClusterQueueStatus)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>clusterQueuePendingWorkload</code><br/>
<a href="#kueue-x-k8s-io-v1beta1-ClusterQueuePendingWorkload"><code>[]ClusterQueuePendingWorkload</code></a>
</td>
<td>
   <p>Head contains the list of top pending workloads.</p>
</td>
</tr>
<tr><td><code>lastChangeTime</code> <B>[Required]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#time-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.Time</code></a>
</td>
<td>
   <p>LastChangeTime indicates the time of the last change of the structure.</p>
</td>
</tr>
</tbody>
</table>

## `ClusterQueuePreemption`     {#kueue-x-k8s-io-v1beta1-ClusterQueuePreemption}
    

**Appears in:**

- [ClusterQueueSpec](#kueue-x-k8s-io-v1beta1-ClusterQueueSpec)


<p>ClusterQueuePreemption contains policies to preempt Workloads from this
ClusterQueue or the ClusterQueue's cohort.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>reclaimWithinCohort</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-PreemptionPolicy"><code>PreemptionPolicy</code></a>
</td>
<td>
   <p>reclaimWithinCohort determines whether a pending Workload can preempt
Workloads from other ClusterQueues in the cohort that are using more than
their nominal quota. The possible values are:</p>
<ul>
<li><code>Never</code> (default): do not preempt Workloads in the cohort.</li>
<li><code>LowerPriority</code>: if the pending Workload fits within the nominal
quota of its ClusterQueue, only preempt Workloads in the cohort that have
lower priority than the pending Workload.</li>
<li><code>Any</code>: if the pending Workload fits within the nominal quota of its
ClusterQueue, preempt any Workload in the cohort, irrespective of
priority.</li>
</ul>
</td>
</tr>
<tr><td><code>withinClusterQueue</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-PreemptionPolicy"><code>PreemptionPolicy</code></a>
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

## `ClusterQueueReference`     {#kueue-x-k8s-io-v1beta1-ClusterQueueReference}
    
(Alias of `string`)

**Appears in:**

- [Admission](#kueue-x-k8s-io-v1beta1-Admission)

- [LocalQueueSpec](#kueue-x-k8s-io-v1beta1-LocalQueueSpec)


<p>ClusterQueueReference is the name of the ClusterQueue.</p>




## `ClusterQueueSpec`     {#kueue-x-k8s-io-v1beta1-ClusterQueueSpec}
    

**Appears in:**

- [ClusterQueue](#kueue-x-k8s-io-v1beta1-ClusterQueue)


<p>ClusterQueueSpec defines the desired state of ClusterQueue</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>resourceGroups</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-ResourceGroup"><code>[]ResourceGroup</code></a>
</td>
<td>
   <p>resourceGroups describes groups of resources.
Each resource group defines the list of resources and a list of flavors
that provide quotas for these resources.
Each resource and each flavor can only form part of one resource group.
resourceGroups can be up to 16.</p>
</td>
</tr>
<tr><td><code>cohort</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>cohort that this ClusterQueue belongs to. CQs that belong to the
same cohort can borrow unused resources from each other.</p>
<p>A CQ can be a member of a single borrowing cohort. A workload submitted
to a queue referencing this CQ can borrow quota from any CQ in the cohort.
Only quota for the [resource, flavor] pairs listed in the CQ can be
borrowed.
If empty, this ClusterQueue cannot borrow from any other ClusterQueue and
vice versa.</p>
<p>A cohort is a name that links CQs together, but it doesn't reference any
object.</p>
<p>Validation of a cohort name is equivalent to that of object names:
subdomain in DNS (RFC 1123).</p>
</td>
</tr>
<tr><td><code>queueingStrategy</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-QueueingStrategy"><code>QueueingStrategy</code></a>
</td>
<td>
   <p>QueueingStrategy indicates the queueing strategy of the workloads
across the queues in this ClusterQueue. This field is immutable.
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
<tr><td><code>namespaceSelector</code> <B>[Required]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#labelselector-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.LabelSelector</code></a>
</td>
<td>
   <p>namespaceSelector defines which namespaces are allowed to submit workloads to
this clusterQueue. Beyond this basic support for policy, an policy agent like
Gatekeeper should be used to enforce more advanced policies.
Defaults to null which is a nothing selector (no namespaces eligible).
If set to an empty selector <code>{}</code>, then all namespaces are eligible.</p>
</td>
</tr>
<tr><td><code>flavorFungibility</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-FlavorFungibility"><code>FlavorFungibility</code></a>
</td>
<td>
   <p>flavorFungibility defines whether a workload should try the next flavor
before borrowing or preempting in the flavor being evaluated.</p>
</td>
</tr>
<tr><td><code>preemption</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-ClusterQueuePreemption"><code>ClusterQueuePreemption</code></a>
</td>
<td>
   <p>preemption describes policies to preempt Workloads from this ClusterQueue
or the ClusterQueue's cohort.</p>
<p>Preemption can happen in two scenarios:</p>
<ul>
<li>When a Workload fits within the nominal quota of the ClusterQueue, but
the quota is currently borrowed by other ClusterQueues in the cohort.
Preempting Workloads in other ClusterQueues allows this ClusterQueue to
reclaim its nominal quota.</li>
<li>When a Workload doesn't fit within the nominal quota of the ClusterQueue
and there are admitted Workloads in the ClusterQueue with lower priority.</li>
</ul>
<p>The preemption algorithm tries to find a minimal set of Workloads to
preempt to accomomdate the pending Workload, preempting Workloads with
lower priority first.</p>
</td>
</tr>
<tr><td><code>admissionChecks</code><br/>
<code>[]string</code>
</td>
<td>
   <p>admissionChecks lists the AdmissionChecks required by this ClusterQueue</p>
</td>
</tr>
<tr><td><code>stopPolicy</code><br/>
<a href="#kueue-x-k8s-io-v1beta1-StopPolicy"><code>StopPolicy</code></a>
</td>
<td>
   <p>stopPolicy - if set the ClusterQueue is considered Inactive, no new reservation being
made.</p>
<p>Depending on its value, its associated workloads will:</p>
<ul>
<li>StopNow - Admitted workloads are evicted and Reserving workloads will cancel the reservation.</li>
<li>WaitForAdmitted - Admitted workloads will run to completion and Reserving workloads will cancel the reservation.</li>
<li>WaitForAdmitted - Admitted and Reserving workloads will run to completion.</li>
</ul>
</td>
</tr>
</tbody>
</table>

## `ClusterQueueStatus`     {#kueue-x-k8s-io-v1beta1-ClusterQueueStatus}
    

**Appears in:**

- [ClusterQueue](#kueue-x-k8s-io-v1beta1-ClusterQueue)


<p>ClusterQueueStatus defines the observed state of ClusterQueue</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>flavorsReservation</code><br/>
<a href="#kueue-x-k8s-io-v1beta1-FlavorUsage"><code>[]FlavorUsage</code></a>
</td>
<td>
   <p>flavorsReservation are the reserved quotas, by flavor, currently in use by the
workloads assigned to this ClusterQueue.</p>
</td>
</tr>
<tr><td><code>flavorsUsage</code><br/>
<a href="#kueue-x-k8s-io-v1beta1-FlavorUsage"><code>[]FlavorUsage</code></a>
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
<tr><td><code>conditions</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#condition-v1-meta"><code>[]k8s.io/apimachinery/pkg/apis/meta/v1.Condition</code></a>
</td>
<td>
   <p>conditions hold the latest available observations of the ClusterQueue
current state.</p>
</td>
</tr>
<tr><td><code>pendingWorkloadsStatus</code><br/>
<a href="#kueue-x-k8s-io-v1beta1-ClusterQueuePendingWorkloadsStatus"><code>ClusterQueuePendingWorkloadsStatus</code></a>
</td>
<td>
   <p>PendingWorkloadsStatus contains the information exposed about the current
status of the pending workloads in the cluster queue.</p>
</td>
</tr>
</tbody>
</table>

## `FlavorFungibility`     {#kueue-x-k8s-io-v1beta1-FlavorFungibility}
    

**Appears in:**

- [ClusterQueueSpec](#kueue-x-k8s-io-v1beta1-ClusterQueueSpec)


<p>FlavorFungibility determines whether a workload should try the next flavor
before borrowing or preempting in current flavor.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>whenCanBorrow</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-FlavorFungibilityPolicy"><code>FlavorFungibilityPolicy</code></a>
</td>
<td>
   <p>whenCanBorrow determines whether a workload should try the next flavor
before borrowing in current flavor. The possible values are:</p>
<ul>
<li><code>Borrow</code> (default): allocate in current flavor if borrowing
is possible.</li>
<li><code>TryNextFlavor</code>: try next flavor even if the current
flavor has enough resources to borrow.</li>
</ul>
</td>
</tr>
<tr><td><code>whenCanPreempt</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-FlavorFungibilityPolicy"><code>FlavorFungibilityPolicy</code></a>
</td>
<td>
   <p>whenCanPreempt determines whether a workload should try the next flavor
before borrowing in current flavor. The possible values are:</p>
<ul>
<li><code>Preempt</code>: allocate in current flavor if it's possible to preempt some workloads.</li>
<li><code>TryNextFlavor</code> (default): try next flavor even if there are enough
candidates for preemption in the current flavor.</li>
</ul>
</td>
</tr>
</tbody>
</table>

## `FlavorFungibilityPolicy`     {#kueue-x-k8s-io-v1beta1-FlavorFungibilityPolicy}
    
(Alias of `string`)

**Appears in:**

- [FlavorFungibility](#kueue-x-k8s-io-v1beta1-FlavorFungibility)





## `FlavorQuotas`     {#kueue-x-k8s-io-v1beta1-FlavorQuotas}
    

**Appears in:**

- [ResourceGroup](#kueue-x-k8s-io-v1beta1-ResourceGroup)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-ResourceFlavorReference"><code>ResourceFlavorReference</code></a>
</td>
<td>
   <p>name of this flavor. The name should match the .metadata.name of a
ResourceFlavor. If a matching ResourceFlavor does not exist, the
ClusterQueue will have an Active condition set to False.</p>
</td>
</tr>
<tr><td><code>resources</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-ResourceQuota"><code>[]ResourceQuota</code></a>
</td>
<td>
   <p>resources is the list of quotas for this flavor per resource.
There could be up to 16 resources.</p>
</td>
</tr>
</tbody>
</table>

## `FlavorUsage`     {#kueue-x-k8s-io-v1beta1-FlavorUsage}
    

**Appears in:**

- [ClusterQueueStatus](#kueue-x-k8s-io-v1beta1-ClusterQueueStatus)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-ResourceFlavorReference"><code>ResourceFlavorReference</code></a>
</td>
<td>
   <p>name of the flavor.</p>
</td>
</tr>
<tr><td><code>resources</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-ResourceUsage"><code>[]ResourceUsage</code></a>
</td>
<td>
   <p>resources lists the quota usage for the resources in this flavor.</p>
</td>
</tr>
</tbody>
</table>

## `LocalQueueFlavorUsage`     {#kueue-x-k8s-io-v1beta1-LocalQueueFlavorUsage}
    

**Appears in:**

- [LocalQueueStatus](#kueue-x-k8s-io-v1beta1-LocalQueueStatus)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-ResourceFlavorReference"><code>ResourceFlavorReference</code></a>
</td>
<td>
   <p>name of the flavor.</p>
</td>
</tr>
<tr><td><code>resources</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-LocalQueueResourceUsage"><code>[]LocalQueueResourceUsage</code></a>
</td>
<td>
   <p>resources lists the quota usage for the resources in this flavor.</p>
</td>
</tr>
</tbody>
</table>

## `LocalQueueResourceUsage`     {#kueue-x-k8s-io-v1beta1-LocalQueueResourceUsage}
    

**Appears in:**

- [LocalQueueFlavorUsage](#kueue-x-k8s-io-v1beta1-LocalQueueFlavorUsage)



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
<tr><td><code>total</code> <B>[Required]</B><br/>
<a href="https://pkg.go.dev/k8s.io/apimachinery/pkg/api/resource#Quantity"><code>k8s.io/apimachinery/pkg/api/resource.Quantity</code></a>
</td>
<td>
   <p>total is the total quantity of used quota.</p>
</td>
</tr>
</tbody>
</table>

## `LocalQueueSpec`     {#kueue-x-k8s-io-v1beta1-LocalQueueSpec}
    

**Appears in:**

- [LocalQueue](#kueue-x-k8s-io-v1beta1-LocalQueue)


<p>LocalQueueSpec defines the desired state of LocalQueue</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>clusterQueue</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-ClusterQueueReference"><code>ClusterQueueReference</code></a>
</td>
<td>
   <p>clusterQueue is a reference to a clusterQueue that backs this localQueue.</p>
</td>
</tr>
</tbody>
</table>

## `LocalQueueStatus`     {#kueue-x-k8s-io-v1beta1-LocalQueueStatus}
    

**Appears in:**

- [LocalQueue](#kueue-x-k8s-io-v1beta1-LocalQueue)


<p>LocalQueueStatus defines the observed state of LocalQueue</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>pendingWorkloads</code><br/>
<code>int32</code>
</td>
<td>
   <p>PendingWorkloads is the number of Workloads in the LocalQueue not yet admitted to a ClusterQueue</p>
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
<tr><td><code>conditions</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#condition-v1-meta"><code>[]k8s.io/apimachinery/pkg/apis/meta/v1.Condition</code></a>
</td>
<td>
   <p>Conditions hold the latest available observations of the LocalQueue
current state.</p>
</td>
</tr>
<tr><td><code>flavorsReservation</code><br/>
<a href="#kueue-x-k8s-io-v1beta1-LocalQueueFlavorUsage"><code>[]LocalQueueFlavorUsage</code></a>
</td>
<td>
   <p>flavorsReservation are the reserved quotas, by flavor currently in use by the
workloads assigned to this LocalQueue.</p>
</td>
</tr>
<tr><td><code>flavorUsage</code><br/>
<a href="#kueue-x-k8s-io-v1beta1-LocalQueueFlavorUsage"><code>[]LocalQueueFlavorUsage</code></a>
</td>
<td>
   <p>flavorsUsage are the used quotas, by flavor currently in use by the
workloads assigned to this LocalQueue.</p>
</td>
</tr>
</tbody>
</table>

## `Parameter`     {#kueue-x-k8s-io-v1beta1-Parameter}
    
(Alias of `string`)

**Appears in:**

- [ProvisioningRequestConfigSpec](#kueue-x-k8s-io-v1beta1-ProvisioningRequestConfigSpec)


<p>Parameter is limited to 255 characters.</p>




## `PodSet`     {#kueue-x-k8s-io-v1beta1-PodSet}
    

**Appears in:**

- [WorkloadSpec](#kueue-x-k8s-io-v1beta1-WorkloadSpec)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<code>string</code>
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
<tr><td><code>count</code> <B>[Required]</B><br/>
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
</tbody>
</table>

## `PodSetAssignment`     {#kueue-x-k8s-io-v1beta1-PodSetAssignment}
    

**Appears in:**

- [Admission](#kueue-x-k8s-io-v1beta1-Admission)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>Name is the name of the podSet. It should match one of the names in .spec.podSets.</p>
</td>
</tr>
<tr><td><code>flavors</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-ResourceFlavorReference"><code>map[ResourceName]ResourceFlavorReference</code></a>
</td>
<td>
   <p>Flavors are the flavors assigned to the workload for each resource.</p>
</td>
</tr>
<tr><td><code>resourceUsage</code> <B>[Required]</B><br/>
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
</tbody>
</table>

## `PodSetUpdate`     {#kueue-x-k8s-io-v1beta1-PodSetUpdate}
    

**Appears in:**

- [AdmissionCheckState](#kueue-x-k8s-io-v1beta1-AdmissionCheckState)


<p>PodSetUpdate contains a list of pod set modifications suggested by AdmissionChecks.
The modifications should be additive only - modifications of already existing keys
or having the same key provided by multiple AdmissionChecks is not allowed and will
result in failure during workload admission.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>Name of the PodSet to modify. Should match to one of the Workload's PodSets.</p>
</td>
</tr>
<tr><td><code>labels</code><br/>
<code>map[string]string</code>
</td>
<td>
   <span class="text-muted">No description provided.</span></td>
</tr>
<tr><td><code>annotations</code><br/>
<code>map[string]string</code>
</td>
<td>
   <span class="text-muted">No description provided.</span></td>
</tr>
<tr><td><code>nodeSelector</code><br/>
<code>map[string]string</code>
</td>
<td>
   <span class="text-muted">No description provided.</span></td>
</tr>
<tr><td><code>tolerations</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#toleration-v1-core"><code>[]k8s.io/api/core/v1.Toleration</code></a>
</td>
<td>
   <span class="text-muted">No description provided.</span></td>
</tr>
</tbody>
</table>

## `PreemptionPolicy`     {#kueue-x-k8s-io-v1beta1-PreemptionPolicy}
    
(Alias of `string`)

**Appears in:**

- [ClusterQueuePreemption](#kueue-x-k8s-io-v1beta1-ClusterQueuePreemption)





## `ProvisioningRequestConfigSpec`     {#kueue-x-k8s-io-v1beta1-ProvisioningRequestConfigSpec}
    

**Appears in:**

- [ProvisioningRequestConfig](#kueue-x-k8s-io-v1beta1-ProvisioningRequestConfig)


<p>ProvisioningRequestConfigSpec defines the desired state of ProvisioningRequestConfig</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>provisioningClassName</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>ProvisioningClassName describes the different modes of provisioning the resources.
Check autoscaling.x-k8s.io ProvisioningRequestSpec.ProvisioningClassName for details.</p>
</td>
</tr>
<tr><td><code>parameters</code><br/>
<a href="#kueue-x-k8s-io-v1beta1-Parameter"><code>map[string]Parameter</code></a>
</td>
<td>
   <p>Parameters contains all other parameters classes may require.</p>
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
</tbody>
</table>

## `QueueingStrategy`     {#kueue-x-k8s-io-v1beta1-QueueingStrategy}
    
(Alias of `string`)

**Appears in:**

- [ClusterQueueSpec](#kueue-x-k8s-io-v1beta1-ClusterQueueSpec)





## `ReclaimablePod`     {#kueue-x-k8s-io-v1beta1-ReclaimablePod}
    

**Appears in:**

- [WorkloadStatus](#kueue-x-k8s-io-v1beta1-WorkloadStatus)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<code>string</code>
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

## `ResourceFlavorReference`     {#kueue-x-k8s-io-v1beta1-ResourceFlavorReference}
    
(Alias of `string`)

**Appears in:**

- [FlavorQuotas](#kueue-x-k8s-io-v1beta1-FlavorQuotas)

- [FlavorUsage](#kueue-x-k8s-io-v1beta1-FlavorUsage)

- [LocalQueueFlavorUsage](#kueue-x-k8s-io-v1beta1-LocalQueueFlavorUsage)

- [PodSetAssignment](#kueue-x-k8s-io-v1beta1-PodSetAssignment)


<p>ResourceFlavorReference is the name of the ResourceFlavor.</p>




## `ResourceFlavorSpec`     {#kueue-x-k8s-io-v1beta1-ResourceFlavorSpec}
    

**Appears in:**

- [ResourceFlavor](#kueue-x-k8s-io-v1beta1-ResourceFlavor)


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
get assigned this ResourceFlavor during admission.</p>
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
</tbody>
</table>

## `ResourceGroup`     {#kueue-x-k8s-io-v1beta1-ResourceGroup}
    

**Appears in:**

- [ClusterQueueSpec](#kueue-x-k8s-io-v1beta1-ClusterQueueSpec)



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
The list cannot be empty and it can contain up to 16 resources.</p>
</td>
</tr>
<tr><td><code>flavors</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-FlavorQuotas"><code>[]FlavorQuotas</code></a>
</td>
<td>
   <p>flavors is the list of flavors that provide the resources of this group.
Typically, different flavors represent different hardware models
(e.g., gpu models, cpu architectures) or pricing models (on-demand vs spot
cpus).
Each flavor MUST list all the resources listed for this group in the same
order as the .resources field.
The list cannot be empty and it can contain up to 16 flavors.</p>
</td>
</tr>
</tbody>
</table>

## `ResourceQuota`     {#kueue-x-k8s-io-v1beta1-ResourceQuota}
    

**Appears in:**

- [FlavorQuotas](#kueue-x-k8s-io-v1beta1-FlavorQuotas)



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
borrowingLimit must be null if spec.cohort is empty.</p>
</td>
</tr>
</tbody>
</table>

## `ResourceUsage`     {#kueue-x-k8s-io-v1beta1-ResourceUsage}
    

**Appears in:**

- [FlavorUsage](#kueue-x-k8s-io-v1beta1-FlavorUsage)



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
<tr><td><code>total</code> <B>[Required]</B><br/>
<a href="https://pkg.go.dev/k8s.io/apimachinery/pkg/api/resource#Quantity"><code>k8s.io/apimachinery/pkg/api/resource.Quantity</code></a>
</td>
<td>
   <p>total is the total quantity of used quota, including the amount borrowed
from the cohort.</p>
</td>
</tr>
<tr><td><code>borrowed</code> <B>[Required]</B><br/>
<a href="https://pkg.go.dev/k8s.io/apimachinery/pkg/api/resource#Quantity"><code>k8s.io/apimachinery/pkg/api/resource.Quantity</code></a>
</td>
<td>
   <p>Borrowed is quantity of quota that is borrowed from the cohort. In other
words, it's the used quota that is over the nominalQuota.</p>
</td>
</tr>
</tbody>
</table>

## `StopPolicy`     {#kueue-x-k8s-io-v1beta1-StopPolicy}
    
(Alias of `string`)

**Appears in:**

- [ClusterQueueSpec](#kueue-x-k8s-io-v1beta1-ClusterQueueSpec)





## `WorkloadSpec`     {#kueue-x-k8s-io-v1beta1-WorkloadSpec}
    

**Appears in:**

- [Workload](#kueue-x-k8s-io-v1beta1-Workload)


<p>WorkloadSpec defines the desired state of Workload</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>podSets</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-PodSet"><code>[]PodSet</code></a>
</td>
<td>
   <p>podSets is a list of sets of homogeneous pods, each described by a Pod spec
and a count.
There must be at least one element and at most 8.
podSets cannot be changed.</p>
</td>
</tr>
<tr><td><code>queueName</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>queueName is the name of the LocalQueue the Workload is associated with.
queueName cannot be changed while .status.admission is not null.</p>
</td>
</tr>
<tr><td><code>priorityClassName</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>If specified, indicates the workload's priority.
&quot;system-node-critical&quot; and &quot;system-cluster-critical&quot; are two special
keywords which indicate the highest priorities with the former being
the highest priority. Any other name must be defined by creating a
PriorityClass object with that name. If not specified, the workload
priority will be default or zero if there is no default.</p>
</td>
</tr>
<tr><td><code>priority</code> <B>[Required]</B><br/>
<code>int32</code>
</td>
<td>
   <p>Priority determines the order of access to the resources managed by the
ClusterQueue where the workload is queued.
The priority value is populated from PriorityClassName.
The higher the value, the higher the priority.
If priorityClassName is specified, priority must not be null.</p>
</td>
</tr>
<tr><td><code>priorityClassSource</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>priorityClassSource determines whether the priorityClass field refers to a pod PriorityClass or kueue.x-k8s.io/workloadpriorityclass.
Workload's PriorityClass can accept the name of a pod priorityClass or a workloadPriorityClass.
When using pod PriorityClass, a priorityClassSource field has the scheduling.k8s.io/priorityclass value.</p>
</td>
</tr>
</tbody>
</table>

## `WorkloadStatus`     {#kueue-x-k8s-io-v1beta1-WorkloadStatus}
    

**Appears in:**

- [Workload](#kueue-x-k8s-io-v1beta1-Workload)


<p>WorkloadStatus defines the observed state of Workload</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>admission</code> <B>[Required]</B><br/>
<a href="#kueue-x-k8s-io-v1beta1-Admission"><code>Admission</code></a>
</td>
<td>
   <p>admission holds the parameters of the admission of the workload by a
ClusterQueue. admission can be set back to null, but its fields cannot be
changed once set.</p>
</td>
</tr>
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
succeeded.</li>
</ul>
</td>
</tr>
<tr><td><code>reclaimablePods</code><br/>
<a href="#kueue-x-k8s-io-v1beta1-ReclaimablePod"><code>[]ReclaimablePod</code></a>
</td>
<td>
   <p>reclaimablePods keeps track of the number pods within a podset for which
the resource reservation is no longer needed.</p>
</td>
</tr>
<tr><td><code>admissionChecks</code><br/>
<a href="#kueue-x-k8s-io-v1beta1-AdmissionCheckState"><code>[]AdmissionCheckState</code></a>
</td>
<td>
   <p>admissionChecks list all the admission checks required by the workload and the current status</p>
</td>
</tr>
</tbody>
</table>
  