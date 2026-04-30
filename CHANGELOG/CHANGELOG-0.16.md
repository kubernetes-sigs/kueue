## v0.16.7

Changes since `v0.16.6`:

## Changes by Kind

### Bug or Regression

- FailureRecovery: Forcefully delete pods that are Failed/Succeeded and scheduled on unreachable nodes.
  This unblocks cases like a JobSet deleting a Job with foreground cascade being stuck because a pod in a terminal phase exists on one of the unhealthy nodes. (#10855, @kshalot)
- Fix a race-condition bug that a deleted ClusterQueue may be kept by a finalizer, even after deletion of all workloads and LQs. (#10834, @ShaanveerS)
- Fixed a bug in Kueue's cache that could leave stale SubtreeQuota values in ancestor cohorts after a child Cohort
  was deleted, leading to potential over-admission of workloads and incorrect metrics reporting. (#10842, @mbobrovskyi)
- Fixed a bug where admitted Workloads could fail to patch through the v1beta1 API due to CEL validation of the `priorityClassSource` immutability rule. (#10630, @kannon92)
- Observability: downgrade the non-compatible flavor error logs to Info level (v3). (#10638, @maishivamhoo123)
- TAS: Fix a bug where admitted workloads with unhealthy nodes were not evicted when an AdmissionCheck entered Retry or when the PodsReady recovery timeout was exceeded. (#10693, @pajakd)
- TAS: Fix handling of PodSet groups which could lead in some scenarios to empty topologyAssignment. (#10857, @mimowo)
- TAS: Fix nil pointer panic in TAS node reconciler when unadmitted workloads exist in the cluster. (#10674, @j-skiba)
- TAS: Refine the NodeHotSwap logic to ensure that UnhealthyNodes are only updated for workloads currently assigned to a Node via a topology topology assignment. This prevents "late pods" from stale topologies from triggering inaccurate health reporting. (#10838, @j-skiba)
- VisibilityOnDemand: Fixed a bug in the visibility endpoint, that listing workloads from a local queue includes
  workloads from other LocalQueues in different namespaces, if the other LocalQueues have the same name. (#10678, @mbobrovskyi)

## v0.16.6

Changes since `v0.16.5`:

## Urgent Upgrade Notes 

### (No, really, you MUST read this before you upgrade)

- AdmissionChecks: Add the alpha `RejectUpdatesToCQWithInvalidOnFlavors` feature gate (disabled by default) to reject updates to existing ClusterQueues with invalid `AdmissionCheckStrategy.OnFlavors` references. 
  when enabling this feature gate, fix any existing invalid `OnFlavors` references before updating the affected ClusterQueues. (#10511, @tenzen-y)
 
## Changes by Kind

### Bug or Regression

- AdmissionChecks: ClusterQueue validation now checks that the flavors specified in `AdmissionCheckStrategy.OnFlavors` are listed in quota. (#10378, @ShaanveerS)
- AdmissionChecks: fix the bug that on backoff admission checks which are spanning all ResourceFlavors, such as MultiKueue, may be missing in the Workload’s status. 
  
  For MultiKueue that manifested with a bug, when aside from the MultiKueue admission check there was another non-MultiKueue admission check. In the scenario when eviction on the management cluster happened the manager that had temporarily lost connection to a worker, the remote workload would keep running on the reconnected worker, despite the workload staying without reservation on the manager cluster. (#9359, @Singularity23x0)
- AdmissionFairSharing: Fixed a bug in entry penalties by reducing them when workload is admitted and also clearing them up if all the resources on the admission entry penalty have value zero. (#10465, @MaysaMacedo)
- ElasticJobs: Fix a bug where pods stay gated after scale-up by allowing finished workloads to ungate their own pods. (#10392, @sohankunkerkar)
- FailureRecoveryPolicy: Fixed an issue where pods could remain stuck terminating if their node became unreachable only after the force-termination timeout had already elapsed. (#10501, @kshalot)
- Fix handling of orphaned workloads which could result in the accumulation of stale workloads
  after PodsReady timeout eviction for Deployment-owned pods. (#10274, @sebest)
- LeaderWorkerSet integration: fix the bug that the PodTemplate metadata wasn't propagated to the Workload's PodSets. (#10444, @pajakd)
- MultiKueue: Fixes the bug where a job, after being dispatched to a worker, would not sync correctly after being evicted there. This would also cause its workload to be incorrectly labeled as admitted.
  
  Now the workload and the manager job instance will correctly reflect the evicted state and MultiKueue will perform a fallback, then dispatch remote workloads to all eligible workers again after being evicted from the Worker it was successfully admitted to before. An example of such a case is if the remote instance got preempted on the worker. (#9670, @Singularity23x0)
- MultiKueue: fix the bug that when custom admission checks are configured on the manager cluster, other than
  the MultiKueue admission check, then the Job may start running on the selected worker before the other admission
  checks are satisfied (Ready). We fix the issue by deferring the dispatching of workload until all non-MultiKueue AdmissionChecks become Ready. (#10405, @mszadkow)
- Observability: Fix excessive memory overhead in hot code paths by reusing the named logger in NewLogConstructor and avoiding unnecessary logger cloning. (#10394, @MatteoFari)
- TAS: Fix empty slices for count=0 podSets causing infinite scheduling loop (#10510, @mimowo)
- TAS: fix a bug that Pods which only contain the `kueue.x-k8s.io/podset-slice-required-topology` as the TAS annotation are not ungated. (#10445, @tg123)
- TAS: reduce the churn on the TAS-enabled controller, called NonTasUsageReconciler, by skipping triggering
  of the Reconcile on Pod changes which are irrelevant from the controller point-of-view. (#10507, @MatteoFari)

## v0.16.5

Changes since `v0.16.4`:

## Changes by Kind

### Feature

- Helm: Add kueueViz.backend.ingress.enabled and kueueViz.frontend.ingress.enabled Helm values to allow disabling KueueViz ingress resources. (#10065, @david-gang)
- Helm: Add support to pass in custom issuerRef to allow for configuration of Issuers. (#10212, @MatteoFari)
- Introduced the `WorkloadNameShorten` feature gate to ensure generated Workload names do not exceed 63 characters. This prevents issues where Workload labels were invalid due to length. When enabled, the owner-based prefix is truncated to fit within the limit while maintaining uniqueness via a hash suffix. (#10129, @mbobrovskyi)

### Bug or Regression

- FairSharing: Fix `FairSharingPrioritizeNonBorrowing` to check per-flavor borrowing at every hierarchy level in hierarchical cohorts, not just at the ClusterQueue level. (#10203, @mukund-wayve)
- RayJob integration: fix the autosaling scenarios when using ElasticJobsViaWorkloadSlices. In particular when
  two consecutive scale ups happen. (#10160, @hiboyang)
- Scheduling: fix the bug that scheduler could get stuck trying to preempt a workload due to the corruption of the
  in-memory state tracking the pending preemptions (called preemptionExpectations). (#10206, @mimowo)
- Strip managedFields from informer cache via DefaultTransform to reduce memory footprint on large clusters. (#10125, @jzhaojieh)
- TAS: Fix a bug where preemption with multiple resources sometimes fails (#10204, @mimowo)
- TAS: Fix nil pointer panic in TAS node reconciler when unadmitted workloads exist in the cluster. (#10039, @kannon92)
- TAS: Improved the performance of the node_controller Reconcile loop by introducing a new field indexer for Workloads. (#10051, @j-skiba)
- TAS: Workloads that require TAS but have a PodSet with a failed TAS request (e.g., more than one flavor assigned) are correctly rejected at admission with a clear Pending reason and message, rather than being admitted without TopologyAssignment. (#10228, @j-skiba)
- TAS: fix the bug that workloads which only specify resource limits, without requests, are not able to perform 
  the second-pass scheduling correctly, after Kueue restart, responsible for NodeHotSwap and ProvisioningRequests. (#10177, @mimowo)

### Other (Cleanup or Flake)

- Observability: Increased log level from 3 to 5 for TAS node filtering-related log events. (#10021, @mwysokin)

## v0.16.4

Changes since `v0.16.3`:

## Changes by Kind

### Feature

- Helm: Allow setting log level (#9944, @gabesaba)
- TAS: Extend the support for handling NoSchedule taints when the TASReplaceNodeOnNodeTaints feature gate is enabled. (#10003, @j-skiba)
- VisibilityOnDemand: Introduce a new Kueue deployment argument, --visibility-server-port, which allows passing custom port when starting the visibility server. (#9976, @Nilsachy)

### Bug or Regression

- LWS integration: Fixed a bug that the `kueue.x-k8s.io/job-uid` label was not set on the workloads. (#10010, @mbobrovskyi)
- MultiKueue: Enable AllowWatchBookmarks for remote client watches to prevent idle watch connections from being terminated by HTTP proxies with idle timeouts (e.g., Cloudflare 524 errors). (#9990, @trilamsr)
- Scheduling: fix the issue that scheduler could indefinitely try re-queueing a workload which was once 
  inadmissible, but is admissible after an update. The issue affected workloads which don't specify 
  resource requests explicitly, but rely on defaulting based on limits. (#9913, @mimowo)
- Scheduling: fixed SchedulingEquivalenceHashing so equivalent workloads that become inadmissible through
  the preemption path with no candidates are also covered by the mechanism. 
  
  As a safety measure while the broader fix is validated, the beta SchedulingEquivalenceHashing feature gate
  is temporarily disabled by default. (#10007, @mimowo)
- StatefulSet integration: Fixed a bug that the `kueue.x-k8s.io/job-uid` label was not set on the workloads. (#9902, @mbobrovskyi)
- TAS: Fixed a bug where pods could become stuck in a `Pending` state during node replacement.
  This may occur when a node gets tainted or `NotReady` after the topology assignment phase, but before
  the pods are ungated. (#9978, @j-skiba)
- TAS: fix the bug that workloads which only specify resource limits, without requests, are not able to perform 
  the second-pass scheduling correctly, responsible for NodeHotSwap and ProvisioningRequests. (#9947, @mimowo)
- VisibilityOnDemand: Fix non-deterministic workload ordering with UsageBasedAdmissionFairSharing enabled. (#9955, @sohankunkerkar)

### Other (Cleanup or Flake)

- Restore the FlavorFungibilityImplicitPreferenceDefault feature gate. (#9991, @mimowo)

## v0.16.3

Changes since `v0.16.2`:

## Changes by Kind

### Feature

- Observability: Add scheduler logs for the scheduling cycle phase boundaries. (#9813, @sohankunkerkar)
- Scheduling: Add the alpha SchedulerLongRequeueInterval feature gate (disabled by default) to increase the 
  inadmissible workload requeue interval from 1s to 10s. This may help to mitigate, on large environments with 
  many pending workloads, issues with frequent re-queues that prevent the scheduler from reaching schedulable 
  workloads deeper in the queue and result in constant re-evaluation of the same top workloads. (#9819, @mbobrovskyi)
- Scheduling: Add the alpha SchedulerTimestampPreemptionBuffer feature gate (disabled by default) to use
  5-minute buffer so that workloads with scheduling timestamps within this buffer don’t preempt each other
  based on LowerOrNewerEqualPriority. (#9837, @mbobrovskyi)

### Bug or Regression

- FailureRecoveryPolicy: forcefully delete stuck pods (without grace period) in addition to transitioning them
  to the `Failed` phase. This fixes a scenario where foreground propagating deletions were blocked by a stuck pod. (#9673, @kshalot)
- Fix a race where updated workload priority could remain stuck in the inadmissible queue and delay rescheduling. (#9678, @sohankunkerkar)
- In fair sharing preemption, bypass DRS strategy gates when the preemptor ClusterQueue is within nominal quota for contested resources, allowing preemption even if the CQ's aggregate DRS is high due to borrowing on other flavors. (#9592, @mukund-wayve)
- Kueueviz: fetch Cohort CRD directly, instead of deriving from ClusterQueues (#9720, @samzong)
- LeaderWorkerSet: fix workload recreation delay during rolling updates by watching for workload deletions. (#9680, @PannagaRao)
- Observability: Fix missing replica_role=leader gauge metrics after HA role transition. (#9794, @IrvingMg)
- Scheduling: Fix a BestEffortFIFO performance issue where many equivalent workloads could
  prevent the scheduler from reaching schedulable workloads deeper in the queue. Kueue now
  skips redundant evaluation by bulk-moving same-hash workloads to inadmissible when one
  representative is categorized as NoFit. (#9698, @sohankunkerkar)
- Scheduling: Fix that the Kueue's scheduler could issue duplicate preemption requests and events for the same workload. (#9627, @sohankunkerkar)
- Scheduling: Fixed a race condition where a workload could simultaneously exist in the scheduler's heap
  and the "inadmissible workloads" list. This fix prevents unnecessary scheduler cycles and prevents temporary 
  double counting for the metric of pending workloads. (#9638, @sohankunkerkar)
- Scheduling: Reduced the maximum sleep time between scheduling cycles from 100ms to 10ms.
  This change fixes a bug where the 100ms delay was excessive on busy systems, in which completed
  workloads can trigger requeue events every second. In such cases, the scheduler could spend up to 10%
  of the time between requeue events sleeping. Reducing the delay allows the scheduler to spend more time
  progressing through the ClusterQueue heap between requeue events. (#9763, @mimowo)
- StatefulSet integration: fix the bug that when using `generateName` the Workload names generated
  for two different StatefulSets would conflict, not allowing to run the second StatefulSet. (#9693, @IrvingMg)
- TAS: Fix performance bug where snapshotting would take very long due to List and DeepCopy
  of all Nodes. Now the cached set of nodes is maintained in event-driven fashion. (#9783, @mbobrovskyi)
- TAS: support ResourceTransformations to define "virtual" resources which allow putting a cap on
  some "virtual" credits across multiple-flavors, see [sharing quotas](https://kueue.sigs.k8s.io/docs/tasks/manage/share_quotas_across_flavors/) for quota-only resources.
  This is considered a bug since there was no validation preventing such configuration before. (#9688, @mbobrovskyi)
- VisibilityOnDemand: Fix the bug that when running Kueue with the custom `--kubeconfig` flag the visibility server
  fails to initialize, because the custom value of the flag is not propagated to it, leading to errors such as:
  "Unable to create and start visibility server","error":"unable to apply VisibilityServerOptions: failed to get delegated authentication kubeconfig:  failed to get delegated authentication kubeconfig: ..." (#9805, @Nilsachy)

## v0.16.2

Changes since `v0.16.1`:

## Changes by Kind

### Feature

- KueueViz Helm: Add podSecurityContext and containerSecurityContext configuration options to KueueViz Helm chart for restricted pod security profile compliance (#9319, @ziadmoubayed)
- Observability: Increased the maximum finite bucket boundary for admission_wait_time_seconds histogram from ~2.84 hours to ~11.3 hours for better observability of long queue times. (#9507, @mukund-wayve)

### Bug or Regression

- ElasticJobs: fix the temporary double-counting of quota during workload replacement. 
  In particular it was causing double-counting of quota requests for unchanged PodSets. (#9364, @benkermani)
- FairSharing: workloads fitting within their ClusterQueue's nominal quota are now preferred over workloads that require borrowing, preventing heavy borrowing on one flavor from deprioritizing a CQ's nominal entitlement on another flavor. (#9532, @mukund-wayve)
- Fix non-deterministic workload ordering in ClusterQueue by adding UID tie-breaker to queue ordering function. (#9140, @sohankunkerkar)
- Fix serverName substitution in kustomize prometheus ServiceMonitor TLS patch for cert-manager deployments. (#9188, @IrvingMg)
- Fixed invalid field name in the `ClusterQueue` printer columns. The "Cohort" column will now correctly display the assigned cohort in kubectl, k9s, and other UI tools instead of being blank. (#9422, @polinasand)
- Fixed the bug that prevented managing workloads with duplicated environment variable names in initContainers. This issue manifested when creating the Workload via the API. (#9126, @monabil08)
- FlavorFungability: fix the bug that the semantics for the `flavorFungability.preference` enum values
  (ie. PreemptionOverBorrowing and BorrowingOverPreemption) were swapped. (#9486, @tenzen-y)
- LeaderWorkerSet: fix an occasional race condition resulting in workload deletion getting stuck during scale down. (#9135, @PannagaRao)
- MultiKueue: Fix a bug that the remote Job object was occasionally left by MultiKueue GC, 
  even when the corresponding Job object on the management cluster was deleted.
  This issue was observed for LeaderWorkerSet. (#9310, @sohankunkerkar)
- MultiKueue: for the StatefulSet integration copy the entire StatefulSet onto the worker clusters. This allows
  for proper management (and replacements) of Pods on the worker clusters. (#9539, @IrvingMg)
- Observability: Fix missing "replica-role" in the logs from the NonTasUsageReconciler. (#9456, @IrvingMg)
- Observability: Fix the stale "replica-role" value in scheduler logs after leader election. (#9431, @IrvingMg)
- Scheduling: Fix the bug where inadmissible workloads would be re-queued too frequently at scale.
  This resulted in excessive processing, lock contention, and starvation of workloads deeper in the queue.
  The fix is to throttle the process with a batch period of 1s per CQ or Cohort. (#9490, @gabesaba)
- TAS: Fix a bug that LeaderWorkerSet with multiple PodTemplates (`.spec.leaderWorkerTemplate.leaderTemplate` and `.spec.leaderWorkerTemplate.workerTemplate`), Pod indexes are not correctly evaluated during rank-based ordering assignments. (#9368, @tenzen-y)
- TAS: fix a bug where NodeHotSwap may assign a Pod, based on rank-ordering, to a node which is already
  occupied by another running Pod. (#9282, @j-skiba)

## v0.16.1

Changes since `v0.16.0`:

## Changes by Kind

### Feature

- KueueViz backend and frontend resource requests/limits are now configurable via Helm values (kueueViz.backend.resources and kueueViz.frontend.resources). (#8981, @david-gang)

### Bug or Regression

- Fix Visibility API OpenAPI schema generation to prevent schema resolution errors when visibility v1beta1/v1beta2 APIServices are installed.
  
  The visibility schema issues result in the following error when re-applying the manifest for Kueue 0.16.0:
  `failed to load open api schema while syncing cluster cache: error getting openapi resources: SchemaError(sigs.k8s.io/kueue/apis/visibility/v1beta1.PendingWorkloadsSummary.items): unknown model in reference: "sigs.k8s.io~1kueue~1apis~1visibility~1v1beta1.PendingWorkload"` (#8901, @vladikkuzn)
- Fix a bug where finished or deactivated workloads blocked ClusterQueue deletion and finalizer removal. (#8936, @sohankunkerkar)
- LeaderWorkerSet: Fix the bug where rolling updates with maxSurge could get stuck. (#8886, @PannagaRao)
- LeaderWorkerSet: Fixed bug that doesn't allow to delete Pod after LeaderWorkerSet delete (#8882, @mbobrovskyi)
- Metrics certificate is now reloaded when certificate data is updated. (#9099, @MaysaMacedo)
- MultiKueue & ElasticJobs: fix the bug that the new size of a Job was not reflected on the worker cluster. (#9055, @ichekrygin)
- Observability: Fix Prometheus ServiceMonitor selector and RBAC to enable metrics scraping. (#8980, @IrvingMg)
- Observability: Fixed a bug where workloads that finished before a Kueue restart were not tracked in the gauge metrics for finished workloads. (#8827, @mbobrovskyi)
- Observability: fix the bug that the "replica-role" (leader / follower) log decorator was missing in the log lines output by
  the  webhooks for LeaderWorkerSet and StatefulSet . (#8820, @mszadkow)
- PodIntegration: Fix the bug that Kueue would occasionally remove the custom finalizers when
  removing the `kueue.x-k8s.io/managed` finalizer. (#8903, @mykysha)
- RayJob integration: Make RayJob top level workload managed by Kueue when autoscaling via
  ElasticJobsViaWorkloadSlices is enabled.
  
  If you are an alpha user of the ElasticJobsViaWorkloadSlices feature for RayJobs, then upgrading Kueue may impact running live jobs which have autoscaling / workload slicing enabled. For example, if you upgrade Kueue, before
  scaling-up completes,  the new pods will be stuck in SchedulingGated state. (#9039, @hiboyang)
- TAS: Fix a bug that TAS ignored resources excluded by excludeResourcePrefixes for node placement. (#8990, @sohankunkerkar)
- TAS: Fixed a bug that pending workloads could be stuck, not being considered by the Kueue's scheduler,
  after the restart of Kueue. The workloads would be considered for scheduling again after any update to their 
  ClusterQueue. (#9056, @sohankunkerkar)

### Other (Cleanup or Flake)

- KueueViz: It switches to the v1beta2 API (#8804, @mbobrovskyi)

## v0.16.0

Changes since `v0.15.0`:

## Urgent Upgrade Notes 

### (No, really, you MUST read this before you upgrade)

- Removed FlavorFungibilityImplicitPreferenceDefault feature gate.
  
  Configure flavor selection preference using the ClusterQueue field `spec.flavorFungibility.preference` instead. (#8134, @mbobrovskyi)
- The short name "wl" for workloads has been removed to avoid potential conflicts with the in-tree workload object coming into Kubernetes.
  
  If you rely on "wl" in your "kubectl" command, you need to migrate to other short names ("kwl", "kueueworkload") or a full resource name ("workloads.kueue.x-k8s.io"). (#8472, @kannon92)
 
## Changes by Kind

### API Change

- Add field multiplyBy for ResourceTransformation (#7599, @calvin0327)
- Kueue v0.16 starts using `v1beta2` API version for storage. The new API brings an optimization for the internal representation of TopologyAssignment (in WorkloadStatus) which allows using TAS for larger workloads (under the assumptions described in issue #7220, it allows to increase the maximal workload size from approx. 20k to approx. 60k nodes).
  
  All new Kueue objects created after the upgrade will be stored using `v1beta2`. 
  
  However, existing objects are only auto-converted to the new storage version by Kubernetes during a write request. This means that Kueue API objects that rarely receive updates - such as Topologies, ResourceFlavors, or long-running Workloads - may remain in the older `v1beta1` format indefinitely.
  
  Ensuring all objects are migrated to `v1beta2` is essential for compatibility with future Kueue upgrades. We tentatively plan to discontinue support for `v1beta1` in version 0.18.
  
  To ensure your environment is consistent, we recommend running the following migration script after installing Kueue v0.16 and verifying cluster stability: https://raw.githubusercontent.com/kubernetes-sigs/kueue/main/hack/migrate-to-v1beta2.sh. The script triggers a "no-op" update for all existing Kueue objects, forcing the API server to pass them through conversion webhooks and save them in the `v1beta2` version.
  Migration instructions (including the official script): https://github.com/kubernetes-sigs/kueue/issues/8018#issuecomment-3788710736. (#8020, @mbobrovskyi)
- MultiKueue: Allow up to 20 clusters per MultiKueueConfig. (#8614, @IrvingMg)

### Feature

- CLI: Support "kwl" and "kueueworkload" as a shortname for Kueue Workloads. (#8379, @kannon92)
- ElasticJobs: Support RayJob InTreeAutoscaling by using the ElasticJobsViaWorkloadSlices feature. (#8082, @hiboyang)
- Enable Pod-based integrations by default (#8096, @sohankunkerkar)
- Logs now include `replica-role` field to identify Kueue instance roles (leader/follower/standalone). (#8107, @IrvingMg)
- MultiKueue: Add support for StatefulSet workloads (#8611, @IrvingMg)
- MultiKueue: ClusterQueues with both MultiKueue and ProvisioningRequest admission checks are marked as inactive with reason "MultiKueueWithProvisioningRequest", as this configuration is invalid on manager clusters. (#8451, @IrvingMg)
- MultiKueue: trigger workload eviction on the management cluster when the corresponding workload is evicted
  on the remote worker cluster. In particular this is fixing the issue with workloads using ProvisioningRequests, 
  which could get stuck in a worker cluster which does not have enough capacity to ever admit the workloads. (#8477, @mszadkow)
- Observability: Add more details (the preemptionMode) to the QuotaReserved condition message,
  and the related event, about the skipped flavors which were considered for preemption. 
  Before: "Quota reserved in ClusterQueue preempt-attempts-cq, wait time since queued was 9223372037s; Flavors considered: main: on-demand(Preempt;insufficient unused quota for cpu in flavor on-demand, 1 more needed)"
  After: "Quota reserved in ClusterQueue preempt-attempts-cq, wait time since queued was 9223372037s; Flavors considered: main: on-demand(preemptionMode=Preempt;insufficient unused quota for cpu in flavor on-demand, 1 more needed)" (#8024, @mykysha)
- Observability: Introduce the counter metrics for finished workloads: kueue_finished_workloads_total and kueue_local_queue_finished_workloads_total. (#8694, @mbobrovskyi)
- Observability: Introduce the gauge metrics for finished workloads: kueue_finished_workloads and kueue_local_queue_finished_workloads. (#8724, @mbobrovskyi)
- Security: Support customization (TLSMinVersion and CipherSuites) for TLS used by the Kueue's webhooks server,
  and the visibility server. (#8563, @kannon92)
- TAS: extend the information in condition messages and events about nodes excluded from calculating the
  assignment due to various recognized reasons like: taints, node affinity, node resource constraints. (#8043, @sohankunkerkar)
- WaitForPodsReady.recoveryTimeout now defaults to the value of waitForPodsReady.timeout when not specified. (#8493, @IrvingMg)

### Bug or Regression

- DRA: fix the race condition bug leading to undefined behavior due to concurrent operations
  on the Workload object, manifested by the "WARNING: DATA RACE" in test logs. (#8073, @mbobrovskyi)
- FailureRecovery: Fix Pod Termination Controller's MaxConcurrentReconciles (#8664, @gabesaba)
- Fix ClusterQueue deletion getting stuck when pending workloads are deleted after being assumed by the scheduler. (#8543, @sohankunkerkar)
- Fix EnsureWorkloadSlices to finish old slice when new is admitted as replacement (#8456, @sohankunkerkar)
- Fix `TrainJob` controller not correctly setting the `PodSet` count value based on `numNodes` for the expected number of training nodes. (#8135, @kaisoz)
- Fix a bug that WorkloadPriorityClass value changes do not trigger Workload priority updates. (#8442, @ASverdlov)
- Fix a performance bug as some "read-only" functions would be taking unnecessary "write" lock. (#8181, @ErikJiang)
- Fix the race condition bug where the kueue_pending_workloads metric may not be updated to 0 after the last 
  workload is admitted and there are no new workloads incoming. (#8037, @Singularity23x0)
- Fixed a bug that Kueue's scheduler would re-evaluate and update already finished workloads, significantly
  impacting overall scheduling throughput. This re-evaluation of a finished workload would be triggered when:
  1. Kueue is restarted
  2. There is any event related to LimitRange or RuntimeClass instances referenced by the workload (#8186, @mbobrovskyi)
- Fixed a bug where workloads requesting zero quantity of a resource not defined in the ClusterQueue were incorrectly rejected. (#8241, @IrvingMg)
- Fixed the following bugs for the StatefulSet integration by ensuring the Workload object
  has the ownerReference to the StatefulSet:
  1. Kueue doesn't keep the StatefulSet as deactivated
  2. Kueue marks the Workload as Finished if all StatefulSet's Pods are deleted
  3. changing the "queue-name" label could occasionally result in the StatefulSet getting stuck (#4799, @mbobrovskyi)
- HC: Avoid redundant requeuing of inadmissible workloads when multiple ClusterQueues in the same cohort hierarchy are processed. (#8441, @sohankunkerkar)
- Integrations based on Pods: skip using finalizers on the Pods created and managed by integrations. 
  
  In particular we skip setting finalizers for Pods managed by the built in Serving Workloads  Deployments,
  StatefulSets, and LeaderWorkerSets.
  
  This improves performance of suspending the workloads, and fixes occasional race conditions when a StatefulSet
  could get stuck when deactivating and re-activating in a short interval. (#8530, @mbobrovskyi)
- JobFramework: Fixed a bug that allowed a deactivated workload to be activated. (#8424, @chengjoey)
- Kubeflow TrainJob v2: fix the bug to prevent duplicate pod template overrides when starting the Job is retried. (#8269, @j-skiba)
- LeaderWorkerSet: Fixed a bug that prevented deleting the workload when the LeaderWorkerSet was scaled down. (#8671, @mbobrovskyi)
- LeaderWorkerSet: add missing RBAC configuration for editor and viewer roles to kustomize and helm. (#8513, @kannon92)
- MultiKueue now waits for WorkloadAdmitted (instead of QuotaReserved) before deleting workloads from non-selected worker clusters. To revert to the previous behavior, disable the `MultiKueueWaitForWorkloadAdmitted` feature gate. (#8592, @IrvingMg)
- MultiKueue via ClusterProfile: Fix the panic if the configuration for ClusterProfiles wasn't provided in the configMap. (#8071, @mszadkow)
- MultiKueue: Fix a bug that the priority change by mutating the `kueue.x-k8s.io/priority-class` label on the management cluster is not propagated to the worker clusters. (#8464, @mbobrovskyi)
- MultiKueue: Fixed status sync for CRD-based jobs (JobSet, Kubeflow, Ray, etc.) that was blocked while the local job was suspended. (#8308, @IrvingMg)
- MultiKueue: fix the bug that for Pod integration the AdmissionCheck status would be kept Pending indefinitely,
  even when the Pods are already running.
  
  The analogous fix is also done for the batch/Job when the MultiKueueBatchJobWithManagedBy feature gate  is disabled. (#8189, @IrvingMg)
- MultiKueue: fix the eviction when initiated by the manager cluster (due to eg. Preemption or WaitForPodsReady timeout). (#8151, @mbobrovskyi)
- Observability: Revert the changes in PR https://github.com/kubernetes-sigs/kueue/pull/8599 for transitioning
  the QuotaReserved, Admitted conditions to `False` for Finished workloads. This introduced a regression,
  because users lost the useful information about the timestamp of the last transitioning of these
  conditions to True, without an API replacement to serve the information. (#8599, @mbobrovskyi)
- ProvisioningRequest: Fixed a bug that prevented events from being updated when the AdmissionCheck state changed. (#8394, @mbobrovskyi)
- Scheduling: fix a bug that evictions submitted by scheduler (preemptions and eviction due to TAS NodeHotSwap failing)
  could result in conflict in case of concurrent workload modification by another controller.
  This could lead to indefinite failing requests sent by scheduler in some scenarios when eviction is initiated by
  TAS NodeHotSwap. (#7933, @mbobrovskyi)
- Scheduling: fix the bug that setting (none -> some) a workload priority class label (kueue.x-k8s.io/priority-class) was ignored. (#8480, @andrewseif)
- TAS NodeHotSwap: fixed the bug that allows workload to requeue by scheduler even if already deleted on TAS NodeHotSwap eviction. (#8278, @mbobrovskyi)
- TAS: Fix a bug that MPIJob with runLauncherAsWorker Pod indexes are not correctly evaluated during rank-based ordering assignments. (#8618, @tenzen-y)
- TAS: Fix handling of admission for workloads using the LeastFreeCapacity algorithm when the  "unconstrained"
  mode is used. In that case scheduling would fail if there is at least one node in the cluster which does not have
  enough capacity to accommodate at least one Pod. (#8168, @PBundyra)
- TAS: Fixed an issue where workloads could remain in the second-pass scheduling queue (used for integration
  or TAS with ProvisioningRequests, and for TAS Node Hot Swap) even if they no longer require to be in the queue. (#8431, @skools-here)
- TAS: Fixed handling of the scenario where a Topology instance is re-created (for example, to add a new Topology level).
  Previously, this would cause cache corruption, leading to issues such as:
  1. Scheduling a workload on nodes that are fully occupied by already running workloads.
  2. Scheduling two or more pods of the same workload on the same node (even when the node cannot host both). (#8755, @mimowo)
- TAS: Lower verbosity of expected missing pod index label logs. (#8689, @IrvingMg)
- TAS: fix TAS resource flavor controller to extract only scheduling-relevant node updates to prevent unnecessary reconciliation. (#8452, @Ladicle)
- TAS: fix a performance bug that continues reconciles of TAS ResourceFlavor (and related ClusterQueues) 
  were triggered by updates to Nodes' heartbeat times. (#8342, @PBundyra)
- TAS: fix bug that when TopologyAwareScheduling is disabled, but there is a ResourceFlavor configured with topologyName, then preemptions fail with "workload requires Topology, but there is no TAS cache information". (#8167, @zhifei92)
- TAS: fixed performance issue due to unnecessary (empty) request by TopologyUngater (#8279, @mbobrovskyi)
- TAS: significantly improves scheduling performance by replacing Pod listing with an event-driven
  cache for non-TAS Pods, thereby avoiding expensive DeepCopy operations during each scheduling cycle. (#8484, @gabesaba)

### Other (Cleanup or Flake)

- Fix: Removed outdated comments incorrectly stating that deployment, statefulset, and leaderworkerset integrations require pod integration to be enabled. (#8053, @IrvingMg)
- Improve error messages for validation errors regarding WorkloadPriorityClass changes in workloads. (#8334, @olekzabl)
- MultiKueue: improve the MultiKueueCluster reconciler to skip attempting to reconcile and throw errors
  when the corresponding Secret or ClusterProfile objects don't exist. The reconcile will be triggered on 
  creation of the objects. (#8144, @mszadkow)
- Removes ConfigurableResourceTransformations feature gate. (#8133, @mbobrovskyi)

