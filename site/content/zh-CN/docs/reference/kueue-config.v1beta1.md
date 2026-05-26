---
title: Kueue Configuration v1beta1 API
content_type: tool-reference
package: config.kueue.x-k8s.io/v1beta1
auto_generated: true
description: 为 config.kueue.x-k8s.io/v1beta1 生成的 API 参考文档。
---

## 资源类型

- [Configuration](#config-kueue-x-k8s-io-v1beta1-Configuration)

## `Configuration`     {#config-kueue-x-k8s-io-v1beta1-Configuration}
    
<p>Configuration 是 kueueconfigurations API 的 Schema</p>

<table class="table">
<thead><tr><th width="30%">字段</th><th>描述</th></tr></thead>
<tbody>
    
<tr><td><code>apiVersion</code><br/>string</td><td><code>config.kueue.x-k8s.io/v1beta1</code></td></tr>
<tr><td><code>kind</code><br/>string</td><td><code>Configuration</code></td></tr>
    
<tr><td><code>namespace</code> <B>[必需]</B><br/>
<code>string</code>
</td>
<td>
   <p>Namespace 是 Kueue 部署的命名空间。它用作 Webhook Service 的 DNSName 的一部分。
如果未设置，该值将从文件 /var/run/secrets/kubernetes.io/serviceaccount/namespace 中设置
如果文件不存在，默认值为 kueue-system。</p>
</td>
</tr>
<tr><td><code>ControllerManager</code> <B>[必需]</B><br/>
<a href="#config-kueue-x-k8s-io-v1beta1-ControllerManager"><code>ControllerManager</code></a>
</td>
<td>(包含 <code>ControllerManager</code> 的成员。)
   <p>ControllerManager 返回控制器的配置</p>
</td>
</tr>
<tr><td><code>manageJobsWithoutQueueName</code> <B>[必需]</B><br/>
<code>bool</code>
</td>
<td>
   <p>ManageJobsWithoutQueueName 控制 Kueue 是否协调
未设置标签 kueue.x-k8s.io/queue-name 的作业。
如果设置为 true，则这些作业将被挂起，除非
它们被分配队列并最终被准入，否则永远不会启动。这也适用于
在启动 kueue 控制器之前创建的作业。
默认为 false；因此，这些作业不受管理，如果它们被创建
未挂起，它们将立即开始。</p>
</td>
</tr>
<tr><td><code>managedJobsNamespaceSelector</code> <B>[必需]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#labelselector-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.LabelSelector</code></a>
</td>
<td>
   <p>ManagedJobsNamespaceSelector 提供了一种基于命名空间的机制，用于豁免作业
不受 Kueue 管理。</p>
<p>它为基于 Pod 的集成（pod、deployment、statefulset 等）提供了强大的豁免，
对于基于 Pod 的集成，只有其命名空间与 ManagedJobsNamespaceSelector 匹配的作业才
有资格由 Kueue 管理。非匹配命名空间中的 Pod、deployment 等将
永远不会由 Kueue 管理，即使它们有 kueue.x-k8s.io/queue-name 标签。
这种强大的豁免确保 Kueue 不会干扰系统命名空间的基本操作。</p>
<p>对于所有其他集成，ManagedJobsNamespaceSelector 通过仅调节 ManageJobsWithoutQueueName 的效果来提供较弱的豁免。对于这些集成，
具有 kueue.x-k8s.io/queue-name 标签的作业将始终由 Kueue 管理。没有
kueue.x-k8s.io/queue-name 标签的作业只有在 ManageJobsWithoutQueueName 为 true 且作业的命名空间与 ManagedJobsNamespaceSelector 匹配时才会由 Kueue 管理。</p>
</td>
</tr>
<tr><td><code>internalCertManagement</code> <B>[必需]</B><br/>
<a href="#config-kueue-x-k8s-io-v1beta1-InternalCertManagement"><code>InternalCertManagement</code></a>
</td>
<td>
   <p>InternalCertManagement 是 internalCertManagement 的配置</p>
</td>
</tr>
<tr><td><code>waitForPodsReady</code> <B>[必需]</B><br/>
<a href="#config-kueue-x-k8s-io-v1beta1-WaitForPodsReady"><code>WaitForPodsReady</code></a>
</td>
<td>
   <p>WaitForPodsReady 是配置，为 Job 提供基于时间的全有或全无
调度语义，通过确保所有 pod 在指定时间内准备就绪（运行
并通过就绪探针）。如果超过超时，则工作负载被驱逐。</p>
</td>
</tr>
<tr><td><code>clientConnection</code> <B>[必需]</B><br/>
<a href="#config-kueue-x-k8s-io-v1beta1-ClientConnection"><code>ClientConnection</code></a>
</td>
<td>
   <p>ClientConnection 为 Kubernetes 提供额外的配置选项
API 服务器客户端。</p>
</td>
</tr>
<tr><td><code>integrations</code> <B>[必需]</B><br/>
<a href="#config-kueue-x-k8s-io-v1beta1-Integrations"><code>Integrations</code></a>
</td>
<td>
   <p>Integrations 为 AI/ML/Batch 框架提供配置选项
集成（包括 K8S job）。</p>
</td>
</tr>
<tr><td><code>queueVisibility</code> <B>[必需]</B><br/>
<a href="#config-kueue-x-k8s-io-v1beta1-QueueVisibility"><code>QueueVisibility</code></a>
</td>
<td>
   <p>QueueVisibility 是配置，用于暴露有关顶部的信息
待处理工作负载。</p>
<p>已弃用：此字段将在 v1beta2 中移除，使用 VisibilityOnDemand
(https://kueue.sigs.k8s.io/docs/tasks/manage/monitor_pending_workloads/pending_workloads_on_demand/)
代替。</p>
</td>
</tr>
<tr><td><code>multiKueue</code> <B>[必需]</B><br/>
<a href="#config-kueue-x-k8s-io-v1beta1-MultiKueue"><code>MultiKueue</code></a>
</td>
<td>
   <p>MultiKueue 控制 MultiKueue AdmissionCheck 控制器的行为。</p>
</td>
</tr>
<tr><td><code>fairSharing</code> <B>[必需]</B><br/>
<a href="#config-kueue-x-k8s-io-v1beta1-FairSharing"><code>FairSharing</code></a>
</td>
<td>
   <p>FairSharing 控制集群中的公平共享语义。</p>
</td>
</tr>
<tr><td><code>admissionFairSharing</code> <B>[必需]</B><br/>
<a href="#config-kueue-x-k8s-io-v1beta1-AdmissionFairSharing"><code>AdmissionFairSharing</code></a>
</td>
<td>
   <p>admissionFairSharing 表示在 <code>AdmissionTime</code> 模式下的 FairSharing 配置</p>
</td>
</tr>
<tr><td><code>resources</code> <B>[必需]</B><br/>
<a href="#config-kueue-x-k8s-io-v1beta1-Resources"><code>Resources</code></a>
</td>
<td>
   <p>Resources 为处理资源提供额外的配置选项。</p>
</td>
</tr>
<tr><td><code>featureGates</code> <B>[必需]</B><br/>
<code>map[string]bool</code>
</td>
<td>
   <p>FeatureGates 是特性名称到布尔值的映射，允许覆盖
特性的默认启用状态。该映射不能与通过命令行参数 "--feature-gates" 传递特性列表一起使用
用于 Kueue 部署。</p>
</td>
</tr>
<tr><td><code>objectRetentionPolicies</code><br/>
<a href="#config-kueue-x-k8s-io-v1beta1-ObjectRetentionPolicies"><code>ObjectRetentionPolicies</code></a>
</td>
<td>
   <p>ObjectRetentionPolicies 为 Kueue 管理的对象的自动删除提供配置选项。nil 值禁用所有自动删除。</p>
</td>
</tr>
</tbody>
</table>

## `AdmissionFairSharing`     {#config-kueue-x-k8s-io-v1beta1-AdmissionFairSharing}
    
**出现于：**

- [Configuration](#config-kueue-x-k8s-io-v1beta1-Configuration)

<table class="table">
<thead><tr><th width="30%">字段</th><th>描述</th></tr></thead>
<tbody>
    
<tr><td><code>usageHalfLifeTime</code> <B>[必需]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#duration-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.Duration</code></a>
</td>
<td>
   <p>usageHalfLifeTime 表示当前使用量衰减一半的时间
如果设置为 0，使用量将立即重置为 0。</p>
</td>
</tr>
<tr><td><code>usageSamplingInterval</code> <B>[必需]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#duration-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.Duration</code></a>
</td>
<td>
   <p>usageSamplingInterval 表示 Kueue 更新 FairSharingStatus 中 consumedResources 的频率
默认为 5 分钟。</p>
</td>
</tr>
<tr><td><code>resourceWeights</code> <B>[必需]</B><br/>
<code>map[ResourceName]float64</code>
</td>
<td>
   <p>resourceWeights 为资源分配权重，这些权重用于计算 LocalQueue 的
资源使用情况和工作负载顺序。
默认为 1。</p>
</td>
</tr>
</tbody>
</table>

## `ClientConnection`     {#config-kueue-x-k8s-io-v1beta1-ClientConnection}
    
**出现于：**

- [Configuration](#config-kueue-x-k8s-io-v1beta1-Configuration)

<table class="table">
<thead><tr><th width="30%">字段</th><th>描述</th></tr></thead>
<tbody>
    
<tr><td><code>qps</code> <B>[必需]</B><br/>
<code>float32</code>
</td>
<td>
   <p>QPS 控制 K8S api 服务器允许的每秒查询数
连接。</p>
<p>将此设置为负值将禁用客户端侧速率限制。</p>
</td>
</tr>
<tr><td><code>burst</code> <B>[必需]</B><br/>
<code>int32</code>
</td>
<td>
   <p>Burst 允许客户端超出其速率时积累额外查询。</p>
</td>
</tr>
</tbody>
</table>

## `ClusterQueueVisibility`     {#config-kueue-x-k8s-io-v1beta1-ClusterQueueVisibility}
    
**出现于：**

- [QueueVisibility](#config-kueue-x-k8s-io-v1beta1-QueueVisibility)

<table class="table">
<thead><tr><th width="30%">字段</th><th>描述</th></tr></thead>
<tbody>
    
<tr><td><code>maxCount</code> <B>[必需]</B><br/>
<code>int32</code>
</td>
<td>
   <p>MaxCount 表示在集群队列状态中暴露的待处理工作负载的最大数量。
当值设置为 0 时，ClusterQueue 可见性更新被禁用。
最大值为 4000。
默认为 10。</p>
</td>
</tr>
</tbody>
</table>

## `ControllerConfigurationSpec`     {#config-kueue-x-k8s-io-v1beta1-ControllerConfigurationSpec}
    
**出现于：**

- [ControllerManager](#config-kueue-x-k8s-io-v1beta1-ControllerManager)

<p>ControllerConfigurationSpec 定义了全局配置
向管理器注册的控制器。</p>

<table class="table">
<thead><tr><th width="30%">字段</th><th>描述</th></tr></thead>
<tbody>
  
<tr><td><code>groupKindConcurrency</code><br/>
<code>map[string]int</code>
</td>
<td>
   <p>GroupKindConcurrency 是从 Kind 到并发协调数的映射
该控制器允许。</p>
<p>当使用构建器实用程序在该管理器中注册控制器时，
用户必须在 For(...) 调用中指定控制器协调的类型。
如果传递的对象的 kind 与该映射中的键之一匹配，则该控制器的并发
设置为指定的数字。</p>
<p>键的格式应与 GroupKind.String() 一致，
例如 apps 组中的 ReplicaSet（无论版本）将是 <code>ReplicaSet.apps</code>。</p>
</td>
</tr>
<tr><td><code>cacheSyncTimeout</code><br/>
<a href="https://pkg.go.dev/time#Duration"><code>time.Duration</code></a>
</td>
<td>
   <p>CacheSyncTimeout 指的是等待同步缓存的时间限制。
如果未设置，默认为 2 分钟。</p>
</td>
</tr>
</tbody>
</table>

## `ControllerHealth`     {#config-kueue-x-k8s-io-v1beta1-ControllerHealth}
    
**出现于：**

- [ControllerManager](#config-kueue-x-k8s-io-v1beta1-ControllerManager)

<p>ControllerHealth 定义了健康配置。</p>

<table class="table">
<thead><tr><th width="30%">字段</th><th>描述</th></tr></thead>
<tbody>
  
<tr><td><code>healthProbeBindAddress</code><br/>
<code>string</code>
</td>
<td>
   <p>HealthProbeBindAddress 是控制器应绑定的 TCP 地址
用于提供健康探针
可以设置为 "0" 或 "" 以禁用健康探针的提供。</p>
</td>
</tr>
<tr><td><code>readinessEndpointName</code><br/>
<code>string</code>
</td>
<td>
   <p>ReadinessEndpointName，默认为 "readyz"</p>
</td>
</tr>
<tr><td><code>livenessEndpointName</code><br/>
<code>string</code>
</td>
<td>
   <p>LivenessEndpointName，默认为 "healthz"</p>
</td>
</tr>
</tbody>
</table>

## `ControllerManager`     {#config-kueue-x-k8s-io-v1beta1-ControllerManager}

**出现于：**

- [Configuration](#config-kueue-x-k8s-io-v1beta1-Configuration)

<table class="table">
<thead><tr><th width="30%">字段</th><th>描述</th></tr></thead>
<tbody>
    
<tr><td><code>webhook</code><br/>
<a href="#config-kueue-x-k8s-io-v1beta1-ControllerWebhook"><code>ControllerWebhook</code></a>
</td>
<td>
   <p>Webhook 包含控制器的 webhook 配置</p>
</td>
</tr>
<tr><td><code>leaderElection</code><br/>
<a href="https://pkg.go.dev/k8s.io/component-base/config/v1alpha1#LeaderElectionConfiguration"><code>k8s.io/component-base/config/v1alpha1.LeaderElectionConfiguration</code></a>
</td>
<td>
   <p>LeaderElection 是配置管理器时使用的 LeaderElection 配置
manager.Manager 领导者选举</p>
</td>
</tr>
<tr><td><code>metrics</code><br/>
<a href="#config-kueue-x-k8s-io-v1beta1-ControllerMetrics"><code>ControllerMetrics</code></a>
</td>
<td>
   <p>Metrics 包含控制器指标配置</p>
</td>
</tr>
<tr><td><code>health</code><br/>
<a href="#config-kueue-x-k8s-io-v1beta1-ControllerHealth"><code>ControllerHealth</code></a>
</td>
<td>
   <p>Health 包含控制器健康配置</p>
</td>
</tr>
<tr><td><code>pprofBindAddress</code><br/>
<code>string</code>
</td>
<td>
   <p>PprofBindAddress 是控制器应绑定的 TCP 地址
用于提供 pprof。
可以设置为 "" 或 "0" 以禁用 pprof 服务。
由于 pprof 可能包含敏感信息，在将其暴露给公众之前请确保对其进行保护。</p>
</td>
</tr>
<tr><td><code>controller</code><br/>
<a href="#config-kueue-x-k8s-io-v1beta1-ControllerConfigurationSpec"><code>ControllerConfigurationSpec</code></a>
</td>
<td>
   <p>Controller 包含控制器的全局配置选项
在此管理器中注册。</p>
</td>
</tr>
<tr><td><code>tls</code><br/>
<a href="#config-kueue-x-k8s-io-v1beta1-TLSOptions"><code>TLSOptions</code></a>
</td>
<td>
   <p>TLS 包含所有 Kueue API 服务器的 TLS 安全设置
(webhooks、metrics 和 visibility)。</p>
</td>
</tr>
</tbody>
</table>

## `ControllerMetrics`     {#config-kueue-x-k8s-io-v1beta1-ControllerMetrics}
    
**出现于：**

- [ControllerManager](#config-kueue-x-k8s-io-v1beta1-ControllerManager)

<p>ControllerMetrics 定义了指标配置。</p>

<table class="table">
<thead><tr><th width="30%">字段</th><th>描述</th></tr></thead>
<tbody>
    
<tr><td><code>bindAddress</code><br/>
<code>string</code>
</td>
<td>
<p>
BindAddress 是控制器应绑定的 TCP 地址用于提供 prometheus 指标。
可以设置为 "0" 以禁用指标服务。
</p>
</td>
</tr>
<tr><td><code>enableClusterQueueResources</code><br/>
<code>bool</code>
</td>
<td>
   <p>EnableClusterQueueResources，如果为 true，将报告集群队列资源使用情况和配额指标。</p>
</td>
</tr>
<tr><td><code>customLabels</code><br/>
<a href="#config-kueue-x-k8s-io-v1beta1-ControllerMetricsCustomLabel"><code>[]ControllerMetricsCustomLabel</code></a>
</td>
<td>
   <p>CustomLabels 是一个条目列表，其值将作为额外的
ClusterQueue、LocalQueue 和 Cohort 指标上的 Prometheus 标签。
需要 CustomMetricLabels 特性门控。</p>
</td>
</tr>
<tr><td><code>localQueueMetrics</code><br/>
<a href="#config-kueue-x-k8s-io-v1beta1-LocalQueueMetrics"><code>LocalQueueMetrics</code></a>
</td>
<td>
   <p>LocalQueueMetrics 是提供 LocalQueue 指标选项的配置。</p>
</td>
</tr>
</tbody>
</table>

## `ControllerMetricsCustomLabel`     {#config-kueue-x-k8s-io-v1beta1-ControllerMetricsCustomLabel}

**出现于：**

- [ControllerMetrics](#config-kueue-x-k8s-io-v1beta1-ControllerMetrics)

<p>ControllerMetricsCustomLabel 定义了一个 Kubernetes 标签或注解，用于提升
作为带有 "custom_" 前缀的 Prometheus 指标标签。</p>

<table class="table">
<thead><tr><th width="30%">字段</th><th>描述</th></tr></thead>
<tbody>
    
<tr><td><code>name</code> <B>[必需]</B><br/>
<code>string</code>
</td>
<td>
   <p>Name 用作构建 Prometheus 标签的后缀：Kueue
自动添加 "custom_" 前缀（例如，name: "team" 变为标签 "custom_team"）。
必须遵循 Prometheus 标签命名约定：[a-zA-Z_][a-zA-Z0-9_]*。</p>
</td>
</tr>
<tr><td><code>sourceLabelKey</code><br/>
<code>string</code>
</td>
<td>
   <p>SourceLabelKey 是从中读取值的 Kubernetes 标签键。
必须是有效的 Kubernetes 限定名称。
与 SourceAnnotationKey 互斥。
如果均未指定，默认为 Name。</p>
</td>
</tr>
<tr><td><code>sourceAnnotationKey</code><br/>
<code>string</code>
</td>
<td>
   <p>SourceAnnotationKey 是从中读取值的 Kubernetes 注解键。
必须是有效的 Kubernetes 限定名称。
与 SourceLabelKey 互斥。</p>
</td>
</tr>
</tbody>
</table>

## `ControllerWebhook`     {#config-kueue-x-k8s-io-v1beta1-ControllerWebhook}
    
**出现于：**

- [ControllerManager](#config-kueue-x-k8s-io-v1beta1-ControllerManager)

<p>ControllerWebhook 定义了控制器的 webhook 服务器。</p>

<table class="table">
<thead><tr><th width="30%">字段</th><th>描述</th></tr></thead>
<tbody>
    
<tr><td><code>port</code><br/>
<code>int</code>
</td>
<td>
   <p>Port 是 webhook 服务器服务的端口。
用于设置 webhook.Server.Port。</p>
</td>
</tr>
<tr><td><code>host</code><br/>
<code>string</code>
</td>
<td>
   <p>Host 是 webhook 服务器绑定的主机名。
用于设置 webhook.Server.Host。</p>
</td>
</tr>
<tr><td><code>certDir</code><br/>
<code>string</code>
</td>
<td>
   <p>CertDir 是包含服务器密钥和证书的目录。
如果未设置，webhook 服务器将在 {TempDir}/k8s-webhook-server/serving-certs 中查找服务器密钥和证书。服务器密钥和证书
必须分别命名为 tls.key 和 tls.crt。</p>
</td>
</tr>
</tbody>
</table>

## `DeviceClassMapping`     {#config-kueue-x-k8s-io-v1beta1-DeviceClassMapping}
    
**出现于：**

- [Resources](#config-kueue-x-k8s-io-v1beta1-Resources)

<p>DeviceClassMapping 保存设备类到逻辑资源的映射
用于动态资源分配支持。</p>

<table class="table">
<thead><tr><th width="30%">字段</th><th>描述</th></tr></thead>
<tbody>
     
<tr><td><code>name</code> <B>[必需]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#resourcename-v1-core"><code>k8s.io/api/core/v1.ResourceName</code></a>
</td>
<td>
   <p>Name 在 ClusterQueue.nominalQuota 和 Workload 状态中引用。
必须是有效的完全限定名称，由可选的 DNS 子域前缀组成
后跟斜杠和 DNS 标签，或仅 DNS 标签。
DNS 标签由小写字母数字字符或连字符组成，
并且必须以字母数字字符开头和结尾。
DNS 子域前缀遵循与 DNS 标签相同的规则，但可以包含句点。
总长度不得超过 253 个字符。</p>
</td>
</tr>
<tr><td><code>deviceClassNames</code> <B>[必需]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#resourcename-v1-core"><code>[]k8s.io/api/core/v1.ResourceName</code></a>
</td>
<td>
   <p>DeviceClassNames 枚举由此资源名称表示的 DeviceClasses。
每个设备类名称必须是有效的限定名称，由可选的 DNS 子域前缀组成
后跟斜杠和 DNS 标签，或仅 DNS 标签。
DNS 标签由小写字母数字字符或连字符组成，
并且必须以字母数字字符开头和结尾。
DNS 子域前缀遵循与 DNS 标签相同的规则，但可以包含句点。
每个名称的总长度不得超过 253 个字符。</p>
</td>
</tr>
</tbody>
</table>

## `FairSharing`     {#config-kueue-x-k8s-io-v1beta1-FairSharing}

**出现于：**

- [Configuration](#config-kueue-x-k8s-io-v1beta1-Configuration)

<table class="table">
<thead><tr><th width="30%">字段</th><th>描述</th></tr></thead>
<tbody>
    
<tr><td><code>enable</code> <B>[必需]</B><br/>
<code>bool</code>
</td>
<td>
   <p>enable 表示是否为所有队列启用公平共享。
默认为 false。</p>
</td>
</tr>
<tr><td><code>preemptionStrategies</code> <B>[必需]</B><br/>
<a href="#config-kueue-x-k8s-io-v1beta1-PreemptionStrategy"><code>[]PreemptionStrategy</code></a>
</td>
<td>
   <p>preemptionStrategies 表示抢占应满足哪些约束。
抢占算法只会在使用先前策略后传入工作负载（抢占者）不适合时才使用列表中的下一个策略。
可能的值为：</p>
<ul>
<li>LessThanOrEqualToFinalShare: 只有当抢占者 CQ 的份额
包含抢占者工作负载时小于或等于被抢占者 CQ 的份额
不包含要被抢占的工作负载。
此策略可能倾向于抢占被抢占者 CQ 中的较小工作负载，
无论优先级或开始时间如何，以努力保持 CQ 的份额
尽可能高。</li>
<li>LessThanInitialShare: 只有当抢占者 CQ 的份额
包含传入工作负载时严格小于被抢占者 CQ 的份额。
此策略不依赖于被抢占工作负载的份额使用情况。
因此，该策略选择首先抢占优先级最低和
开始时间最新的工作负载。
默认策略是 ["LessThanOrEqualToFinalShare", "LessThanInitialShare"]。</li>
</ul>
</td>
</tr>
</tbody>
</table>

## `Integrations`     {#config-kueue-x-k8s-io-v1beta1-Integrations}
    
**出现于：**

- [Configuration](#config-kueue-x-k8s-io-v1beta1-Configuration)

<table class="table">
<thead><tr><th width="30%">字段</th><th>描述</th></tr></thead>
<tbody>
  
<tr><td><code>frameworks</code> <B>[必需]</B><br/>
<code>[]string</code>
</td>
<td>
   <p>要启用的框架名称列表。
可能的选项：</p>
<ul>
<li>"batch/job"</li>
<li>"kubeflow.org/mpijob"</li>
<li>"ray.io/rayjob"</li>
<li>"ray.io/rayservice"</li>
<li>"ray.io/raycluster"</li>
<li>"jobset.x-k8s.io/jobset"</li>
<li>"kubeflow.org/paddlejob"</li>
<li>"kubeflow.org/pytorchjob"</li>
<li>"kubeflow.org/tfjob"</li>
<li>"kubeflow.org/xgboostjob"</li>
<li>"kubeflow.org/jaxjob"</li>
<li>"trainer.kubeflow.org/trainjob"</li>
<li>"workload.codeflare.dev/appwrapper"</li>
<li>"sparkoperator.k8s.io/sparkapplication"</li>
<li>"pod"</li>
<li>"deployment"</li>
<li>"statefulset"</li>
<li>"leaderworkerset.x-k8s.io/leaderworkerset"</li>
</ul>
</td>
</tr>
<tr><td><code>externalFrameworks</code> <B>[必需]</B><br/>
<code>[]string</code>
</td>
<td>
   <p>由外部控制器为 Kueue 管理的 GroupVersionKinds 列表；
预期格式为 <code>Kind.version.group.com</code>。</p>
</td>
</tr>
<tr><td><code>podOptions</code> <B>[必需]</B><br/>
<a href="#config-kueue-x-k8s-io-v1beta1-PodIntegrationOptions"><code>PodIntegrationOptions</code></a>
</td>
<td>
   <p>PodOptions 定义了 kueue 控制器对 pod 对象的行为</p>
<p>已弃用：此字段将在 v1beta2 中移除，使用 ManagedJobsNamespaceSelector
(https://kueue.sigs.k8s.io/docs/tasks/run/plain_pods/)
代替。</p>
</td>
</tr>
<tr><td><code>labelKeysToCopy</code> <B>[必需]</B><br/>
<code>[]string</code>
</td>
<td>
   <p>labelKeysToCopy 是应从作业复制到工作负载对象的标签键列表。作业不需要拥有此列表中的所有标签。如果作业没有此列表中具有给定键的某些标签，则构造的工作负载对象将在没有此标签的情况下创建。在从可组合作业（pod group）创建工作负载的情况下，如果多个对象具有此列表中某个键的标签，则这些标签的值必须匹配，否则工作负载创建将失败。标签仅在工作负载创建期间复制，即使基础作业的标签更改也不会更新。</p>
</td>
</tr>
</tbody>
</table>

## `InternalCertManagement`     {#config-kueue-x-k8s-io-v1beta1-InternalCertManagement}

**出现于：**

- [Configuration](#config-kueue-x-k8s-io-v1beta1-Configuration)

<table class="table">
<thead><tr><th width="30%">字段</th><th>描述</th></tr></thead>
<tbody>
    
<tr><td><code>enable</code> <B>[必需]</B><br/>
<code>bool</code>
</td>
<td>
   <p>Enable 控制 webhook 的内部证书管理的使用，
指标和可见性端点。
启用时，Kueue 使用库生成和
自签名证书。
禁用时，您需要为 webhooks、metrics 和 visibility 提供证书
通过第三方证书
此 secret 被挂载到 kueue 控制器管理器 pod。webhooks 的挂载路径是 /tmp/k8s-webhook-server/serving-certs，
metrics 端点的预期路径是 <code>/etc/kueue/metrics/certs</code>，visibility 端点的预期路径是 <code>/visibility</code>。
密钥和证书分别命名为 tls.key 和 tls.crt。</p>
</td>
</tr>
<tr><td><code>webhookServiceName</code> <B>[必需]</B><br/>
<code>string</code>
</td>
<td>
   <p>WebhookServiceName 是用作 DNSName 一部分的 Service 的名称。
默认为 kueue-webhook-service。</p>
</td>
</tr>
<tr><td><code>webhookSecretName</code> <B>[必需]</B><br/>
<code>string</code>
</td>
<td>
   <p>WebhookSecretName 是用于存储 CA 和服务器证书的 Secret 的名称。
默认为 kueue-webhook-server-cert。</p>
</td>
</tr>
</tbody>
</table>

## `LocalQueueMetrics`     {#config-kueue-x-k8s-io-v1beta1-LocalQueueMetrics}

**出现于：**

- [ControllerMetrics](#config-kueue-x-k8s-io-v1beta1-ControllerMetrics)

<p>LocalQueueMetrics 定义了本地队列指标的配置选项。
如果留空，则指标将暴露所有命名空间中的所有本地队列。</p>

<table class="table">
<thead><tr><th width="30%">字段</th><th>描述</th></tr></thead>
<tbody>
    
<tr><td><code>enable</code><br/>
<code>bool</code>
</td>
<td>
   <p>Enable 是一个旋钮，允许为本地队列暴露指标。默认为 true。</p>
</td>
</tr>
<tr><td><code>localQueueSelector</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#labelselector-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.LabelSelector</code></a>
</td>
<td>
   <p>LocalQueueSelector 可用于选择需要收集指标的本地队列。</p>
</td>
</tr>
</tbody>
</table>

## `MultiKueue`     {#config-kueue-x-k8s-io-v1beta1-MultiKueue}
    
**出现于：**

- [Configuration](#config-kueue-x-k8s-io-v1beta1-Configuration)

<table class="table">
<thead><tr><th width="30%">字段</th><th>描述</th></tr></thead>
<tbody>
    
<tr><td><code>gcInterval</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#duration-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.Duration</code></a>
</td>
<td>
   <p>GCInterval 定义两次连续垃圾回收运行之间的时间间隔。
默认为 1 分钟。如果为 0，则禁用垃圾回收。</p>
</td>
</tr>
<tr><td><code>origin</code><br/>
<code>string</code>
</td>
<td>
   <p>Origin 定义一个标签值，用于跟踪工作集群中工作负载的创建者。
这被 multikueue 在其垃圾收集器等组件中使用，以识别
由该 multikueue 管理器集群创建的远程对象，并在其本地对应物不再存在时删除它们。</p>
</td>
</tr>
<tr><td><code>workerLostTimeout</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#duration-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.Duration</code></a>
</td>
<td>
   <p>WorkerLostTimeout 定义如果与保留工作负载的工作集群的连接丢失，本地工作负载的 multikueue 准入检查状态保持 Ready 的时间。</p>
<p>默认为 15 分钟。</p>
</td>
</tr>
<tr><td><code>dispatcherName</code><br/>
<code>string</code>
</td>
<td>
   <p>DispatcherName 定义负责选择工作集群来处理工作负载的调度器。</p>
<ul>
<li>如果指定，工作负载将由命名的调度器处理。</li>
<li>如果未指定，工作负载将由默认（"kueue.x-k8s.io/multikueue-dispatcher-all-at-once"）调度器处理。</li>
</ul>
</td>
</tr>
<tr><td><code>externalFrameworks</code><br/>
<a href="#config-kueue-x-k8s-io-v1beta1-MultiKueueExternalFramework"><code>[]MultiKueueExternalFramework</code></a>
</td>
<td>
   <p>ExternalFrameworks 定义了应由通用 MultiKueue 适配器支持的外部框架列表。每个条目定义如何处理 MultiKueue 操作的特定 GroupVersionKind (GVK)。</p>
</td>
</tr>
</tbody>
</table>

## `MultiKueueExternalFramework`     {#config-kueue-x-k8s-io-v1beta1-MultiKueueExternalFramework} 

**出现于：**

- [MultiKueue](#config-kueue-x-k8s-io-v1beta1-MultiKueue)

<p>MultiKueueExternalFramework 定义了一个非内置的框架。</p>

<table class="table">
<thead><tr><th width="30%">字段</th><th>描述</th></tr></thead>
<tbody>
  
<tr><td><code>name</code> <B>[必需]</B><br/>
<code>string</code>
</td>
<td>
   <p>Name 是由外部控制器管理的资源的 GVK
预期格式为 <code>kind.version.group</code>。</p>
</td>
</tr>
</tbody>
</table>

## `ObjectRetentionPolicies`     {#config-kueue-x-k8s-io-v1beta1-ObjectRetentionPolicies}
    
**出现于：**

- [Configuration](#config-kueue-x-k8s-io-v1beta1-Configuration)

<p>ObjectRetentionPolicies 保存不同对象类型的保留设置。</p>

<table class="table">
<thead><tr><th width="30%">字段</th><th>描述</th></tr></thead>
<tbody>
    
<tr><td><code>workloads</code><br/>
<a href="#config-kueue-x-k8s-io-v1beta1-WorkloadRetentionPolicy"><code>WorkloadRetentionPolicy</code></a>
</td>
<td>
   <p>Workloads 配置 Workloads 的保留。
nil 值禁用 Workloads 的自动删除。</p>
</td>
</tr>
</tbody>
</table>

## `PodIntegrationOptions`     {#config-kueue-x-k8s-io-v1beta1-PodIntegrationOptions}
    
**出现于：**

- [Integrations](#config-kueue-x-k8s-io-v1beta1-Integrations)

<table class="table">
<thead><tr><th width="30%">字段</th><th>描述</th></tr></thead>
<tbody>
    
<tr><td><code>namespaceSelector</code> <B>[必需]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#labelselector-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.LabelSelector</code></a>
</td>
<td>
   <p>NamespaceSelector 可用于从 pod 协调中省略某些命名空间</p>
</td>
</tr>
<tr><td><code>podSelector</code> <B>[必需]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#labelselector-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.LabelSelector</code></a>
</td>
<td>
   <p>PodSelector 可用于选择要协调的 pod</p>
</td>
</tr>
</tbody>
</table>

## `PreemptionStrategy`     {#config-kueue-x-k8s-io-v1beta1-PreemptionStrategy}
    
（`string` 的别名）

**出现于：**

- [FairSharing](#config-kueue-x-k8s-io-v1beta1-FairSharing)

## `QueueVisibility`     {#config-kueue-x-k8s-io-v1beta1-QueueVisibility}
    
**出现于：**

- [Configuration](#config-kueue-x-k8s-io-v1beta1-Configuration)

<table class="table">
<thead><tr><th width="30%">字段</th><th>描述</th></tr></thead>
<tbody>
    
<tr><td><code>clusterQueues</code> <B>[必需]</B><br/>
<a href="#config-kueue-x-k8s-io-v1beta1-ClusterQueueVisibility"><code>ClusterQueueVisibility</code></a>
</td>
<td>
   <p>ClusterQueues 是配置，用于暴露集群队列中顶部待处理工作负载的信息。</p>
</td>
</tr>
<tr><td><code>updateIntervalSeconds</code> <B>[必需]</B><br/>
<code>int32</code>
</td>
<td>
   <p>UpdateIntervalSeconds 指定队列中顶部待处理工作负载结构更新的时间间隔。
最小值为 1。
默认为 5。</p>
</td>
</tr>
</tbody>
</table>

## `RequeuingStrategy`     {#config-kueue-x-k8s-io-v1beta1-RequeuingStrategy}
    
**出现于：**

- [WaitForPodsReady](#config-kueue-x-k8s-io-v1beta1-WaitForPodsReady)

<table class="table">
<thead><tr><th width="30%">字段</th><th>描述</th></tr></thead>
<tbody>
    
<tr><td><code>timestamp</code><br/>
<a href="#config-kueue-x-k8s-io-v1beta1-RequeuingTimestamp"><code>RequeuingTimestamp</code></a>
</td>
<td>
   <p>Timestamp 定义用于重新排队因 Pod 就绪性而被驱逐的 Workload 的时间戳。可能的值为：</p>
<ul>
<li><code>Eviction</code>（默认）表示来自具有 <code>PodsReadyTimeout</code> 原因的 Workload <code>Evicted</code> 条件。</li>
<li><code>Creation</code> 表示来自 Workload .metadata.creationTimestamp。</li>
</ul>
</td>
</tr>
<tr><td><code>backoffLimitCount</code><br/>
<code>int32</code>
</td>
<td>
   <p>BackoffLimitCount 定义重新排队重试的最大次数。
一旦达到该次数，工作负载将被停用（<code>.spec.activate</code>=<code>false</code>）。
当它为 null 时，工作负载将重复且无限制地重新排队。</p>
<p>每次退避持续时间约为 "b*2^(n-1)+Rand"，其中：</p>
<ul>
<li>"b" 表示由 "BackoffBaseSeconds" 参数设置的基数，</li>
<li>"n" 表示 "workloadStatus.requeueState.count"，</li>
<li>"Rand" 表示随机抖动。
在此期间，工作负载被视为不可准入，
其他工作负载将有机会被准入。
默认情况下，连续的重新排队延迟约为：(60s, 120s, 240s, ...)。</li>
</ul>
<p>默认为 null。</p>
</td>
</tr>
<tr><td><code>backoffBaseSeconds</code><br/>
<code>int32</code>
</td>
<td>
   <p>BackoffBaseSeconds 定义被驱逐工作负载重新排队的指数退避基数。</p>
<p>默认为 60。</p>
</td>
</tr>
<tr><td><code>backoffMaxSeconds</code><br/>
<code>int32</code>
</td>
<td>
   <p>BackoffMaxSeconds 定义重新排队被驱逐工作负载的最大退避时间。</p>
<p>默认为 3600。</p>
</td>
</tr>
</tbody>
</table>

## `RequeuingTimestamp`     {#config-kueue-x-k8s-io-v1beta1-RequeuingTimestamp}
    
（`string` 的别名）

**出现于：**

- [RequeuingStrategy](#config-kueue-x-k8s-io-v1beta1-RequeuingStrategy)

## `ResourceTransformation`     {#config-kueue-x-k8s-io-v1beta1-ResourceTransformation}
    
**出现于：**

- [Resources](#config-kueue-x-k8s-io-v1beta1-Resources)

<table class="table">
<thead><tr><th width="30%">字段</th><th>描述</th></tr></thead>
<tbody>
    
<tr><td><code>input</code> <B>[必需]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#resourcename-v1-core"><code>k8s.io/api/core/v1.ResourceName</code></a>
</td>
<td>
   <p>Input 是输入资源的名称。</p>
</td>
</tr>
<tr><td><code>strategy</code> <B>[必需]</B><br/>
<a href="#config-kueue-x-k8s-io-v1beta1-ResourceTransformationStrategy"><code>ResourceTransformationStrategy</code></a>
</td>
<td>
   <p>Strategy 指定输入资源是应该被替换还是保留。
默认为 Retain</p>
</td>
</tr>
<tr><td><code>multiplyBy</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#resourcename-v1-core"><code>k8s.io/api/core/v1.ResourceName</code></a>
</td>
<td>
   <p>MultiplyBy 表示工作负载请求的资源名称，如果
指定。
资源的请求量用于乘以
由 "input" 字段指示的资源的请求量。</p>
</td>
</tr>
<tr><td><code>outputs</code> <B>[必需]</B><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#resourcelist-v1-core"><code>k8s.io/api/core/v1.ResourceList</code></a>
</td>
<td>
   <p>Outputs 指定每单位输入资源的输出资源和数量。
与 <code>Replace</code> 策略组合的空 Outputs 会导致 Kueue 忽略 Input 资源。</p>
</td>
</tr>
</tbody>
</table>

## `ResourceTransformationStrategy`     {#config-kueue-x-k8s-io-v1beta1-ResourceTransformationStrategy}
    
（`string` 的别名）

**出现于：**

- [ResourceTransformation](#config-kueue-x-k8s-io-v1beta1-ResourceTransformation)

## `Resources`     {#config-kueue-x-k8s-io-v1beta1-Resources}
    

**出现于：**

- [Configuration](#config-kueue-x-k8s-io-v1beta1-Configuration)

<table class="table">
<thead><tr><th width="30%">字段</th><th>描述</th></tr></thead>
<tbody>
    
<tr><td><code>excludeResourcePrefixes</code> <B>[必需]</B><br/>
<code>[]string</code>
</td>
<td>
   <p>ExcludedResourcePrefixes 定义哪些资源应被 Kueue 忽略</p>
</td>
</tr>
<tr><td><code>transformations</code> <B>[必需]</B><br/>
<a href="#config-kueue-x-k8s-io-v1beta1-ResourceTransformation"><code>[]ResourceTransformation</code></a>
</td>
<td>
   <p>Transformations 定义如何将 PodSpec 资源转换为 Workload 资源请求。
这旨在成为一个以 Input 为键的映射（由验证代码强制执行）</p>
</td>
</tr>
<tr><td><code>deviceClassMappings</code><br/>
<a href="#config-kueue-x-k8s-io-v1beta1-DeviceClassMapping"><code>[]DeviceClassMapping</code></a>
</td>
<td>
   <p>DeviceClassMappings 定义从设备类到逻辑资源的映射
用于动态资源分配支持。</p>
</td>
</tr>
</tbody>
</table>

## `TLSOptions`     {#config-kueue-x-k8s-io-v1beta1-TLSOptions}
    
**出现于：**

- [ControllerManager](#config-kueue-x-k8s-io-v1beta1-ControllerManager)

<p>TLSOptions 定义 Kueue 服务器的 TLS 安全设置</p>

<table class="table">
<thead><tr><th width="30%">字段</th><th>描述</th></tr></thead>
<tbody>
    
<tr><td><code>minVersion</code><br/>
<code>string</code>
</td>
<td>
   <p>minVersion 是支持的最低 TLS 版本。
值来自 tls 包常量 (https://golang.org/pkg/crypto/tls/#pkg-constants)。
此字段仅在 TLSOptions 设置为 true 时有效。
默认值是不设置此值并继承 golang 设置。</p>
</td>
</tr>
<tr><td><code>cipherSuites</code><br/>
<code>[]string</code>
</td>
<td>
   <p>cipherSuites 是服务器允许的密码套件列表。
请注意，TLS 1.3 密码套件不可配置。
值来自 tls 包常量 (https://golang.org/pkg/crypto/tls/#pkg-constants)。
默认值是不设置此值并继承 golang 设置。</p>
</td>
</tr>
</tbody>
</table>

## `WaitForPodsReady`     {#config-kueue-x-k8s-io-v1beta1-WaitForPodsReady}
    
**出现于：**

- [Configuration](#config-kueue-x-k8s-io-v1beta1-Configuration)


<p>WaitForPodsReady 定义 Wait For Pods Ready 特性的配置，
用于确保所有 Pod 在指定时间内准备就绪。</p>

<table class="table">
<thead><tr><th width="30%">字段</th><th>描述</th></tr></thead>
<tbody>
    
<tr><td><code>enable</code> <B>[必需]</B><br/>
<code>bool</code>
</td>
<td>
   <p>Enable 表示是否启用等待 pods 就绪特性。
默认为 false。</p>
</td>
</tr>
<tr><td><code>timeout</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#duration-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.Duration</code></a>
</td>
<td>
   <p>Timeout 定义已准入工作负载达到
PodsReady=true 条件的时间。当超过超时时，工作负载
被驱逐并重新排队到同一个集群队列中。
默认为 5 分钟。</p>
</td>
</tr>
<tr><td><code>blockAdmission</code> <B>[必需]</B><br/>
<code>bool</code>
</td>
<td>
   <p>BlockAdmission 为 true 时，集群队列将阻止所有后续作业的准入，直到作业达到 PodsReady=true 条件。
此设置仅在 <code>Enable</code> 设置为 true 时生效。</p>
</td>
</tr>
<tr><td><code>requeuingStrategy</code><br/>
<a href="#config-kueue-x-k8s-io-v1beta1-RequeuingStrategy"><code>RequeuingStrategy</code></a>
</td>
<td>
   <p>RequeuingStrategy 定义工作负载重新排队的策略。</p>
</td>
</tr>
<tr><td><code>recoveryTimeout</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#duration-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.Duration</code></a>
</td>
<td>
   <p>RecoveryTimeout 定义一个超时，从工作负载被准入并运行后最后一次转换到 PodsReady=false 条件开始测量。
这种转换可能发生在 Pod 失败且等待替换 Pod 被调度时。
超过超时后，相应的作业会再次被挂起
并在退避延迟后重新排队。只有当 waitForPodsReady.enable=true 时，才会强制执行超时。
默认为 timeout 的值。设置为 "0s" 禁用恢复超时检查。</p>
</td>
</tr>
</tbody>
</table>

## `WorkloadRetentionPolicy`     {#config-kueue-x-k8s-io-v1beta1-WorkloadRetentionPolicy}

**出现于：**

- [ObjectRetentionPolicies](#config-kueue-x-k8s-io-v1beta1-ObjectRetentionPolicies)

<p>WorkloadRetentionPolicy 定义了工作负载应何时被删除的策略。</p>

<table class="table">
<thead><tr><th width="30%">字段</th><th>描述</th></tr></thead>
<tbody>
    
<tr><td><code>afterFinished</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#duration-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.Duration</code></a>
</td>
<td>
   <p>AfterFinished 是工作负载完成后等待的持续时间
在删除它之前。
持续时间为 0 将立即删除。
nil 值禁用自动删除。
使用 metav1.Duration 表示（例如 "10m"、"1h30m"）。</p>
</td>
</tr>
<tr><td><code>afterDeactivatedByKueue</code><br/>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#duration-v1-meta"><code>k8s.io/apimachinery/pkg/apis/meta/v1.Duration</code></a>
</td>
<td>
   <p>AfterDeactivatedByKueue 是等待任何 Kueue 管理的工作负载（如 Job、JobSet 或其他自定义工作负载类型）被 Kueue 标记为停用后自动删除它的持续时间。
停用工作负载的删除可能会级联到 Kueue 未创建的对象，因为删除父工作负载所有者（如 JobSet）可能会触发依赖资源的垃圾回收。
持续时间为 0 将立即删除。
nil 值禁用自动删除。
使用 metav1.Duration 表示（例如 "10m"、"1h30m"）。</p>
</td>
</tr>
</tbody>
</table>
