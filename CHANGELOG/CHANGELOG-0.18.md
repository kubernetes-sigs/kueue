## v0.18.0

Changes since `v0.17.0`:

## Actions Required Before Upgrading

### (No, really, you MUST read this before you upgrade)

- **Minor releases:** Review the `.0` release notes for each new minor version you cross; see: [`v0.16.0`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.16.0), [`v0.17.0`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.0).

- AdmissionChecks: Add the alpha `RejectUpdatesToCQWithInvalidOnFlavors` feature gate (disabled by default) to reject updates to existing ClusterQueues with invalid `AdmissionCheckStrategy.OnFlavors` references. 
  when enabling this feature gate, fix any existing invalid `OnFlavors` references before updating the affected ClusterQueues. (#10384, @ShaanveerS)
 - If you maintain an in-house integration you will need to modify the code
  to pass the k8s client when calling the following functions: RunWithPodSetsInfo, PodSets, PodsReady, 
  ReclaimablePods. (#11310, @kaisoz)
 - Kueue integration webhooks now consistently exclude `kube-system` and the Kueue
  installation namespace. For manifest-based installs this is `kueue-system`; for Helm
  installs this is the Helm release namespace.
  
  Previously, Pod, Deployment, and StatefulSet integrations already excluded these
  namespaces, while other workload integrations did not.
  
  If you run Kueue-managed workloads in `kube-system` or in the namespace
  where Kueue is installed, move them to another namespace before upgrading.
  
  If this is not possible, update `managedJobsNamespaceSelector` and the relevant webhook
  `namespaceSelector`s to include those namespaces. This setup is discouraged, as it may
  affect system workloads and disrupt cluster upgrades. (#11192, @dkaluza)
 - Observability: Migrate from the old event types `core/v1` to use `events.k8s.io/v1`, by replacing the
  helper used in controller-runtime.
  
  if you compile your own "in house" integration then you need to adjust the code to replace 
  GetEventRecorderFor to GetEventRecorder when obtaining the event recorder. (#10971, @vladikkuzn)
 - Observability: Replace the "evicted_workloads_once_total" metric "detailed_reason" label with "underlying_cause" label. This is a consistency fix as all other metrics name the label "underlying_cause".
  
  If you use the "detailed_reason" label for the "evicted_workloads_once_total", you can migrate to "underlying_cause" label. (#10637, @vamsikrishna-siddu)
 - Removed the deprecated `--visibility-server-port` flag from the Kueue controller manager.
  
  If your installation passes `--visibility-server-port` to the
  controller manager, remove the flag before upgrading and set the same port via 
  Kueue Configuration API in `visibilityServer.bindPort` instead. (#11309, @ekam-walia)
 
## Changes by Kind

### Feature

- Add `WorkloadPriorityClassDefaulting` feature-gate to set a default WorkloadPriorityClass on Jobs if WorkloadPriorityClassLabel is not set and the `default` WorkloadPriorityClass exists (#10798, @atosatto)
- Add a new configuration named `quotaCheckStrategy` to be able to restrict the quota check to only resources which are declared in the `ClusterQueue`. (#9808, @MaysaMacedo)
- Add kubectl short names for AdmissionCheck (ac), Cohort (co), MultiKueueCluster (mkc), MultiKueueConfig (mkconf), ProvisioningRequestConfig (prc), Topology (topo), and WorkloadPriorityClass (wpc). (#11540, @gyliu513)
- Aggregate Kueue CRD read-only clusterRoles to k8s default view clusterRole (#10482, @amy)
- DRA: Add KueueDRARejectWorkloadsWhenDRADisabled feature gate (default: enabled, Beta) that rejects
  workloads using DRA resources when KueueDRAIntegration is disabled, preventing silent quota bypass. (#10964, @kannon92)
- DRA: Add counter-based quota support for partitionable DRA devices (e.g., NVIDIA MIG) via counterMappings configuration. (#10320, @PannagaRao)
- DRA: Promote KueueDRAIntegration feature gate to beta (#10996, @sohankunkerkar)
- Examples: Added a priority booster controller example that adjusts Workload priority
  using the `kueue.x-k8s.io/priority-boost` annotation, supporting time sharing in a
  ClusterQueue between Workloads with the same priority. (#9959, @vladikkuzn)
- Improve eviction message for AdmissionChecks in Retry state to include per-check name and reason (#10623, @reruno)
- Improved TAS balanced placement for LeaderWorkerSet: domains that meet the worker threshold stay usable even when they can't host the leader. (#10948, @ShaanveerS)
- Increased the maximum number of PodSets per Workload from 8 to 16. As a result, RayCluster, RayJob, and RayService integrations now allow up to 15 worker groups, since one PodSet is reserved for the Ray head group. (#11388, @yuluo-yx)
- Kueue-populator: Support creating LocalQueue instances named as their ClusterQueue target. (#9746, @samzong)
- Kueue-populator: Supports Kueue's managedJobsNamespaceSelector from the Kueue manager configuration instead of a separate Helm value. (#11218, @MatteoFari)
- LWS integration: Allow mutating the queue-name in LeaderWorkerSet when ReadyReplicas is zero. (#4932, @mbobrovskyi)
- MultiKueue: Add the `kueue.x-k8s.io/multikueue-worker-workload-pod=true` label to workload pods running on worker clusters. (#10242, @polinasand)
- MultiKueue: Keep manager ClusterQueue quota equal to the sum of workers' quotas. This is enabled by the `MultiKueueManagerQuotaAutomation` feature gate, and by `.QuotaManagement` in MultiKueueConfig. (#11141, @olekzabl)
- New kueue_cluster_queue_resource_pending metric to show total resources pending. (#10485, @amy)
- Observability:  Introduce the cohort info metrics and clusterqueue info metric. (#10004, @mszadkow)
- Observability: Added a new Prometheus metric `workload_creation_latency_seconds` to track the time elapsed between a Job's creation and the creation of its corresponding Workload by Kueue. (#10357, @Nilsachy)
- Observability: Improved FairSharing strategy-evaluation logs by including DRS share values and emitting them at verbosity level V(4). (#11165, @PBundyra)
- Observability: Introduced the workload_eviction_latency_seconds histogram metric, which records the time from when an eviction starts to when it is finalized. (#10323, @vladikkuzn)
- Promote ElasticJobsViaWorkloadSlices feature gate to beta (#11547, @sohankunkerkar)
- Promote MultiKueueRedoAdmissionOnEvictionInWorker to stable. (#10695, @mbobrovskyi)
- Promote MultiKueueWaitForWorkloadAdmitted to stable. (#10656, @mbobrovskyi)
- Promote SkipFinalizersForPodsSuspendedByParent to stable. (#10645, @mbobrovskyi)
- RBAC: Each per-resource editor and viewer ClusterRoles carries the label `rbac.kueue.x-k8s.io/role=<resource>-<access>` (e.g., `clusterqueue-viewer`). (#11205, @amy)
- Scheduling: Introduce Concurrent Admission, an alpha feature disabled by default, allowing
  Kueue to migrate admitted Workloads between ResourceFlavors to chase the optimal available capacity. (#10610, @PBundyra)
- Scheduling: re-enable SchedulingEquivalenceHashing by default, narrowed to NoFit-only to avoid incorrectly bulk-moving namespace-mismatched or preemption-gated workloads. (#11097, @sohankunkerkar)
- Support CEL expressions in DRA ResourceClaimTemplates. Kueue no longer rejects workloads that use CEL device selectors for filtering devices in resource claims. (#9742, @kannon92)
- TAS: Added support for `preferredDuringSchedulingIgnoredDuringExecution` node affinity. This feature is currently in alpha and is guarded by the `TASPreferredSchedulingAffinity` feature gate. (#10903, @j-skiba)
- TAS: optimize performance of building the snapshot by pre-aggregating the node usage coming from non-TAS Pods. (#10366, @jzhaojieh)
- Workload CRD now supports `--field-selector status.admission.clusterQueue=<name>` to filter by ClusterQueue server-side. `status.admission.clusterQueue` is set when a workload reserves quota in the CQ. (#11568, @mukund-wayve)

### Documentation

- AgentSkills: Add an agent skill for debugging Kueue CI flakes. (#11054, @mimowo)
- AgentSkills: New agent kueue related skills under cmd/experimental/agent/skills in the kueue repo (#10744, @amy)

### Failing Test

- Observability: Fix a bug where kueue_cohort_subtree_admitted_workloads_total and kueue_cohort_subtree_admitted_active_workloads metrics could include results for an implicit root Cohort after deletion of a child Cohort or ClusterQueue. (#10080, @mbobrovskyi)

### Bug or Regression

- AdmissionChecks: ClusterQueue validation now checks that the flavors specified in `AdmissionCheckStrategy.OnFlavors` are listed in quota. (#10336, @ShaanveerS)
- AdmissionChecks: fix the bug that on backoff admission checks which are spanning all ResourceFlavors, such as MultiKueue, may be missing in the Workload’s status. 
  
  For MultiKueue that manifested with a bug, when aside from the MultiKueue admission check there was another non-MultiKueue admission check. In the scenario when eviction on the management cluster happened the manager that had temporarily lost connection to a worker, the remote workload would keep running on the reconnected worker, despite the workload staying without reservation on the manager cluster. (#9359, @Singularity23x0)
- AdmissionFairSharing: Fixed a bug in entry penalties by reducing them when workload is admitted and also clearing them up if all the resources on the admission entry penalty have value zero. (#10156, @MaysaMacedo)
- DRA: Fix the performance of determining if a Workload should be handled using DRA Extended Resources, 
  using an event-driven maintained cache of DRA Extended Resources. (#10973, @PannagaRao)
- DRA: Fixed a bug where the kueue-controller-manager startup fails when DRA v1 APIs are not available (#11405, @PannagaRao)
- ElasticJobs: Fix a bug where pods stay gated after scale-up by allowing finished workloads to ungate their own pods. (#10272, @sohankunkerkar)
- ElasticJobsViaWorkloadSlices: Fix a bug where the workload-slice-name annotation was incorrectly set on all workloads when the ElasticJobsViaWorkloadSlices feature gate was enabled, instead of only on elastic workloads. (#11620, @sohankunkerkar)
- ElasticJobsViaWorkloadSlices: Fix quota leak during elastic workload scale-up where old slice was finished before replacement slice was admitted. (#11195, @sohankunkerkar)
- ElasticJobsViaWorkloadSlices: Fixed a bug where rapid elastic Job scaling could leave
  duplicate replacement Workload slices admitted indefinitely, causing quota to remain
  reserved. (#11327, @sohankunkerkar)
- ElasticJobsViaWorkloadSlices: Fixed a bug where updating the `kueue.x-k8s.io/elastic-job` annotation on a Job resulted in a validation error pointing to the incorrect path `metadata.labels` instead of `metadata.annotations`. (#11342, @weizhoublue)
- ElasticJobsViaWorkloadSlices: Fixed a bug where workload slices with identical creation timestamps could be incorrectly sorted, potentially leading to quota leaks during scale-up. (#11198, @KumarADITHYA123)
- FailureRecovery: Forcefully delete pods that are Failed/Succeeded and scheduled on unreachable nodes.
  This unblocks cases like a JobSet deleting a Job with foreground cascade being stuck because a pod in a terminal phase exists on one of the unhealthy nodes. (#10853, @kshalot)
- FailureRecoveryPolicy: Fixed an issue where pods could remain stuck terminating if their node became unreachable only after the force-termination timeout had already elapsed. (#10463, @kshalot)
- FairSharing: fix the bug which can cause nil-pointer panic when FairSharing is enabled on clusters
  with ClusterQueues without Cohorts. (#10891, @gyliu513)
- FeatureGates: Fix a bug that TAS-enhanced features can be enabled even if the dependent TopologyAwareScheduling or TASFailedNodeReplacement feature gates are disabled (#11781, @tenzen-y)
- FeatureGates: Fixed a bug that user-specified feature gate parameters are not verified. (#10931, @MaysaMacedo)
- Fix a bug in HA mode that caused follower replicas to retain stale workload caches after deletion. (#10518, @Ladicle)
- Fix a bug where finished Workloads could remain stuck after object retention deletion if they still had Kueue's resource-in-use finalizer. (#11181, @ShaanveerS)
- Fix a bug where the batch/v1 Job mutating webhook could still run even when the batch/job integration was disabled. (#10315, @Ladicle)
- Fix a race-condition bug that a deleted ClusterQueue may be kept by a finalizer, even after deletion of all workloads and LQs. (#10821, @ShaanveerS)
- Fix handling of orphaned workloads which could result in the accumulation of stale workloads
  after PodsReady timeout eviction for Deployment-owned pods. (#10274, @sebest)
- Fix waitForPodsReady timeout not triggering for pod groups with fast-admission when group members arrive after the first pod is Ready. (#11654, @sohankunkerkar)
- Fixed a bug that prevented finishing the Workloads corresponding to Jobs deleted with --cascade=orphan. (#11596, @mbobrovskyi)
- Fixed a bug where admitted Workloads could fail to patch through the v1beta1 API due to CEL validation of the `priorityClassSource` immutability rule. (#10594, @kannon92)
- Fixed a regression where Kueue could mark newly created Workloads as finished, potentially blocking queues. The FinishOrphanedWorkloads feature gate has been downgraded to Alpha. (#11010, @mbobrovskyi)
- Fixed multi-arch image builds for importer, kueue-populator, and kueueviz backend images so runtime images
  and binaries are built for the target platform, preventing wrong-architecture containers and exec format error
  for non-amd64 target platforms, such as arm64, ppc64le, and s390x. (#10775, @carterpewpew)
- Fixed vulnerability where two podsets with total requests exceeding max int64 would lead to integer overflow and break quota limits. (#11137, @pajakd)
- Fixed vulnerability where workload with CPU requests close to max int64 would lead to integer overflow when converting to milicpu and break quota limits. (#11139, @pajakd)
- Fixed vulnerability where workload with a large number of pods and with CPU requests close to max int64 would lead to integer overflow when calculating total requests and break quota limits (#11182, @pajakd)
- Helm: Fixed manager probe templates so periodSeconds correctly uses the configured periodSeconds value,
  rather than initialDelaySeconds. (#10978, @cixuuz)
- Helm: Fixed the FlowSchema priorityLevelConfiguration reference to use the Helm fullname template, preventing APF configuration from breaking when fullnameOverride or nameOverride is set. (#10979, @cixuuz)
- KueueViz: Fixed RBAC permissions for WorkloadPriorityClass objects by using the correct plural workloadpriorityclasses resource name. (#10977, @cixuuz)
- KueueViz: Fixed a bug where list pages briefly flashed "No resources found" empty state messages before data finished loading over the WebSocket connection. (#11711, @YadavAkhileshh)
- KueueViz: Fixed a bug where non-JSON WebSocket messages from the backend could crash
  the frontend. KueueViz now reports such messages as errors instead of freezing the UI. (#11575, @YadavAkhileshh)
- KueueViz: Fixed a bug where workload and local queue detail pages would get stuck on a loading spinner instead of showing connection/fetching error messages on failure. (#11701, @YadavAkhileshh)
- KueueViz: Fixed frontend crashes caused by non-string errors in the error display. (#11566, @YadavAkhileshh)
- KueueViz: Fixed the LocalQueue details page to show only workloads from the selected queue. (#11199, @ManthanNimodiya)
- KueueViz: Fixed the navigation bar to avoid layout breakage on narrow mobile screens. (#11322, @YadavAkhileshh)
- LeaderWorkerSet & Pods: Fixed a bug where LeaderWorkerSets with names longer than 39 characters failed to create pods with a `metadata.labels: Invalid value` error. This happened when the `kueue.x-k8s.io/pod-group-name` and
  `kueue.x-k8s.io/prebuilt-workload-name` labels, set by the LWS integration, exceeded 63 characters. 
  
  The `WorkloadIdentifierAnnotations` feature gate (enabled by default) resolves this by supporting these identifiers
  as both labels and annotations. LeaderWorkerSet now utilizes the annotation counterparts to support names up to
  52 characters. Labels, now along with annotations, remain the user-facing API for manually defining PodGroups. (#10311, @ivnovakov)
- LeaderWorkerSet & StatefulSet: Fixed a race condition bug that could occasionally result in reverting, at the level of the Workload object, manual changes to the queue-name label for LeaderWorkerSet and StatefulSet. (#11191, @mbobrovskyi)
- LeaderWorkerSet integration: fix the bug that the PodTemplate metadata wasn't propagated to the Workload's PodSets. (#10330, @pajakd)
- MultiKueue: Add reconnect backoff guardrail to suppress redundant cluster reconciles for the MultiKueueCluster reconciler. (#10990, @reruno)
- MultiKueue: Fixed a bug in the AllAtOnce dispatcher where workloads evicted from a
  worker cluster could fail to be re-admitted. Kueue now waits for the ongoing eviction to
  complete before starting a new nomination and re-admission cycle. (#11378, @mszadkow)
- MultiKueue: Fixed a bug where a hung watch connection to one remote cluster could block
  reconciliation of other MultiKueueClusters, leaving them inactive and preventing workload
  admission. Kueue now applies a circuit-breaking timeout while establishing remote-cluster
  watches: the timeout starts at 1 minute and backs off exponentially on consecutive failures,
  up to 10 minutes. (#11304, @trilamsr)
- MultiKueue: Fixed a bug where a lost connection to a worker cluster failed to trigger the "workerLostTimeout" delay mechanism for workload requeuing. (#11559, @yuluo-yx)
- MultiKueue: Fixed a bug where one slow or unresponsive remote cluster could stall
  reconciliation for other MultiKueueClusters, even when
  `controller.groupKindConcurrency["MultiKueueCluster.kueue.x-k8s.io"]` was set above 1.
  This could delay or block admission through other healthy clusters. (#11305, @trilamsr)
- MultiKueue: Fixed admission for Kubernetes Jobs on Kubernetes 1.36 clusters by ensuring all Job status updates comply with the updated Kubernetes Job validation rules. See kubernetes/kubernetes#139281 for more details. (#11649, @olekzabl)
- MultiKueue: Fixed unnecessary `status.nominatedClusterNames` updates from the AllAtOnce
  dispatcher when the set of nominated clusters did not change. (#11497, @mszadkow)
- MultiKueue: Fixes the bug where a job, after being dispatched to a worker, would not sync correctly after being evicted there. This would also cause its workload to be incorrectly labeled as admitted.
  
  Now the workload and the manager job instance will correctly reflect the evicted state and MultiKueue will perform a fallback, then dispatch remote workloads to all eligible workers again after being evicted from the Worker it was successfully admitted to before. An example of such a case is if the remote instance got preempted on the worker. (#9670, @Singularity23x0)
- MultiKueue: fix the bug that when custom admission checks are configured on the manager cluster, other than
  the MultiKueue admission check, then the Job may start running on the selected worker before the other admission
  checks are satisfied (Ready). We fix the issue by deferring the dispatching of workload until all non-MultiKueue AdmissionChecks become Ready. (#9866, @mszadkow)
- ObjectRetentionPolicies: Fixed a bug that doesn't allow the deletion of orphaned finished workloads. (#11721, @mbobrovskyi)
- Observability: Fix excessive memory overhead in hot code paths by reusing the named logger in NewLogConstructor and avoiding unnecessary logger cloning. (#10365, @MatteoFari)
- Observability: Fix kueue_cohort_subtree_quota and kueue_cohort_subtree_resource_reservations metrics incorrectly reporting raw milliCPU values instead of CPU units for CPU resources. (#10747, @baoalvin1)
- Observability: avoid logging update failures as "error" when they are caused by concurrent object modifications, especially when multiple errors are present.
  
  Example log message: "failed to update MultiKueueCluster status: Operation cannot be fulfilled on multikueueclusters.kueue.x-k8s.io \"testing-cluster\": the object has been modified; please apply your changes to the latest version and try again after failing to load client config: open /tmp/kubeconfig no such file or directory" (#10322, @mbobrovskyi)
- Observability: downgrade the non-compatible flavor error logs to Info level (v3). (#10636, @maishivamhoo123)
- Observability: fix the missing "replica-role" information from the logs generated by the controller managing the
  MultiKueueCluster instances. (#11153, @reruno)
- Scheduling: Fix a race condition within the admission process that could cause workloads waiting indefinitely for a preemption, causing head-of-line blocking of the affected ClusterQueues. (#11502, @kshalot)
- Scheduling: Fixed a bug in Kueue's cache that could leave stale SubtreeQuota values in ancestor cohorts after a child Cohort
  was deleted, leading to potential over-admission of workloads and incorrect metrics reporting. (#10797, @mszadkow)
- Scheduling: Fixed a bug where Kueue could inject duplicate tolerations when a
  ResourceFlavor toleration and a PodTemplate toleration differed only by `operator: ""`
  versus `operator: "Equal"`, which represent the same Kubernetes toleration. This could
  cause update rejections and leave Pods `scheduleGated`. (#11147, @benkermani)
- Scheduling: Fixed a bug where in-flight workloads that were concurrently marked as finished (`Finished=True`) or deactivated could be requeued by Kueue's scheduler, causing re-scheduling attempts which were interfering with the scheduling of other workloads. (#11014, @mbobrovskyi)
- SparkApplication: Fixed a bug where `PodsReady` returned true based on driver state alone, so workloads
  with stuck executors never reached `waitForPodsReady.timeout`. (#11676, @hahahaheihei)
- TAS: Balanced Placement now also falls back to BestFit when the threshold cannot be satisfied, instead of failing the assignment. (#11136, @ShaanveerS)
- TAS: Fix a bug where admitted workloads with unhealthy nodes were not evicted when an AdmissionCheck entered Retry or when the PodsReady recovery timeout was exceeded. (#10666, @vamsikrishna-siddu)
- TAS: Fix empty slices for count=0 podSets causing infinite scheduling loop (#10478, @jzhaojieh)
- TAS: Fix handling of PodSet groups which could lead in some scenarios to empty topologyAssignment. (#10783, @yuluo-yx)
- TAS: Fix nil pointer panic in TAS node reconciler when unadmitted workloads exist in the cluster. (#10641, @j-skiba)
- TAS: Fixed NodeHotSwap with TASReplaceNodeOnNodeTaints enabled to evaluate node taints using effective Workload tolerations, including tolerations from AdmissionCheck PodSetUpdates. (#11185, @Ladicle)
- TAS: Fixed a bug where multi-resource workloads, such as workloads requesting both CPU and memory,
  could fail admission during second-pass scheduling for ProvisioningRequests or NodeHotSwap because one
  resource's usage was double-counted against quota. (#11005, @cvgenesis)
- TAS: Fixed a scheduling bug where a workload with multiple PodSets could be admitted even when the combined PodSets exceeded node pod capacity. (#11293, @yuluo-yx)
- TAS: Fixed cache cleanup for non-TAS Pods that reach a terminal phase without Kueue observing the expected status update, preventing stale Pod usage from remaining in the TAS cache. (#11033, @amy)
- TAS: Refine the NodeHotSwap logic to ensure that UnhealthyNodes are only updated for workloads currently assigned to a Node via a topology topology assignment. This prevents "late pods" from stale topologies from triggering inaccurate health reporting. (#10760, @j-skiba)
- TAS: ensure that `Snapshot()` does not perform update the list of workloads under the read-lock. (#11286, @mimowo)
- TAS: fix a bug that Pods which only contain the `kueue.x-k8s.io/podset-slice-required-topology` or `kueue.x-k8s.io/podset-slice-required-topology-constraints` as the TAS annotation are not ungated. (#10282, @tg123)
- TAS: fix over-subscription of nodes that belong to multiple ResourceFlavors sharing the same hostname-leaf Topology. (#11210, @tenzen-y)
- TAS: reduce the churn on the TAS-enabled controller, called NonTasUsageReconciler, by skipping triggering
  of the Reconcile on Pod changes which are irrelevant from the controller point-of-view. (#10488, @MatteoFari)
- VisibilityOnDemand: Fixed a bug in the visibility endpoint, that listing workloads from a local queue includes
  workloads from other LocalQueues in different namespaces, if the other LocalQueues have the same name. (#10672, @mbobrovskyi)
- Workloads: Fixed a bug where, with the `FinishOrphanedWorkloads` feature gate enabled,
  Workloads could be marked `Finished` immediately after owner Job or JobSet creation.
  `FinishOrphanedWorkloads` is now enabled by default as a beta feature, allowing orphaned
  Workloads to be immediately finished and release quota after owner deletion. (#11296, @mbobrovskyi)

### Other (Cleanup or Flake)

- MultiKueue: improved performance by avoiding unnecessary MultiKueueCluster reconciliations.
  MultiKueueCluster updates now trigger reconciliation only when the spec changes or the object is deleted. (#11001, @reruno)

