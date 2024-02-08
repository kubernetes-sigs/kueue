---
title: Kueue Configuration API
content_type: tool-reference
package: /v1beta1
auto_generated: true
description: Generated API reference documentation for Kueue Configuration.
---


## Resource Types 


- [Configuration](#Configuration)
  
    
    

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
batch/v1.Jobs that don't set the annotation kueue.x-k8s.io/queue-name.
If set to true, then those jobs will be suspended and never started unless
they are assigned a queue and eventually admitted. This also applies to
jobs created before starting the kueue controller.
Defaults to false; therefore, those jobs are not managed and if they are created
unsuspended, they will start immediately.</p>
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
   <p>WaitForPodsReady is configuration to provide simple all-or-nothing
scheduling semantics for jobs to ensure they get resources assigned.
This is achieved by blocking the start of new jobs until the previously
started job has all pods running (ready).</p>
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
pending workloads.</p>
</td>
</tr>
<tr><td><code>multiKueue</code> <B>[Required]</B><br/>
<a href="#MultiKueue"><code>MultiKueue</code></a>
</td>
<td>
   <p>MultiKueue controls the behaviour of the MultiKueue AdmissionCheck Controller.</p>
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
<li>&quot;kubeflow.org/mxjob&quot;</li>
<li>&quot;kubeflow.org/paddlejob&quot;</li>
<li>&quot;kubeflow.org/pytorchjob&quot;</li>
<li>&quot;kubeflow.org/tfjob&quot;</li>
<li>&quot;kubeflow.org/xgboostjob&quot;</li>
<li>&quot;pod&quot;</li>
</ul>
</td>
</tr>
<tr><td><code>podOptions</code> <B>[Required]</B><br/>
<a href="#PodIntegrationOptions"><code>PodIntegrationOptions</code></a>
</td>
<td>
   <p>PodOptions defines kueue controller behaviour for pod objects</p>
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
   <p>Enable controls whether to enable internal cert management or not.
Defaults to true. If you want to use a third-party management, e.g. cert-manager,
set it to false. See the user guide for more information.</p>
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
   <p>Timestamp defines the timestamp used for requeuing a Workload
that was evicted due to Pod readiness. The possible values are:</p>
<ul>
<li><code>Eviction</code> (default): indicates from Workload .metadata.creationTimestamp.</li>
<li><code>Creation</code>: indicates from Workload .status.conditions.</li>
</ul>
</td>
</tr>
<tr><td><code>backoffLimitCount</code><br/>
<code>int32</code>
</td>
<td>
   <p>BackoffLimitCount defines the maximum number of requeuing retries.
When the number is reached, the workload is deactivated (<code>.spec.activate</code>=<code>false</code>).</p>
<p>Every backoff duration is calculated by &quot;1.41284738^(n-1)+Rand&quot;
where the &quot;n&quot; represents the &quot;workloadStatus.requeueState.count&quot;, and the &quot;Rand&quot; represents the random jitter.
Considering the &quot;.waitForPodsReady.timeout&quot; (default: 300 seconds),
this indicates that an evicted workload with PodsReadyTimeout reason is continued re-queuing for
the &quot;t(n+1) + Rand + SUM[k=1,n]1.41284738^(k-1)&quot; seconds where the &quot;t&quot; represents &quot;waitForPodsReady.timeout&quot;.
Given that the &quot;backoffLimitCount&quot; equals &quot;30&quot; and the &quot;waitForPodsReady.timeout&quot; equals &quot;300&quot; (default),
the result equals 24 hours (+Rand seconds).</p>
<p>Defaults to null.</p>
</td>
</tr>
</tbody>
</table>

## `RequeuingTimestamp`     {#RequeuingTimestamp}
    
(Alias of `string`)

**Appears in:**

- [RequeuingStrategy](#RequeuingStrategy)





## `WaitForPodsReady`     {#WaitForPodsReady}
    

**Appears in:**




<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>enable</code> <B>[Required]</B><br/>
<code>bool</code>
</td>
<td>
   <p>Enable when true, indicates that each admitted workload
blocks the admission of all other workloads from all queues until it is in the
<code>PodsReady</code> condition. If false, all workloads start as soon as they are
admitted and do not block admission of other workloads. The PodsReady
condition is only added if this setting is enabled. It defaults to false.</p>
</td>
</tr>
<tr><td><code>timeout</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#duration-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.Duration</code></a>
</td>
<td>
   <p>Timeout defines the time for an admitted workload to reach the
PodsReady=true condition. When the timeout is reached, the workload admission
is cancelled and requeued in the same cluster queue. Defaults to 5min.</p>
</td>
</tr>
<tr><td><code>blockAdmission</code> <B>[Required]</B><br/>
<code>bool</code>
</td>
<td>
   <p>BlockAdmission when true, cluster queue will block admissions for all subsequent jobs
until the jobs reach the PodsReady=true condition. It defaults to false if Enable is false
and defaults to true otherwise.</p>
</td>
</tr>
<tr><td><code>requeuingStrategy</code><br/>
<a href="#RequeuingStrategy"><code>RequeuingStrategy</code></a>
</td>
<td>
   <p>RequeuingStrategy defines the strategy for requeuing a Workload.</p>
</td>
</tr>
</tbody>
</table>