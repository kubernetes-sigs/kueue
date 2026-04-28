## v0.17.1

Changes since `v0.17.0`:

## Urgent Upgrade Notes 

### (No, really, you MUST read this before you upgrade)

- AdmissionChecks: Add the alpha `RejectUpdatesToCQWithInvalidOnFlavors` feature gate (disabled by default) to reject updates to existing ClusterQueues with invalid `AdmissionCheckStrategy.OnFlavors` references. 
  when enabling this feature gate, fix any existing invalid `OnFlavors` references before updating the affected ClusterQueues. (#10512, @tenzen-y)
 
## Changes by Kind

### Bug or Regression

- AdmissionChecks: ClusterQueue validation now checks that the flavors specified in `AdmissionCheckStrategy.OnFlavors` are listed in quota. (#10369, @ShaanveerS)
- AdmissionChecks: fix the bug that on backoff admission checks which are spanning all ResourceFlavors, such as MultiKueue, may be missing in the Workload’s status. 
  
  For MultiKueue that manifested with a bug, when aside from the MultiKueue admission check there was another non-MultiKueue admission check. In the scenario when eviction on the management cluster happened the manager that had temporarily lost connection to a worker, the remote workload would keep running on the reconnected worker, despite the workload staying without reservation on the manager cluster. (#9359, @Singularity23x0)
- AdmissionFairSharing: Fixed a bug in entry penalties by reducing them when workload is admitted and also clearing them up if all the resources on the admission entry penalty have value zero. (#10455, @MaysaMacedo)
- ElasticJobs: Fix a bug where pods stay gated after scale-up by allowing finished workloads to ungate their own pods. (#10364, @sohankunkerkar)
- FailureRecoveryPolicy: Fixed an issue where pods could remain stuck terminating if their node became unreachable only after the force-termination timeout had already elapsed. (#10500, @kshalot)
- Fix a bug in HA mode that caused follower replicas to retain stale workload caches after deletion. (#10521, @Ladicle)
- Fix a bug where the batch/v1 Job mutating webhook could still run even when the batch/job integration was disabled. (#10328, @Ladicle)
- Fix handling of orphaned workloads which could result in the accumulation of stale workloads
  after PodsReady timeout eviction for Deployment-owned pods. (#10274, @sebest)
- LeaderWorkerSet integration: fix the bug that the PodTemplate metadata wasn't propagated to the Workload's PodSets. (#10399, @pajakd)
- MultiKueue: Fixes the bug where a job, after being dispatched to a worker, would not sync correctly after being evicted there. This would also cause its workload to be incorrectly labeled as admitted.
  
  Now the workload and the manager job instance will correctly reflect the evicted state and MultiKueue will perform a fallback, then dispatch remote workloads to all eligible workers again after being evicted from the Worker it was successfully admitted to before. An example of such a case is if the remote instance got preempted on the worker. (#10340, @Singularity23x0)
- MultiKueue: fix the bug that when custom admission checks are configured on the manager cluster, other than
  the MultiKueue admission check, then the Job may start running on the selected worker before the other admission
  checks are satisfied (Ready). We fix the issue by deferring the dispatching of workload until all non-MultiKueue AdmissionChecks become Ready. (#10398, @mszadkow)
- Observability: Fix a bug where kueue_cohort_subtree_admitted_workloads_total and kueue_cohort_subtree_admitted_active_workloads metrics could include results for an implicit root Cohort after deletion of a child Cohort or ClusterQueue. (#10395, @mbobrovskyi)
- Observability: Fix excessive memory overhead in hot code paths by reusing the named logger in NewLogConstructor and avoiding unnecessary logger cloning. (#10393, @MatteoFari)
- Observability: avoid logging update failures as "error" when they are caused by concurrent object modifications, especially when multiple errors are present.
  
  Example log message: "failed to update MultiKueueCluster status: Operation cannot be fulfilled on multikueueclusters.kueue.x-k8s.io \"testing-cluster\": the object has been modified; please apply your changes to the latest version and try again after failing to load client config: open /tmp/kubeconfig no such file or directory" (#10348, @mbobrovskyi)
- TAS: Fix empty slices for count=0 podSets causing infinite scheduling loop (#10502, @jzhaojieh)
- TAS: fix the bug that Pods which only contain the `kueue.x-k8s.io/podset-slice-required-topology` or `kueue.x-k8s.io/podset-slice-required-topology-constraints` as the TAS annotation are not ungated. (#10442, @tg123)
- TAS: reduce the churn on the TAS-enabled controller, called NonTasUsageReconciler, by skipping triggering
  of the Reconcile on Pod changes which are irrelevant from the controller point-of-view. (#10508, @MatteoFari)

## v0.17.0

Changes since `v0.16.0`:

## Urgent Upgrade Notes 

### (No, really, you MUST read this before you upgrade)

- TAS: Stop kueue.x-k8s.io/tas label evaluation.
  
  ACTION REQUIRED
  Before you upgrade the Kueue version to v0.17.0, we highly recommend that there are no TAS workloads created before Kueue v0.14.0. (#8770, @tenzen-y)
 
## Changes by Kind

### API Change

- KueueViz: The backend WebSocket URL is no longer configured via a container environment variable. It is now automatically derived from the backend ingress configuration and mounted as a ConfigMap. (#9397, @ziadmoubayed)
- TAS: Relax ResourceFlavor spec immutability to only topology-sensitive fields (nodeLabels, tolerations, topologyName), allowing nodeTaints and future spec fields to be updated in-place. (#9427, @mukund-wayve)

### Feature

- Add support to Kubeflow Trainer v2.2 (#10127, @kaisoz)
- DRA: Add support for extended resources in alpha. The feature is enabled by the DRAExtendedResources feature gate. (#8597, @sohankunkerkar)
- ElasticWorkloads & TAS: enable the integration with the ElasticJobsViaWorkloadSlicesWithTAS feature gate (alpha) (#8580, @sohankunkerkar)
- Graduated LendingLimit to GA. The feature gate is now locked to true and cannot be disabled. (#9258, @kannon92)
- Graduated LocalQueueDefaulting to GA. (#9299, @kannon92)
- Graduated ObjectRetentionPolicies to GA (#9300, @kannon92)
- Graduated SanitizePodSets feature gate to GA. (#9260, @kannon92)
- HC: Graduated HierarchicalCohorts to GA (#9618, @PannagaRao)
- Helm: Add kueueViz.backend.ingress.enabled and kueueViz.frontend.ingress.enabled Helm values to allow disabling KueueViz ingress resources. (#8986, @david-gang)
- Helm: Add support to pass in custom issuerRef to allow for configuration of Issuers. (#9984, @MatteoFari)
- Helm: Allow setting log level (#9944, @gabesaba)
- Implements AdmissionGatedBy, a mechanism to prevent admission immediately after creation (#9792, @VassilisVassiliadis)
- Introduced the `WorkloadNameShorten` feature gate to ensure generated Workload names do not exceed 63 characters. This prevents issues where Workload labels were invalid due to length. When enabled, the owner-based prefix is truncated to fit within the limit while maintaining uniqueness via a hash suffix. (#9973, @mbobrovskyi)
- KubeRay integration: native support for managing RayService via Kueue. (#9102, @hiboyang)
- KueueViz Helm: Add podSecurityContext and containerSecurityContext configuration options to KueueViz Helm chart for restricted pod security profile compliance (#9311, @ziadmoubayed)
- KueueViz backend and frontend resource requests/limits are now configurable via Helm values (kueueViz.backend.resources and kueueViz.frontend.resources). (#8978, @david-gang)
- KueueViz: Add token-based authentication as opt-in. (#9684, @samzong)
- KueueViz: Added resource utilization reporting  and small UI improvements. (#10073, @ziadmoubayed)
- Kueueviz: support hierarchical cohorts with tree view (#9725, @samzong)
- Kueueviz: use informers instead of polling for WebSocket updates (#8707, @thejoeejoee)
- MultiKueue: Add support for LeaderWorkerSet workloads (#8670, @IrvingMg)
- MultiKueue: Add the  `MultiKueueOrchestratedPreemption` alpha feature gate proposed in KEP-8303, which enables the orchestration of preemptions within MultiKueue worker clusters, guaranteeing that the preemptions are executed sequentially.
  Introduce a new `PreemptionGate` API to `Workload`, which governs whether a workload can execute preemptions. (#9721, @kshalot)
- MultiKueue: Deprecate MultiKueueAllowInsecureKubeconfig feature gate. (#9297, @kannon92)
- MultiKueue: Enabling RayService workloads to be dispatched across multiple Kueue-managed clusters. (#10025, @hiboyang)
- MultiKueue: Promote MultiKueueBatchJobWithManagedBy to stable (#8877, @kannon92)
- Observability:  Introduce the cohort nominal quota metric. (#9132, @mszadkow)
- Observability:  Introduce the cohort resource reservations metric. (#9833, @mszadkow)
- Observability: Add custom Prometheus metric labels from CQ/LQ/Cohort metadata via metrics.customLabels config. (#9774, @IrvingMg)
- Observability: Add scheduler logs for the scheduling cycle phase boundaries. (#9795, @sohankunkerkar)
- Observability: Increased the maximum finite bucket boundary for admission_wait_time_seconds histogram from ~2.84 hours to ~11.3 hours for better observability of long queue times. (#9493, @mukund-wayve)
- Observability: Introduce "kueue.x-k8s.io/cluster-queue-name" and "kueue.x-k8s.io/local-queue-name" labels for pods created or managed by Kueue. (#9348, @dkaluza)
- Observability: Introduce kueue_cohort_admitted_workloads_total metric (#9999, @mbobrovskyi)
- Observability: Introduce kueue_cohort_subtree_admitted_active_workloads metric (#10013, @mbobrovskyi)
- Observability: added LocalQueueMetrics configuration to control which metrics should be displayed. (#9371, @mykysha)
- Observability: clean local queue metrics if the labels are not matching with the configuration after update. (#10097, @mykysha)
- Observability: graduated LocalQueueMetrics to Beta. (#9700, @mykysha)
- Pod integration: introduce the FastQuotaReleaseInPodIntegration feature gate (alpha, disabled by default)
  which allows to release quota as soon as all Pods in a group are terminating. (#8992, @tskillian)
- Promote PropagateBatchJobLabelsToWorkload to stable (#8876, @kannon92)
- Scheduling: Add the alpha SchedulerLongRequeueInterval feature gate (disabled by default) to increase the 
  inadmissible workload requeue interval from 1s to 10s. This may help to mitigate, on large environments with 
  many pending workloads, issues with frequent re-queues that prevent the scheduler from reaching schedulable 
  workloads deeper in the queue and result in constant re-evaluation of the same top workloads. (#9812, @mbobrovskyi)
- Scheduling: Add the alpha SchedulerTimestampPreemptionBuffer feature gate (disabled by default) to use
  5-minute buffer so that workloads with scheduling timestamps within this buffer don’t preempt each other
  based on LowerOrNewerEqualPriority. (#9835, @mbobrovskyi)
- Scheduling: support the kueue.k8s-x.io/priority-boost annotation to increase the effective priority of a workload.
  The annotation is meant for the use of external controllers on the Workload object. (#9120, @vladikkuzn)
- Support kubeflow/spark-operator's SparkApplication integration without Elastic Job Support (i.e., no support for Dynamic Allocation in SparkApplication) behind the SparkApplicationIntegration feature gate (#7268, @everpeace)
- TAS: Extend the support for handling NoSchedule taints when the TASReplaceNodeOnNodeTaints feature gate is enabled. (#8997, @j-skiba)
- TAS: Introduce the TASReplaceNodeOnNodeTaints feature gate (enabled by default) which allows 
  TAS workloads to be evicted or replaced when a node is tainted with NoExecute. (#9450, @j-skiba)
- TAS: Introduce the `TASMultiLayerTopology` feature gate (alpha) to support up to 3 hierarchical slice layers (#9506, @Huang-Wei)
- VisibilityOnDemand: Introduce a new Kueue deployment argument, --visibility-server-port, which allows passing custom port when starting the visibility server. (#9863, @Nilsachy)
- VisibilityOnDemand: Introduce a new configuration type: "VisibilityServer" which allows passing custom "VisibilityServerConfiguration" limited to BindAddress and BindPort as a first iteration. Removing the deprecated "visibility-server-port" flag. (#9850, @Nilsachy)

### Documentation

- Add documentation for common Grafana queries to monitor Kueue metrics. (#8968, @IrvingMg)

### Bug or Regression

- ElasticJobs: fix the temporary double-counting of quota during workload replacement. 
  In particular it was causing double-counting of quota requests for unchanged PodSets. (#9322, @benkermani)
- FailureRecoveryPolicy: forcefully delete stuck pods (without grace period) in addition to transitioning them
  to the `Failed` phase. This fixes a scenario where foreground propagating deletions were blocked by a stuck pod. (#9651, @kshalot)
- FairSharing: Fix `FairSharingPrioritizeNonBorrowing` to check per-flavor borrowing at every hierarchy level in hierarchical cohorts, not just at the ClusterQueue level. (#10149, @mukund-wayve)
- FairSharing: workloads fitting within their ClusterQueue's nominal quota are now preferred over workloads that require borrowing, preventing heavy borrowing on one flavor from deprioritizing a CQ's nominal entitlement on another flavor. (#9484, @mukund-wayve)
- Fix a bug where finished or deactivated workloads blocked ClusterQueue deletion and finalizer removal. (#8919, @sohankunkerkar)
- Fix non-deterministic workload ordering in ClusterQueue by adding UID tie-breaker to queue ordering function. (#9101, @sohankunkerkar)
- Fix serverName substitution in kustomize prometheus ServiceMonitor TLS patch for cert-manager deployments. (#9180, @IrvingMg)
- Fixed invalid field name in the `ClusterQueue` printer columns. The "Cohort" column will now correctly display the assigned cohort in kubectl, k9s, and other UI tools instead of being blank. (#9394, @polinasand)
- Fixed the bug that prevented managing workloads with duplicated environment variable names in initContainers. This issue manifested when creating the Workload via the API. (#9118, @monabil08)
- FlavorFungability: fix the bug that the semantics for the `flavorFungability.preference` enum values
  (ie. PreemptionOverBorrowing and BorrowingOverPreemption) were swapped. (#9464, @tenzen-y)
- In fair sharing preemption, bypass DRS strategy gates when the preemptor ClusterQueue is within nominal quota for contested resources, allowing preemption even if the CQ's aggregate DRS is high due to borrowing on other flavors. (#9494, @mukund-wayve)
- Kueueviz: fetch Cohort CRD directly, instead of deriving from ClusterQueues (#9719, @samzong)
- LeaderWorkerSet: Fix the bug where rolling updates with maxSurge could get stuck. (#8801, @PannagaRao)
- LeaderWorkerSet: Fixed a bug that the `kueue.x-k8s.io/job-uid` label was not set on the workloads. (#9898, @mbobrovskyi)
- LeaderWorkerSet: Fixed bug that doesn't allow to delete Pod after LeaderWorkerSet delete (#8862, @mbobrovskyi)
- LeaderWorkerSet: fix an occasional race condition resulting in workload deletion getting stuck during scale down. (#9135, @PannagaRao)
- LeaderWorkerSet: fix workload recreation delay during rolling updates by watching for workload deletions. (#9631, @PannagaRao)
- Metrics certificate is now reloaded when certificate data is updated. (#9071, @MaysaMacedo)
- MultiKueue & ElasticJobs: fix the bug that the new size of a Job was not reflected on the worker cluster. (#9044, @ichekrygin)
- MultiKueue: Enable AllowWatchBookmarks for remote client watches to prevent idle watch connections from being terminated by HTTP proxies with idle timeouts (e.g., Cloudflare 524 errors). (#9968, @trilamsr)
- MultiKueue: Fix a bug that the remote Job object was occasionally left by MultiKueue GC, 
  even when the corresponding Job object on the management cluster was deleted.
  This issue was observed for LeaderWorkerSet. (#9201, @sohankunkerkar)
- MultiKueue: for the StatefulSet integration copy the entire StatefulSet onto the worker clusters. This allows
  for proper management (and replacements) of Pods on the worker clusters. (#9344, @IrvingMg)
- Observability: Fix Prometheus ServiceMonitor selector and RBAC to enable metrics scraping. (#8977, @IrvingMg)
- Observability: Fix missing "replica-role" in the logs from the NonTasUsageReconciler. (#9433, @IrvingMg)
- Observability: Fix missing replica_role=leader gauge metrics after HA role transition. (#9487, @IrvingMg)
- Observability: Fix the stale "replica-role" value in scheduler logs after leader election. (#9429, @IrvingMg)
- Observability: Fixed a bug where workloads that finished before a Kueue restart were not tracked in the gauge metrics for finished workloads. (#8818, @mbobrovskyi)
- Observability: do not use "error" for logging when an update request fails because the underlying object was modified
  concurrently. Now we use V3 logging level for such cases and don't print stracktrace. 
  
  This is an example log message: "Operation cannot be fulfilled on podtemplates \"job\": the object has been modified;
  please apply your changes to the latest version and try again" (#8708, @dkaluza)
- Observability: fix the bug that the "replica-role" (leader / follower) log decorator was missing in the log lines output by
  the  webhooks for LeaderWorkerSet and StatefulSet . (#8815, @mszadkow)
- PodIntegration: Fix the bug that Kueue would occasionally remove the custom finalizers when
  removing the `kueue.x-k8s.io/managed` finalizer. (#3912, @mykysha)
- RayJob integration: Make RayJob top level workload managed by Kueue when autoscaling via
  ElasticJobsViaWorkloadSlices is enabled.
  
  If you are an alpha user of the ElasticJobsViaWorkloadSlices feature for RayJobs, then upgrading Kueue may impact running live jobs which have autoscaling / workload slicing enabled. For example, if you upgrade Kueue, before
  scaling-up completes,  the new pods will be stuck in SchedulingGated state. (#8341, @hiboyang)
- RayJob integration: fix the autosaling scenarios when using ElasticJobsViaWorkloadSlices. In particular when
  two consecutive scale ups happen. (#9960, @hiboyang)
- RoleTracker: fix missing replica-role in logs for LWS, StatefulSet, and MultiKueue handlers. (#9445, @IrvingMg)
- Scheduling: Fix a BestEffortFIFO performance issue where many equivalent workloads could
  prevent the scheduler from reaching schedulable workloads deeper in the queue. Kueue now
  skips redundant evaluation by bulk-moving same-hash workloads to inadmissible when one
  representative is categorized as NoFit. (#9698, @sohankunkerkar)
- Scheduling: Fix a race where updated workload priority could remain stuck in the inadmissible queue and delay rescheduling. (#9661, @sohankunkerkar)
- Scheduling: Fix that the Kueue's scheduler could issue duplicate preemption requests and events for the same workload. (#9437, @sohankunkerkar)
- Scheduling: Fix the bug where inadmissible workloads would be re-queued too frequently at scale.
  This resulted in excessive processing, lock contention, and starvation of workloads deeper in the queue.
  The fix is to throttle the process with a batch period of 1s per CQ or Cohort. (#9232, @gabesaba)
- Scheduling: Fixed a race condition where a workload could simultaneously exist in the scheduler's heap
  and the "inadmissible workloads" list. This fix prevents unnecessary scheduler cycles and prevents temporary 
  double counting for the metric of pending workloads. (#9598, @sohankunkerkar)
- Scheduling: fix the bug that scheduler could get stuck trying to preempt a workload due to the corruption of the
  in-memory state tracking the pending preemptions (called preemptionExpectations). (#10185, @mimowo)
- Scheduling: fix the issue that scheduler could indefinitely try re-queueing a workload which was once 
  inadmissible, but is admissible after an update. The issue affected workloads which don't specify 
  resource requests explicitly, but rely on defaulting based on limits. (#9894, @mimowo)
- Scheduling: fixed SchedulingEquivalenceHashing so equivalent workloads that become inadmissible through
  the preemption path with no candidates are also covered by the mechanism. 
  
  As a safety measure while the broader fix is validated, the beta SchedulingEquivalenceHashing feature gate
  is temporarily disabled by default. (#10001, @mimowo)
- StatefulSet integration: Fixed a bug that the `kueue.x-k8s.io/job-uid` label was not set on the workloads. (#9897, @mbobrovskyi)
- StatefulSet integration: fix the bug that when using `generateName` the Workload names generated
  for two different StatefulSets would conflict, not allowing to run the second StatefulSet. (#9091, @IrvingMg)
- Strip managedFields from informer cache via DefaultTransform to reduce memory footprint on large clusters. (#10078, @jzhaojieh)
- TAS: Fix a bug that LeaderWorkerSet with multiple PodTemplates (`.spec.leaderWorkerTemplate.leaderTemplate` and `.spec.leaderWorkerTemplate.workerTemplate`), Pod indexes are not correctly evaluated during rank-based ordering assignments. (#8685, @tenzen-y)
- TAS: Fix a bug that TAS ignored resources excluded by excludeResourcePrefixes for node placement. (#8865, @sohankunkerkar)
- TAS: Fix a bug where preemption with multiple resources sometimes fails (#10193, @pajakd)
- TAS: Fix nil pointer panic in TAS node reconciler when unadmitted workloads exist in the cluster. (#10036, @kannon92)
- TAS: Fix performance bug where snapshotting would take very long due to List and DeepCopy
  of all Nodes. Now the cached set of nodes is maintained in event-driven fashion. (#9712, @mbobrovskyi)
- TAS: Fixed a bug that pending workloads could be stuck, not being considered by the Kueue's scheduler,
  after the restart of Kueue. The workloads would be considered for scheduling again after any update to their 
  ClusterQueue. (#9029, @sohankunkerkar)
- TAS: Fixed a bug where pods could become stuck in a `Pending` state during node replacement.
  This may occur when a node gets tainted or `NotReady` after the topology assignment phase, but before
  the pods are ungated. (#9615, @j-skiba)
- TAS: Improved the performance of the node_controller Reconcile loop by introducing a new field indexer for Workloads. (#10042, @j-skiba)
- TAS: Workloads that require TAS but have a PodSet with a failed TAS request (e.g., more than one flavor assigned) are correctly rejected at admission with a clear Pending reason and message, rather than being admitted without TopologyAssignment. (#8945, @zhifei92)
- TAS: fix a bug where NodeHotSwap may assign a Pod, based on rank-ordering, to a node which is already
  occupied by another running Pod. (#9211, @j-skiba)
- TAS: fix the bug that workloads which only specify resource limits, without requests, are not able to perform 
  the second-pass scheduling correctly, after Kueue restart, responsible for NodeHotSwap and ProvisioningRequests. (#10158, @mimowo)
- TAS: fix the bug that workloads which only specify resource limits, without requests, are not able to perform 
  the second-pass scheduling correctly, responsible for NodeHotSwap and ProvisioningRequests. (#9939, @mimowo)
- TAS: support ResourceTransformations to define "virtual" resources which allow putting a cap on
  some "virtual" credits across multiple-flavors, see [sharing quotas](https://kueue.sigs.k8s.io/docs/tasks/manage/share_quotas_across_flavors/) for quota-only resources.
  This is considered a bug since there was no validation preventing such configuration before. (#8963, @mbobrovskyi)
- VisibilityOnDemand: Fix Visibility API OpenAPI schema generation to prevent schema resolution errors when visibility v1beta1/v1beta2 APIServices are installed.
  
  The visibility schema issues result in the following error when re-applying the manifest for Kueue 0.16.0:
  `failed to load open api schema while syncing cluster cache: error getting openapi resources: SchemaError(sigs.k8s.io/kueue/apis/visibility/v1beta1.PendingWorkloadsSummary.items): unknown model in reference: "sigs.k8s.io~1kueue~1apis~1visibility~1v1beta1.PendingWorkload"` (#8885, @vladikkuzn)
- VisibilityOnDemand: Fix non-deterministic workload ordering with UsageBasedAdmissionFairSharing enabled. (#9899, @sohankunkerkar)
- VisibilityOnDemand: Fix the bug that when running Kueue with the custom `--kubeconfig` flag the visibility server
  fails to initialize, because the custom value of the flag is not propagated to it, leading to errors such as:
  "Unable to create and start visibility server","error":"unable to apply VisibilityServerOptions: failed to get delegated authentication kubeconfig:  failed to get delegated authentication kubeconfig: ..." (#9619, @Nilsachy)

### Other (Cleanup or Flake)

- KueueViz: It switches to the v1beta2 API (#8784, @mbobrovskyi)
- Observability: Increased log level from 3 to 5 for TAS node filtering-related log events. (#10017, @mwysokin)
- Observability: Update the name of cohort metric from `kueue_cohort_nominal_quota` to `kueue_cohort_subtree_quota`. (#9787, @mszadkow)
- Scheduling: Reduced the maximum sleep time between scheduling cycles from 100ms to 10ms.
  This change fixes a bug where the 100ms delay was excessive on busy systems, in which completed
  workloads can trigger requeue events every second. In such cases, the scheduler could spend up to 10%
  of the time between requeue events sleeping. Reducing the delay allows the scheduler to spend more time
  progressing through the ClusterQueue heap between requeue events. (#9697, @mimowo)
- TAS: Remove the deprecated TASProfileLeastFreeCapacity feature gate. (#9298, @kannon92)
- V1beta2: Optimized migration script to allow migration of namespaced resources by namespace. (#9340, @mbobrovskyi)

