---
title: Kueue Configuration v1beta2 API
content_type: tool-reference
package: config.kueue.x-k8s.io/v1beta2
auto_generated: true
description: Generated API reference documentation for config.kueue.x-k8s.io/v1beta2.
---


## Resource Types 


  

## `AdmissionFairSharing`     {#config-kueue-x-k8s-io-v1beta2-AdmissionFairSharing}
    

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

## `ClientConnection`     {#config-kueue-x-k8s-io-v1beta2-ClientConnection}
    

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

## `ClusterProfile`     {#config-kueue-x-k8s-io-v1beta2-ClusterProfile}
    

**Appears in:**

- [MultiKueue](#config-kueue-x-k8s-io-v1beta2-MultiKueue)


<p>ClusterProfile defines configuration for using the ClusterProfile API in MultiKueue.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>credentialsProviders</code> <B>[Required]</B><br/>
<a href="#config-kueue-x-k8s-io-v1beta2-ClusterProfileCredentialsProvider"><code>[]ClusterProfileCredentialsProvider</code></a>
</td>
<td>
   <p>CredentialsProviders defines a list of providers to obtain credentials of worker clusters
using the ClusterProfile API.</p>
</td>
</tr>
</tbody>
</table>

## `ClusterProfileCredentialsProvider`     {#config-kueue-x-k8s-io-v1beta2-ClusterProfileCredentialsProvider}
    

**Appears in:**

- [ClusterProfile](#config-kueue-x-k8s-io-v1beta2-ClusterProfile)


<p>ClusterProfileCredentialsProvider defines a credentials provider in the ClusterProfile API.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>Name is the name of the provider.</p>
</td>
</tr>
<tr><td><code>execConfig</code> <B>[Required]</B><br/>
<a href="https://pkg.go.dev/k8s.io/client-go/tools/clientcmd/api#ExecConfig"><code>k8s.io/client-go/tools/clientcmd/api.ExecConfig</code></a>
</td>
<td>
   <p>ExecConfig is the exec configuration to obtain credentials.</p>
</td>
</tr>
</tbody>
</table>

## `ControllerConfigurationSpec`     {#config-kueue-x-k8s-io-v1beta2-ControllerConfigurationSpec}
    

**Appears in:**

- [ControllerManager](#config-kueue-x-k8s-io-v1beta2-ControllerManager)


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

## `ControllerHealth`     {#config-kueue-x-k8s-io-v1beta2-ControllerHealth}
    

**Appears in:**

- [ControllerManager](#config-kueue-x-k8s-io-v1beta2-ControllerManager)


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

## `ControllerManager`     {#config-kueue-x-k8s-io-v1beta2-ControllerManager}
    

**Appears in:**




<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>webhook</code><br/>
<a href="#config-kueue-x-k8s-io-v1beta2-ControllerWebhook"><code>ControllerWebhook</code></a>
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
<a href="#config-kueue-x-k8s-io-v1beta2-ControllerMetrics"><code>ControllerMetrics</code></a>
</td>
<td>
   <p>Metrics contains the controller metrics configuration</p>
</td>
</tr>
<tr><td><code>health</code><br/>
<a href="#config-kueue-x-k8s-io-v1beta2-ControllerHealth"><code>ControllerHealth</code></a>
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
<a href="#config-kueue-x-k8s-io-v1beta2-ControllerConfigurationSpec"><code>ControllerConfigurationSpec</code></a>
</td>
<td>
   <p>Controller contains global configuration options for controllers
registered within this manager.</p>
</td>
</tr>
<tr><td><code>tls</code><br/>
<a href="#config-kueue-x-k8s-io-v1beta2-TLSOptions"><code>TLSOptions</code></a>
</td>
<td>
   <p>TLS contains TLS security settings for all Kueue API servers
(webhooks, metrics, and visibility).</p>
</td>
</tr>
</tbody>
</table>

## `ControllerMetrics`     {#config-kueue-x-k8s-io-v1beta2-ControllerMetrics}
    

**Appears in:**

- [ControllerManager](#config-kueue-x-k8s-io-v1beta2-ControllerManager)


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

## `ControllerWebhook`     {#config-kueue-x-k8s-io-v1beta2-ControllerWebhook}
    

**Appears in:**

- [ControllerManager](#config-kueue-x-k8s-io-v1beta2-ControllerManager)


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

## `DeviceClassMapping`     {#config-kueue-x-k8s-io-v1beta2-DeviceClassMapping}
    

**Appears in:**

- [Resources](#config-kueue-x-k8s-io-v1beta2-Resources)


<p>DeviceClassMapping holds device class to logical resource mappings
for Dynamic Resource Allocation support.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#resourcename-v1-core"><code>k8s.io/api/core/v1.ResourceName</code></a>
</td>
<td>
   <p>Name is referenced in ClusterQueue.nominalQuota and Workload status.
Must be a valid fully qualified name consisting of an optional DNS subdomain prefix
followed by a slash and a DNS label, or just a DNS label.
DNS labels consist of lower-case alphanumeric characters or hyphens,
and must start and end with an alphanumeric character.
DNS subdomain prefixes follow the same rules as DNS labels but can contain periods.
The total length must not exceed 253 characters.</p>
</td>
</tr>
<tr><td><code>deviceClassNames</code> <B>[Required]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#resourcename-v1-core"><code>[]k8s.io/api/core/v1.ResourceName</code></a>
</td>
<td>
   <p>DeviceClassNames enumerates the DeviceClasses represented by this resource name.
Each device class name must be a valid qualified name consisting of an optional DNS subdomain prefix
followed by a slash and a DNS label, or just a DNS label.
DNS labels consist of lower-case alphanumeric characters or hyphens,
and must start and end with an alphanumeric character.
DNS subdomain prefixes follow the same rules as DNS labels but can contain periods.
The total length of each name must not exceed 253 characters.</p>
</td>
</tr>
</tbody>
</table>

## `FairSharing`     {#config-kueue-x-k8s-io-v1beta2-FairSharing}
    

**Appears in:**




<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>preemptionStrategies</code> <B>[Required]</B><br/>
<a href="#config-kueue-x-k8s-io-v1beta2-PreemptionStrategy"><code>[]PreemptionStrategy</code></a>
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
newest start time first.</li>
</ul>
</td>
</tr>
</tbody>
</table>

## `Integrations`     {#config-kueue-x-k8s-io-v1beta2-Integrations}
    

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
<li>&quot;ray.io/rayservice&quot;</li>
<li>&quot;ray.io/raycluster&quot;</li>
<li>&quot;jobset.x-k8s.io/jobset&quot;</li>
<li>&quot;kubeflow.org/paddlejob&quot;</li>
<li>&quot;kubeflow.org/pytorchjob&quot;</li>
<li>&quot;kubeflow.org/tfjob&quot;</li>
<li>&quot;kubeflow.org/xgboostjob&quot;</li>
<li>&quot;kubeflow.org/jaxjob&quot;</li>
<li>&quot;trainer.kubeflow.org/trainjob&quot;</li>
<li>&quot;workload.codeflare.dev/appwrapper&quot;</li>
<li>&quot;pod&quot;</li>
<li>&quot;deployment&quot;</li>
<li>&quot;statefulset&quot;</li>
<li>&quot;leaderworkerset.x-k8s.io/leaderworkerset&quot;</li>
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

## `InternalCertManagement`     {#config-kueue-x-k8s-io-v1beta2-InternalCertManagement}
    

**Appears in:**




<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>enable</code> <B>[Required]</B><br/>
<code>bool</code>
</td>
<td>
   <p>Enable controls the use of internal cert management for the webhook,
metrics and visibility endpoints.
When enabled Kueue is using libraries to generate and
self-sign the certificates.
When disabled, you need to provide the certificates for
the webhooks, metrics and visibility through a third party certificate
This secret is mounted to the kueue controller manager pod. The mount
path for webhooks is /tmp/k8s-webhook-server/serving-certs, for
metrics endpoint the expected path is <code>/etc/kueue/metrics/certs</code> and for
visibility endpoint the expected path is <code>/visibility</code>.
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

## `MultiKueue`     {#config-kueue-x-k8s-io-v1beta2-MultiKueue}
    

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
<tr><td><code>externalFrameworks</code><br/>
<a href="#config-kueue-x-k8s-io-v1beta2-MultiKueueExternalFramework"><code>[]MultiKueueExternalFramework</code></a>
</td>
<td>
   <p>ExternalFrameworks defines a list of external frameworks that should be supported
by the generic MultiKueue adapter. Each entry defines how to handle a specific
GroupVersionKind (GVK) for MultiKueue operations.</p>
</td>
</tr>
<tr><td><code>clusterProfile</code><br/>
<a href="#config-kueue-x-k8s-io-v1beta2-ClusterProfile"><code>ClusterProfile</code></a>
</td>
<td>
   <p>ClusterProfile defines configuration for using the ClusterProfile API.</p>
</td>
</tr>
</tbody>
</table>

## `MultiKueueExternalFramework`     {#config-kueue-x-k8s-io-v1beta2-MultiKueueExternalFramework}
    

**Appears in:**

- [MultiKueue](#config-kueue-x-k8s-io-v1beta2-MultiKueue)


<p>MultiKueueExternalFramework defines a framework that is not built-in.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>name</code> <B>[Required]</B><br/>
<code>string</code>
</td>
<td>
   <p>Name is the GVK of the resource that are
managed by external controllers
the expected format is <code>kind.version.group</code>.</p>
</td>
</tr>
</tbody>
</table>

## `ObjectRetentionPolicies`     {#config-kueue-x-k8s-io-v1beta2-ObjectRetentionPolicies}
    

**Appears in:**



<p>ObjectRetentionPolicies holds retention settings for different object types.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>workloads</code><br/>
<a href="#config-kueue-x-k8s-io-v1beta2-WorkloadRetentionPolicy"><code>WorkloadRetentionPolicy</code></a>
</td>
<td>
   <p>Workloads configures retention for Workloads.
A nil value disables automatic deletion of Workloads.</p>
</td>
</tr>
</tbody>
</table>

## `PreemptionStrategy`     {#config-kueue-x-k8s-io-v1beta2-PreemptionStrategy}
    
(Alias of `string`)

**Appears in:**

- [FairSharing](#config-kueue-x-k8s-io-v1beta2-FairSharing)





## `RequeuingStrategy`     {#config-kueue-x-k8s-io-v1beta2-RequeuingStrategy}
    

**Appears in:**

- [WaitForPodsReady](#config-kueue-x-k8s-io-v1beta2-WaitForPodsReady)



<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>timestamp</code><br/>
<a href="#config-kueue-x-k8s-io-v1beta2-RequeuingTimestamp"><code>RequeuingTimestamp</code></a>
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

## `RequeuingTimestamp`     {#config-kueue-x-k8s-io-v1beta2-RequeuingTimestamp}
    
(Alias of `string`)

**Appears in:**

- [RequeuingStrategy](#config-kueue-x-k8s-io-v1beta2-RequeuingStrategy)





## `ResourceTransformation`     {#config-kueue-x-k8s-io-v1beta2-ResourceTransformation}
    

**Appears in:**

- [Resources](#config-kueue-x-k8s-io-v1beta2-Resources)



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
<a href="#config-kueue-x-k8s-io-v1beta2-ResourceTransformationStrategy"><code>ResourceTransformationStrategy</code></a>
</td>
<td>
   <p>Strategy specifies if the input resource should be replaced or retained.
Defaults to Retain</p>
</td>
</tr>
<tr><td><code>multiplyBy</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#resourcename-v1-core"><code>k8s.io/api/core/v1.ResourceName</code></a>
</td>
<td>
   <p>MultiplyBy indicates the resource name requested by a workload, if
specified.
The requested amount of the resource is used to multiply the requested
amount of the resource indicated by the &quot;input&quot; field.</p>
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

## `ResourceTransformationStrategy`     {#config-kueue-x-k8s-io-v1beta2-ResourceTransformationStrategy}
    
(Alias of `string`)

**Appears in:**

- [ResourceTransformation](#config-kueue-x-k8s-io-v1beta2-ResourceTransformation)





## `Resources`     {#config-kueue-x-k8s-io-v1beta2-Resources}
    

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
<a href="#config-kueue-x-k8s-io-v1beta2-ResourceTransformation"><code>[]ResourceTransformation</code></a>
</td>
<td>
   <p>Transformations defines how to transform PodSpec resources into Workload resource requests.
This is intended to be a map with Input as the key (enforced by validation code)</p>
</td>
</tr>
<tr><td><code>deviceClassMappings</code><br/>
<a href="#config-kueue-x-k8s-io-v1beta2-DeviceClassMapping"><code>[]DeviceClassMapping</code></a>
</td>
<td>
   <p>DeviceClassMappings defines mappings from device classes to logical resources
for Dynamic Resource Allocation support.</p>
</td>
</tr>
</tbody>
</table>

## `TLSOptions`     {#config-kueue-x-k8s-io-v1beta2-TLSOptions}
    

**Appears in:**

- [ControllerManager](#config-kueue-x-k8s-io-v1beta2-ControllerManager)


<p>TLSOptions defines TLS security settings for Kueue servers</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>minVersion</code><br/>
<code>string</code>
</td>
<td>
   <p>minVersion is the minimum TLS version supported.
Values are from tls package constants (https://golang.org/pkg/crypto/tls/#pkg-constants).
This field is only valid when TLSOptions is set to true.
The default would be to not set this value and inherit golang settings.</p>
</td>
</tr>
<tr><td><code>cipherSuites</code><br/>
<code>[]string</code>
</td>
<td>
   <p>cipherSuites is the list of allowed cipher suites for the server.
Note that TLS 1.3 ciphersuites are not configurable.
Values are from tls package constants (https://golang.org/pkg/crypto/tls/#pkg-constants).
The default would be to not set this value and inherit golang settings.</p>
</td>
</tr>
</tbody>
</table>

## `WaitForPodsReady`     {#config-kueue-x-k8s-io-v1beta2-WaitForPodsReady}
    

**Appears in:**



<p>WaitForPodsReady defines configuration for the Wait For Pods Ready feature,
which is used to ensure that all Pods are ready within the specified time.</p>


<table class="table">
<thead><tr><th width="30%">Field</th><th>Description</th></tr></thead>
<tbody>
    
  
<tr><td><code>timeout</code> <B>[Required]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#duration-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.Duration</code></a>
</td>
<td>
   <p>Timeout defines the time for an admitted workload to reach the
PodsReady=true condition. When the timeout is exceeded, the workload
evicted and requeued in the same cluster queue.</p>
</td>
</tr>
<tr><td><code>blockAdmission</code><br/>
<code>bool</code>
</td>
<td>
   <p>BlockAdmission when true, the cluster queue will block admissions for all
subsequent jobs until the jobs reach the PodsReady=true condition.
Defaults to false.</p>
</td>
</tr>
<tr><td><code>requeuingStrategy</code><br/>
<a href="#config-kueue-x-k8s-io-v1beta2-RequeuingStrategy"><code>RequeuingStrategy</code></a>
</td>
<td>
   <p>RequeuingStrategy defines the strategy for requeuing a Workload.</p>
</td>
</tr>
<tr><td><code>recoveryTimeout</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#duration-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.Duration</code></a>
</td>
<td>
   <p>RecoveryTimeout defines a timeout, measured since the
last transition to the PodsReady=false condition after a Workload is Admitted and running.
Such a transition may happen when a Pod failed and the replacement Pod
is awaited to be scheduled.
After exceeding the timeout the corresponding job gets suspended again
and requeued after the backoff delay.
Defaults to the value of timeout. Setting to &quot;0s&quot; disables recovery timeout checking.</p>
</td>
</tr>
</tbody>
</table>

## `WorkloadRetentionPolicy`     {#config-kueue-x-k8s-io-v1beta2-WorkloadRetentionPolicy}
    

**Appears in:**

- [ObjectRetentionPolicies](#config-kueue-x-k8s-io-v1beta2-ObjectRetentionPolicies)


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
  