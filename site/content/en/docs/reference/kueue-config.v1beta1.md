---
title: Kueue Configuration API
content_type: tool-reference
package: /v1beta1
auto_generated: true
description: Generated API reference documentation for Kueue Configuration.
---


## Resource Types 


- [Configuration](#Configuration)
  
    
    

## `AdmissionFairSharing`     {#AdmissionFairSharing}
    

**Appears in:**




<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>usageHalfLifeTime</code> <B>[Required]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#duration-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.Duration</code></a>
</td>
<td>
   <p>usageHalfLifeTime indicates the time after which the current usage will decay by a half
If set to 0, usage will be reset to 0 immediately.</p>
</td>
</tr>
<tr><td><code>usageSamplingInterval</code> <B>[Required]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#duration-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.Duration</code></a>
</td>
<td>
   <p>usageSamplingInterval indicates how often Kueue updates consumedResources in FairSharingStatus
Defaults to 5min.</p>
</td>
</tr>
<tr><td><code>resourceWeights</code> <B>[Required]</B><br/>
<code>map[ResourceName]float64</code>
</td>
<td>
   <p>resourceWeights assigns weights to resources which then are used to calculate LocalQueue's
resource usage and order Workloads.
Defaults to 1.</p>
</td>
</tr>
</tbody>
</table>

## `ClientConnection`     {#ClientConnection}
    

**Appears in:**




<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>qps</code> <B>[Required]</B><br/>
<code>float32</code>
</td>
<td>
   <p>QPS controls the number of queries per second allowed for K8S api server
connection.</p>
<p>Setting this to a negative value will disable client-side ratelimiting.</p>
</td>
</tr>
<tr><td><code>burst</code> <B>[Required]</B><br/>
<code>int32</code>
</td>
<td>
   <p>Burst allows extra queries to accumulate when a client is exceeding its rate.</p>
</td>
</tr>
</tbody>
</table>

## `ClusterQueueVisibility`     {#ClusterQueueVisibility}
    

**Appears in:**

- [QueueVisibility](#QueueVisibility)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>maxCount</code> <B>[Required]</B><br/>
<code>int32</code>
</td>
<td>
   <p>MaxCount indicates the maximal number of pending workloads exposed in the
cluster queue status.  When the value is set to 0, then ClusterQueue
visibility updates are disabled.
The maximal value is 4000.
Defaults to 10.</p>
</td>
</tr>
</tbody>
</table>

## `Configuration`     {#Configuration}
    


<p>Configuration is the Schema for the kueueconfigurations API</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>namespace</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>Namespace is the namespace in which kueue is deployed. It is used as part of DNSName of the webhook Service.
If not set, the value is set from the file /var/run/secrets/kubernetes.io/serviceaccount/namespace
If the file doesn't exist, default value is kueue-system.</p>
</td>
</tr>
<tr><td><code>ControllerManager</code> <B>[Required]</B><br/>
<a href="#ControllerManager"><code>ControllerManager</code></a>
</td>
<td>(Members of <code>ControllerManager</code> are embedded into this type.)
   <p>ControllerManager returns the configurations for controllers</p>
</td>
</tr>
<tr><td><code>manageJobsWithoutQueueName</code> <B>[Required]</B><br/>
<code>bool</code>
</td>
<td>
   <p>ManageJobsWithoutQueueName controls whether or not Kueue reconciles
jobs that don't set the label kueue.x-k8s.io/queue-name.
If set to true, then those jobs will be suspended and never started unless
they are assigned a queue and eventually admitted. This also applies to
jobs created before starting the kueue controller.
Defaults to false; therefore, those jobs are not managed and if they are created
unsuspended, they will start immediately.</p>
</td>
</tr>
<tr><td><code>managedJobsNamespaceSelector</code> <B>[Required]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#labelselector-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.LabelSelector</code></a>
</td>
<td>
   <p>ManagedJobsNamespaceSelector provides a namespace-based mechanism to exempt jobs
from management by Kueue.</p>
<p>It provides a strong exemption for the Pod-based integrations (pod, deployment, statefulset, etc.),
For Pod-based integrations, only jobs whose namespaces match ManagedJobsNamespaceSelector are
eligible to be managed by Kueue.  Pods, deployments, etc. in non-matching namespaces will
never be managed by Kueue, even if they have a kueue.x-k8s.io/queue-name label.
This strong exemption ensures that Kueue will not interfere with the basic operation
of system namespace.</p>
<p>For all other integrations, ManagedJobsNamespaceSelector provides a weaker exemption
by only modulating the effects of ManageJobsWithoutQueueName.  For these integrations,
a job that has a kueue.x-k8s.io/queue-name label will always be managed by Kueue. Jobs without
a kueue.x-k8s.io/queue-name label will be managed by Kueue only when ManageJobsWithoutQueueName is
true and the job's namespace matches ManagedJobsNamespaceSelector.</p>
</td>
</tr>
<tr><td><code>internalCertManagement</code> <B>[Required]</B><br/>
<a href="#InternalCertManagement"><code>InternalCertManagement</code></a>
</td>
<td>
   <p>InternalCertManagement is configuration for internalCertManagement</p>
</td>
</tr>
<tr><td><code>waitForPodsReady</code> <B>[Required]</B><br/>
<a href="#WaitForPodsReady"><code>WaitForPodsReady</code></a>
</td>
<td>
   <p>WaitForPodsReady is configuration to provide a time-based all-or-nothing
scheduling semantics for Jobs, by ensuring all pods are ready (running
and passing the readiness probe) within the specified time. If the timeout
is exceeded, then the workload is evicted.</p>
</td>
</tr>
<tr><td><code>clientConnection</code> <B>[Required]</B><br/>
<a href="#ClientConnection"><code>ClientConnection</code></a>
</td>
<td>
   <p>ClientConnection provides additional configuration options for Kubernetes
API server client.</p>
</td>
</tr>
<tr><td><code>integrations</code> <B>[Required]</B><br/>
<a href="#Integrations"><code>Integrations</code></a>
</td>
<td>
   <p>Integrations provide configuration options for AI/ML/Batch frameworks
integrations (including K8S job).</p>
</td>
</tr>
<tr><td><code>queueVisibility</code> <B>[Required]</B><br/>
<a href="#QueueVisibility"><code>QueueVisibility</code></a>
</td>
<td>
   <p>QueueVisibility is configuration to expose the information about the top
pending workloads.
Deprecated: This field will be removed on v1beta2, use VisibilityOnDemand
(https://kueue.sigs.k8s.io/docs/tasks/manage/monitor_pending_workloads/pending_workloads_on_demand/)
instead.</p>
</td>
</tr>
<tr><td><code>multiKueue</code> <B>[Required]</B><br/>
<a href="#MultiKueue"><code>MultiKueue</code></a>
</td>
<td>
   <p>MultiKueue controls the behaviour of the MultiKueue AdmissionCheck Controller.</p>
</td>
</tr>
<tr><td><code>fairSharing</code> <B>[Required]</B><br/>
<a href="#FairSharing"><code>FairSharing</code></a>
</td>
<td>
   <p>FairSharing controls the Fair Sharing semantics across the cluster.</p>
</td>
</tr>
<tr><td><code>admissionFairSharing</code> <B>[Required]</B><br/>
<a href="#AdmissionFairSharing"><code>AdmissionFairSharing</code></a>
</td>
<td>
   <p>admissionFairSharing indicates configuration of FairSharing with the <code>AdmissionTime</code> mode on</p>
</td>
</tr>
<tr><td><code>resources</code> <B>[Required]</B><br/>
<a href="#Resources"><code>Resources</code></a>
</td>
<td>
   <p>Resources provides additional configuration options for handling the resources.</p>
</td>
</tr>
<tr><td><code>featureGates</code> <B>[Required]</B><br/>
<code>map[string]bool</code>
</td>
<td>
   <p>FeatureGates is a map of feature names to bools that allows to override the
default enablement status of a feature. The map cannot be used in conjunction
with passing the list of features via the command line argument &quot;--feature-gates&quot;
for the Kueue Deployment.</p>
</td>
</tr>
<tr><td><code>objectRetentionPolicies</code><br/>
<a href="#ObjectRetentionPolicies"><code>ObjectRetentionPolicies</code></a>
</td>
<td>
   <p>ObjectRetentionPolicies provides configuration options for automatic deletion
of Kueue-managed objects. A nil value disables all automatic deletions.</p>
</td>
</tr>
</tbody>
</table>

## `ControllerConfigurationSpec`     {#ControllerConfigurationSpec}
    

**Appears in:**

- [ControllerManager](#ControllerManager)


<p>ControllerConfigurationSpec defines the global configuration for
controllers registered with the manager.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>groupKindConcurrency</code><br/>
<code>map[string]int</code>
</td>
<td>
   <p>GroupKindConcurrency is a map from a Kind to the number of concurrent reconciliation
allowed for that controller.</p>
<p>When a controller is registered within this manager using the builder utilities,
users have to specify the type the controller reconciles in the For(...) call.
If the object's kind passed matches one of the keys in this map, the concurrency
for that controller is set to the number specified.</p>
<p>The key is expected to be consistent in form with GroupKind.String(),
e.g. ReplicaSet in apps group (regardless of version) would be <code>ReplicaSet.apps</code>.</p>
</td>
</tr>
<tr><td><code>cacheSyncTimeout</code><br/>
<a href="https://pkg.go.dev/time#Duration"><code>time.Duration</code></a>
</td>
<td>
   <p>CacheSyncTimeout refers to the time limit set to wait for syncing caches.
Defaults to 2 minutes if not set.</p>
</td>
</tr>
</tbody>
</table>

## `ControllerHealth`     {#ControllerHealth}
    

**Appears in:**

- [ControllerManager](#ControllerManager)


<p>ControllerHealth defines the health configs.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>healthProbeBindAddress</code><br/>
<code>string</code>
</td>
<td>
   <p>HealthProbeBindAddress is the TCP address that the controller should bind to
for serving health probes
It can be set to &quot;0&quot; or &quot;&quot; to disable serving the health probe.</p>
</td>
</tr>
<tr><td><code>readinessEndpointName</code><br/>
<code>string</code>
</td>
<td>
   <p>ReadinessEndpointName, defaults to &quot;readyz&quot;</p>
</td>
</tr>
<tr><td><code>livenessEndpointName</code><br/>
<code>string</code>
</td>
<td>
   <p>LivenessEndpointName, defaults to &quot;healthz&quot;</p>
</td>
</tr>
</tbody>
</table>

## `ControllerManager`     {#ControllerManager}
    

**Appears in:**




<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>webhook</code><br/>
<a href="#ControllerWebhook"><code>ControllerWebhook</code></a>
</td>
<td>
   <p>Webhook contains the controllers webhook configuration</p>
</td>
</tr>
<tr><td><code>leaderElection</code><br/>
<a href="https://pkg.go.dev/k8s.io/component-base/config/v1alpha1#LeaderElectionConfiguration"><code>k8s.io/component-base/config/v1alpha1.LeaderElectionConfiguration</code></a>
</td>
<td>
   <p>LeaderElection is the LeaderElection config to be used when configuring
the manager.Manager leader election</p>
</td>
</tr>
<tr><td><code>metrics</code><br/>
<a href="#ControllerMetrics"><code>ControllerMetrics</code></a>
</td>
<td>
   <p>Metrics contains the controller metrics configuration</p>
</td>
</tr>
<tr><td><code>health</code><br/>
<a href="#ControllerHealth"><code>ControllerHealth</code></a>
</td>
<td>
   <p>Health contains the controller health configuration</p>
</td>
</tr>
<tr><td><code>pprofBindAddress</code><br/>
<code>string</code>
</td>
<td>
   <p>PprofBindAddress is the TCP address that the controller should bind to
for serving pprof.
It can be set to &quot;&quot; or &quot;0&quot; to disable the pprof serving.
Since pprof may contain sensitive information, make sure to protect it
before exposing it to public.</p>
</td>
</tr>
<tr><td><code>controller</code><br/>
<a href="#ControllerConfigurationSpec"><code>ControllerConfigurationSpec</code></a>
</td>
<td>
   <p>Controller contains global configuration options for controllers
registered within this manager.</p>
</td>
</tr>
</tbody>
</table>

## `ControllerMetrics`     {#ControllerMetrics}
    

**Appears in:**

- [ControllerManager](#ControllerManager)


<p>ControllerMetrics defines the metrics configs.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>bindAddress</code><br/>
<code>string</code>
</td>
<td>
   <p>BindAddress is the TCP address that the controller should bind to
for serving prometheus metrics.
It can be set to &quot;0&quot; to disable the metrics serving.</p>
</td>
</tr>
<tr><td><code>enableClusterQueueResources</code><br/>
<code>bool</code>
</td>
<td>
   <p>EnableClusterQueueResources, if true the cluster queue resource usage and quotas
metrics will be reported.</p>
</td>
</tr>
</tbody>
</table>

## `ControllerWebhook`     {#ControllerWebhook}
    

**Appears in:**

- [ControllerManager](#ControllerManager)


<p>ControllerWebhook defines the webhook server for the controller.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>port</code><br/>
<code>int</code>
</td>
<td>
   <p>Port is the port that the webhook server serves at.
It is used to set webhook.Server.Port.</p>
</td>
</tr>
<tr><td><code>host</code><br/>
<code>string</code>
</td>
<td>
   <p>Host is the hostname that the webhook server binds to.
It is used to set webhook.Server.Host.</p>
</td>
</tr>
<tr><td><code>certDir</code><br/>
<code>string</code>
</td>
<td>
   <p>CertDir is the directory that contains the server key and certificate.
if not set, webhook server would look up the server key and certificate in
{TempDir}/k8s-webhook-server/serving-certs. The server key and certificate
must be named tls.key and tls.crt, respectively.</p>
</td>
</tr>
</tbody>
</table>

## `FairSharing`     {#FairSharing}
    

**Appears in:**




<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>enable</code> <B>[Required]</B><br/>
<code>bool</code>
</td>
<td>
   <p>enable indicates whether to enable Fair Sharing for all cohorts.
Defaults to false.</p>
</td>
</tr>
<tr><td><code>preemptionStrategies</code> <B>[Required]</B><br/>
<a href="#PreemptionStrategy"><code>[]PreemptionStrategy</code></a>
</td>
<td>
   <p>preemptionStrategies indicates which constraints should a preemption satisfy.
The preemption algorithm will only use the next strategy in the list if the
incoming workload (preemptor) doesn't fit after using the previous strategies.
Possible values are:</p>
<ul>
<li>LessThanOrEqualToFinalShare: Only preempt a workload if the share of the preemptor CQ
with the preemptor workload is less than or equal to the share of the preemptee CQ
without the workload to be preempted.
This strategy might favor preemption of smaller workloads in the preemptee CQ,
regardless of priority or start time, in an effort to keep the share of the CQ
as high as possible.</li>
<li>LessThanInitialShare: Only preempt a workload if the share of the preemptor CQ
with the incoming workload is strictly less than the share of the preemptee CQ.
This strategy doesn't depend on the share usage of the workload being preempted.
As a result, the strategy chooses to preempt workloads with the lowest priority and
newest start time first.
The default strategy is [&quot;LessThanOrEqualToFinalShare&quot;, &quot;LessThanInitialShare&quot;].</li>
</ul>
</td>
</tr>
</tbody>
</table>

## `Integrations`     {#Integrations}
    

**Appears in:**




<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>frameworks</code> <B>[Required]</B><br/>
<code>[]string</code>
</td>
<td>
   <p>List of framework names to be enabled.
Possible options:</p>
<ul>
<li>&quot;batch/job&quot;</li>
<li>&quot;kubeflow.org/mpijob&quot;</li>
<li>&quot;ray.io/rayjob&quot;</li>
<li>&quot;ray.io/raycluster&quot;</li>
<li>&quot;jobset.x-k8s.io/jobset&quot;</li>
<li>&quot;kubeflow.org/paddlejob&quot;</li>
<li>&quot;kubeflow.org/pytorchjob&quot;</li>
<li>&quot;kubeflow.org/tfjob&quot;</li>
<li>&quot;kubeflow.org/xgboostjob&quot;</li>
<li>&quot;kubeflow.org/jaxjob&quot;</li>
<li>&quot;workload.codeflare.dev/appwrapper&quot;</li>
<li>&quot;pod&quot;</li>
<li>&quot;deployment&quot; (requires enabling pod integration)</li>
<li>&quot;statefulset&quot; (requires enabling pod integration)</li>
<li>&quot;leaderworkerset.x-k8s.io/leaderworkerset&quot; (requires enabling pod integration)</li>
</ul>
</td>
</tr>
<tr><td><code>externalFrameworks</code> <B>[Required]</B><br/>
<code>[]string</code>
</td>
<td>
   <p>List of GroupVersionKinds that are managed for Kueue by external controllers;
the expected format is <code>Kind.version.group.com</code>.</p>
</td>
</tr>
<tr><td><code>podOptions</code> <B>[Required]</B><br/>
<a href="#PodIntegrationOptions"><code>PodIntegrationOptions</code></a>
</td>
<td>
   <p>PodOptions defines kueue controller behaviour for pod objects
Deprecated: This field will be removed on v1beta2, use ManagedJobsNamespaceSelector
(https://kueue.sigs.k8s.io/docs/tasks/run/plain_pods/)
instead.</p>
</td>
</tr>
<tr><td><code>labelKeysToCopy</code> <B>[Required]</B><br/>
<code>[]string</code>
</td>
<td>
   <p>labelKeysToCopy is a list of label keys that should be copied from the job into the
workload object. It is not required for the job to have all the labels from this
list. If a job does not have some label with the given key from this list, the
constructed workload object will be created without this label. In the case
of creating a workload from a composable job (pod group), if multiple objects
have labels with some key from the list, the values of these labels must
match or otherwise the workload creation would fail. The labels are copied only
during the workload creation and are not updated even if the labels of the
underlying job are changed.</p>
</td>
</tr>
</tbody>
</table>

## `InternalCertManagement`     {#InternalCertManagement}
    

**Appears in:**




<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>enable</code> <B>[Required]</B><br/>
<code>bool</code>
</td>
<td>
   <p>Enable controls the use of internal cert management for the webhook
and metrics endpoints.
When enabled Kueue is using libraries to generate and
self-sign the certificates.
When disabled, you need to provide the certificates for
the webhooks and metrics through a third party certificate
This secret is mounted to the kueue controller manager pod. The mount
path for webhooks is /tmp/k8s-webhook-server/serving-certs, whereas for
metrics endpoint the expected path is <code>/etc/kueue/metrics/certs</code>.
The keys and certs are named tls.key and tls.crt.</p>
</td>
</tr>
<tr><td><code>webhookServiceName</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>WebhookServiceName is the name of the Service used as part of the DNSName.
Defaults to kueue-webhook-service.</p>
</td>
</tr>
<tr><td><code>webhookSecretName</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>WebhookSecretName is the name of the Secret used to store CA and server certs.
Defaults to kueue-webhook-server-cert.</p>
</td>
</tr>
</tbody>
</table>

## `MultiKueue`     {#MultiKueue}
    

**Appears in:**




<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>gcInterval</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#duration-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.Duration</code></a>
</td>
<td>
   <p>GCInterval defines the time interval between two consecutive garbage collection runs.
Defaults to 1min. If 0, the garbage collection is disabled.</p>
</td>
</tr>
<tr><td><code>origin</code><br/>
<code>string</code>
</td>
<td>
   <p>Origin defines a label value used to track the creator of workloads in the worker
clusters.
This is used by multikueue in components like its garbage collector to identify
remote objects that ware created by this multikueue manager cluster and delete
them if their local counterpart no longer exists.</p>
</td>
</tr>
<tr><td><code>workerLostTimeout</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#duration-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.Duration</code></a>
</td>
<td>
   <p>WorkerLostTimeout defines the time a local workload's multikueue admission check state is kept Ready
if the connection with its reserving worker cluster is lost.</p>
<p>Defaults to 15 minutes.</p>
</td>
</tr>
<tr><td><code>dispatcherName</code><br/>
<code>string</code>
</td>
<td>
   <p>DispatcherName defines the dispatcher responsible for selecting worker clusters to handle the workload.</p>
<ul>
<li>If specified, the workload will be handled by the named dispatcher.</li>
<li>If not specified, the workload will be handled by the default (&quot;kueue.x-k8s.io/multikueue-dispatcher-all-at-once&quot;) dispatcher.</li>
</ul>
</td>
</tr>
</tbody>
</table>

## `ObjectRetentionPolicies`     {#ObjectRetentionPolicies}
    

**Appears in:**



<p>ObjectRetentionPolicies holds retention settings for different object types.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>workloads</code><br/>
<a href="#WorkloadRetentionPolicy"><code>WorkloadRetentionPolicy</code></a>
</td>
<td>
   <p>Workloads configures retention for Workloads.
A nil value disables automatic deletion of Workloads.</p>
</td>
</tr>
</tbody>
</table>

## `PodIntegrationOptions`     {#PodIntegrationOptions}
    

**Appears in:**

- [Integrations](#Integrations)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>namespaceSelector</code> <B>[Required]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#labelselector-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.LabelSelector</code></a>
</td>
<td>
   <p>NamespaceSelector can be used to omit some namespaces from pod reconciliation</p>
</td>
</tr>
<tr><td><code>podSelector</code> <B>[Required]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#labelselector-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.LabelSelector</code></a>
</td>
<td>
   <p>PodSelector can be used to choose what pods to reconcile</p>
</td>
</tr>
</tbody>
</table>

## `PreemptionStrategy`     {#PreemptionStrategy}
    
(Alias of `string`)

**Appears in:**

- [FairSharing](#FairSharing)





## `QueueVisibility`     {#QueueVisibility}
    

**Appears in:**




<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>clusterQueues</code> <B>[Required]</B><br/>
<a href="#ClusterQueueVisibility"><code>ClusterQueueVisibility</code></a>
</td>
<td>
   <p>ClusterQueues is configuration to expose the information
about the top pending workloads in the cluster queue.</p>
</td>
</tr>
<tr><td><code>updateIntervalSeconds</code> <B>[Required]</B><br/>
<code>int32</code>
</td>
<td>
   <p>UpdateIntervalSeconds specifies the time interval for updates to the structure
of the top pending workloads in the queues.
The minimum value is 1.
Defaults to 5.</p>
</td>
</tr>
</tbody>
</table>

## `RequeuingStrategy`     {#RequeuingStrategy}
    

**Appears in:**

- [WaitForPodsReady](#WaitForPodsReady)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>timestamp</code><br/>
<a href="#RequeuingTimestamp"><code>RequeuingTimestamp</code></a>
</td>
<td>
   <p>Timestamp defines the timestamp used for re-queuing a Workload
that was evicted due to Pod readiness. The possible values are:</p>
<ul>
<li><code>Eviction</code> (default) indicates from Workload <code>Evicted</code> condition with <code>PodsReadyTimeout</code> reason.</li>
<li><code>Creation</code> indicates from Workload .metadata.creationTimestamp.</li>
</ul>
</td>
</tr>
<tr><td><code>backoffLimitCount</code><br/>
<code>int32</code>
</td>
<td>
   <p>BackoffLimitCount defines the maximum number of re-queuing retries.
Once the number is reached, the workload is deactivated (<code>.spec.activate</code>=<code>false</code>).
When it is null, the workloads will repeatedly and endless re-queueing.</p>
<p>Every backoff duration is about &quot;b*2^(n-1)+Rand&quot; where:</p>
<ul>
<li>&quot;b&quot; represents the base set by &quot;BackoffBaseSeconds&quot; parameter,</li>
<li>&quot;n&quot; represents the &quot;workloadStatus.requeueState.count&quot;,</li>
<li>&quot;Rand&quot; represents the random jitter.
During this time, the workload is taken as an inadmissible and
other workloads will have a chance to be admitted.
By default, the consecutive requeue delays are around: (60s, 120s, 240s, ...).</li>
</ul>
<p>Defaults to null.</p>
</td>
</tr>
<tr><td><code>backoffBaseSeconds</code><br/>
<code>int32</code>
</td>
<td>
   <p>BackoffBaseSeconds defines the base for the exponential backoff for
re-queuing an evicted workload.</p>
<p>Defaults to 60.</p>
</td>
</tr>
<tr><td><code>backoffMaxSeconds</code><br/>
<code>int32</code>
</td>
<td>
   <p>BackoffMaxSeconds defines the maximum backoff time to re-queue an evicted workload.</p>
<p>Defaults to 3600.</p>
</td>
</tr>
</tbody>
</table>

## `RequeuingTimestamp`     {#RequeuingTimestamp}
    
(Alias of `string`)

**Appears in:**

- [RequeuingStrategy](#RequeuingStrategy)





## `ResourceTransformation`     {#ResourceTransformation}
    

**Appears in:**

- [Resources](#Resources)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>input</code> <B>[Required]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#resourcename-v1-core"><code>k8s.io/api/core/v1.ResourceName</code></a>
</td>
<td>
   <p>Input is the name of the input resource.</p>
</td>
</tr>
<tr><td><code>strategy</code> <B>[Required]</B><br/>
<a href="#ResourceTransformationStrategy"><code>ResourceTransformationStrategy</code></a>
</td>
<td>
   <p>Strategy specifies if the input resource should be replaced or retained.
Defaults to Retain</p>
</td>
</tr>
<tr><td><code>outputs</code> <B>[Required]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#resourcelist-v1-core"><code>k8s.io/api/core/v1.ResourceList</code></a>
</td>
<td>
   <p>Outputs specifies the output resources and quantities per unit of input resource.
An empty Outputs combined with a <code>Replace</code> Strategy causes the Input resource to be ignored by Kueue.</p>
</td>
</tr>
</tbody>
</table>

## `ResourceTransformationStrategy`     {#ResourceTransformationStrategy}
    
(Alias of `string`)

**Appears in:**

- [ResourceTransformation](#ResourceTransformation)





## `Resources`     {#Resources}
    

**Appears in:**




<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>excludeResourcePrefixes</code> <B>[Required]</B><br/>
<code>[]string</code>
</td>
<td>
   <p>ExcludedResourcePrefixes defines which resources should be ignored by Kueue</p>
</td>
</tr>
<tr><td><code>transformations</code> <B>[Required]</B><br/>
<a href="#ResourceTransformation"><code>[]ResourceTransformation</code></a>
</td>
<td>
   <p>Transformations defines how to transform PodSpec resources into Workload resource requests.
This is intended to be a map with Input as the key (enforced by validation code)</p>
</td>
</tr>
</tbody>
</table>

## `WaitForPodsReady`     {#WaitForPodsReady}
    

**Appears in:**



<p>WaitForPodsReady defines configuration for the Wait For Pods Ready feature,
which is used to ensure that all Pods are ready within the specified time.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>enable</code> <B>[Required]</B><br/>
<code>bool</code>
</td>
<td>
   <p>Enable indicates whether to enable wait for pods ready feature.
Defaults to false.</p>
</td>
</tr>
<tr><td><code>timeout</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#duration-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.Duration</code></a>
</td>
<td>
   <p>Timeout defines the time for an admitted workload to reach the
PodsReady=true condition. When the timeout is exceeded, the workload
evicted and requeued in the same cluster queue.
Defaults to 5min.</p>
</td>
</tr>
<tr><td><code>blockAdmission</code> <B>[Required]</B><br/>
<code>bool</code>
</td>
<td>
   <p>BlockAdmission when true, cluster queue will block admissions for all
subsequent jobs until the jobs reach the PodsReady=true condition.
This setting is only honored when <code>Enable</code> is set to true.</p>
</td>
</tr>
<tr><td><code>requeuingStrategy</code><br/>
<a href="#RequeuingStrategy"><code>RequeuingStrategy</code></a>
</td>
<td>
   <p>RequeuingStrategy defines the strategy for requeuing a Workload.</p>
</td>
</tr>
<tr><td><code>recoveryTimeout</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#duration-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.Duration</code></a>
</td>
<td>
   <p>RecoveryTimeout defines an opt-in timeout, measured since the
last transition to the PodsReady=false condition after a Workload is Admitted and running.
Such a transition may happen when a Pod failed and the replacement Pod
is awaited to be scheduled.
After exceeding the timeout the corresponding job gets suspended again
and requeued after the backoff delay. The timeout is enforced only if waitForPodsReady.enable=true.
If not set, there is no timeout.</p>
</td>
</tr>
</tbody>
</table>

## `WorkloadRetentionPolicy`     {#WorkloadRetentionPolicy}
    

**Appears in:**

- [ObjectRetentionPolicies](#ObjectRetentionPolicies)


<p>WorkloadRetentionPolicy defines the policies for when Workloads should be deleted.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>afterFinished</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#duration-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.Duration</code></a>
</td>
<td>
   <p>AfterFinished is the duration to wait after a Workload finishes
before deleting it.
A duration of 0 will delete immediately.
A nil value disables automatic deletion.
Represented using metav1.Duration (e.g. &quot;10m&quot;, &quot;1h30m&quot;).</p>
</td>
</tr>
<tr><td><code>afterDeactivatedByKueue</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#duration-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.Duration</code></a>
</td>
<td>
   <p>AfterDeactivatedByKueue is the duration to wait after <em>any</em> Kueue-managed Workload
(such as a Job, JobSet, or other custom workload types) has been marked
as deactivated by Kueue before automatically deleting it.
Deletion of deactivated workloads may cascade to objects not created by
Kueue, since deleting the parent Workload owner (e.g. JobSet) can trigger
garbage-collection of dependent resources.
A duration of 0 will delete immediately.
A nil value disables automatic deletion.
Represented using metav1.Duration (e.g. &quot;10m&quot;, &quot;1h30m&quot;).</p>
</td>
</tr>
</tbody>
</table>