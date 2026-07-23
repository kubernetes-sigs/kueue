## v0.19.0

Changes since `v0.18.0`:

## Actions Required Before Upgrading

### (No, really, you MUST read this before you upgrade)

- **Minor releases:** Review the `.0` release notes for each new minor version you cross; see: [`v0.17.0`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.0), [`v0.18.0`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.18.0).

- If you maintain an in-house integration you will need to modify the code
  to pass the k8s context when calling the `RestorePodSetsInfo` function. (#13114, @ivnovakov)
 - KueuePopulator Helm: `helm uninstall` removes the ClusterQueue, ResourceFlavor, Topology, ConfigMap, and RBAC created by the chart, which previously leaked after uninstall.
  
  If you installed a previous version of the kueue-populator chart, its ConfigMap and RBAC (`*-kueue-hook-*` ServiceAccount/ClusterRole/ClusterRoleBinding and the `*-kueue-resources` ConfigMap) were created as Helm hooks and are not adopted by the new release. Delete them manually before upgrading to avoid `helm upgrade`/`install` ownership conflicts. (#12402, @kevin85421)
 - MultiKueue: Fixed a security vulnerability in `locationType=Path` kubeconfig handling
  that could allow users with `MultiKueueCluster` create or update access to make the
  controller read arbitrary files. Kueue now validates path-based kubeconfigs to stay under
  `/etc/multikueue/kubeconfigs`.
  
  If you use `locationType=Path`, plan to move kubeconfig files under
  `/etc/multikueue/kubeconfigs`, or switch to `locationType=Secret` or `ClusterProfile`.
  This prepares your setup for future releases where `MultiKueueKubeConfigPathValidation`
  is expected to be enabled by default. (#12223, @kannon92)
 - RayCluster: Fixed a bug where the Ray autoscaler sidecar container's resources were not counted against quota when in-tree autoscaling was enabled, causing the head PodSet to be under-counted. The head PodSet now includes the autoscaler sidecar (KubeRay's default `500m` CPU / `512Mi` memory, or `spec.autoscalerOptions.resources` when set).
  
  users with autoscaling-enabled RayClusters may need to increase their ClusterQueue CPU quota by 500m and memory quota by 512Mi per head pod to avoid admission failures after upgrading. (#12405, @kevin85421)
 - RayJob: Fixed a bug where the Ray job submitter container's resources were not counted against quota when `submissionMode: SidecarMode` was used, causing the head PodSet to be under-counted. The head PodSet now includes the submitter sidecar (KubeRay's default `500m` CPU / `200Mi` memory).
  
  After upgrading, RayJobs using `submissionMode: SidecarMode` reserve the submitter sidecar's resources (default `500m` CPU / `200Mi` memory) on the head. ClusterQueues sized without this headroom may fail to admit such RayJobs; increase the affected ClusterQueue's CPU/memory quota accordingly. (#12454, @kevin85421)
 - TAS: A negative `subGroupCount` on a Workload now produces an admission warning. 
  
  Starting with the 0.20 release, a negative `subGroupCount` will be rejected at the API level. (#13101, @reruno)
 - WaitForPodsReady is now enabled by default. New Kueue installations and existing installations that do not explicitly configure `waitForPodsReady` will use the default WaitForPodsReady configuration (30 minute timeout, 30 minute recovery timeout). (#11855, @amirialy)
 
## Changes by Kind

### Deprecation

- DRA: Remove the deprecated `DynamicResourceAllocation` feature gate. Use `KueueDRAIntegration` instead. (#12258, @kshalot)
- MultiKueue: Added `accessProviders` as the preferred `ClusterProfile` field for
  configuring cluster access providers. The existing `credentialsProviders` field remains
  supported but is deprecated and cannot be used together with `accessProviders`. (#12011, @kahirokunn)

### API Change

- Use `SchemeGroupVersion` instead of `GroupVersion` in the API.
  
  If your code references the `GroupVersion` variable from the API, update it to use `SchemeGroupVersion` instead. (#12738, @mbobrovskyi)

### Feature

- AFS Observability: Added `kueue_local_queue_admission_fair_sharing_usage` Prometheus metric to report AFS usage per LocalQueue, calculated from the resource-weighted sum of consumed resources and pending admission penalties, and divided by the LocalQueue's fair sharing weight. (#12326, @ShaanveerS)
- ConcurrentAdmission: Fixed Variants not being created or deleted when a ClusterQueue's resource flavors change. (#12501, @ivnovakov)
- ConcurrentAdmission: make sure there is at most one preemption variant issuing preemptions at any given time.
  This is achieved using the "preemption gates" mechanism. (#11872, @reruno)
- DRA Partitionable Devices: support multi-counter tracking by allowing the same DeviceClass in multiple deviceClassMappings with different counter sources. Add ResourceSliceCache for consolidated ResourceSlice listing. (#13018, @PannagaRao)
- DRA: Adds capacity-based quota for DRA devices with multiple allocations. (#13152, @sohankunkerkar)
- Graduate KueueDRAIntegrationExtendedResource to Beta (enabled by default) (#13102, @PannagaRao)
- Graduate KueueDRAIntegrationPartitionableDevices to Beta (enabled by default) (#13167, @PannagaRao)
- Graduate ManagedJobsNamespaceSelectorAlwaysRespected to GA (#13021, @PannagaRao)
- Graduate the AdmissionGatedBy feature gate to Beta, enabled by default. Users who previously had to manually enable this gate no longer need to. Users who do not use the `kueue.x-k8s.io/admission-gated-by` annotation are unaffected. (#12110, @carterpewpew)
- Helm: Added enableVisibilityAuthReaderRoleBinding Helm value (default: true) to make the visibility server's auth-reader RoleBinding in kube-system optional. Set to false when deploying under a GitOps project that cannot manage resources in kube-system, and create the RoleBinding out-of-band instead. (#12699, @amy)
- Increase OOTB QPS and concurrency for Kueue: QPS: 300, Burst=500, Workload concurrency: 10, LQ and CQ: 5. (#12440, @yuluo-yx)
- KueueViz: Added a global rate limiter to the KueueViz backend to protect against distributed Denial of Service (DoS) attacks and TokenReview amplification. (#13173, @Vaishnav88sk)
- MultiKueue Observability: Added a new metric `multikueue_workloads_dispatched_total` to count remote workloads successfully created by the MultiKueue manager per worker cluster. (#12782, @Mostafahassen1)
- MultiKueue: Added a new metric `multikueue_workloads_admitted_total` that counts remote workloads admitted by a worker cluster, labeled by `cluster_queue`, `cluster`, and `replica_role`. (#13050, @Mostafahassen1)
- MultiKueue: Elastic RayCluster worker-group replica changes made on the management cluster (via the `ElasticJobsViaWorkloadSlices` feature gate) now propagate to the RayCluster on the admitting worker cluster. Previously the remote RayCluster was created once and never resized. (#12885, @jiaoew1991)
- MultiKueue: The incremental dispatcher now nominates worker clusters in the order defined in `MultiKueueConfig.spec.clusters` instead of alphabetically, enabling priority-based spillover (for example, trying cheaper on-premises clusters before public-cloud clusters). (#13041, @andrewseif)
- MultiKueue: provide stepSize configuration for the Incremental Dispatcher. (#11208, @Mostafahassen1)
- Observability: Added `kueue_unadmitted_workloads` and `kueue_local_queue_unadmitted_workloads` metrics (gated by `UnadmittedWorkloadsObservability`) to track the count of unadmitted workloads by ClusterQueue/LocalQueue and the underlying blockage cause (e.g., `WaitingForQuota`, `ChecksNotReady`). (#12759, @j-skiba)
- Observability: Added granular Kubernetes warning event reasons for unadmitted
  Workloads, such as `WaitingForQuota`, `NoMatchingFlavor`,
  `ExceedsMaxQuota`, and `TopologyPlacementFailed`, matching the reason reported
  in the `QuotaReserved` condition. The feature is disabled by default,
  and guarded by `UnadmittedWorkloadsObservability`. The related proactive
  initialization of explicit unadmitted status conditions is separately guarded
  by `UnadmittedWorkloadsExplicitStatus`, which is also disabled by
  default, and requires `UnadmittedWorkloadsObservability`. (#13022, @j-skiba)
- Observability: Added support for the UnadmittedWorkloadsObservability feature gate in the workload controller. When enabled, Kueue populates the QuotaReserved workload condition with granular reasons (such as Misconfigured, Suspended, or AdmissionGated) and detailed messages when a workload cannot be admitted, making it easier for operators to diagnose admission issues. (#12510, @j-skiba)
- Observability: Added the `kueue_pod_scheduling_gate_removal_seconds` histogram metric to
  measure the time from Workload admission to Pod scheduling-gate removal, helping operators
  track delays before admitted Pods can be scheduled. (#12137, @mbobrovskyi)
- Observability: Updated the alpha custom metric labels API with support for source-specific labels, including labels sourced from Workloads, and allowlisting of tracked label values. The feature remains disabled by default and can be enabled using the `CustomMetricLabels` feature gate. (#12713, @Singularity23x0)
- Observability: When enabling custom metric labels, workloads will automatically copy appropriate labels
  and annotations from underlying jobs. PodSets must match annotation and label values defined as custom
  metric label value sources across component Pods if feature enabled. (#13146, @Singularity23x0)
- Promoted QuotaCheckStrategy to Beta and enabled by default. (#13075, @MaysaMacedo)
- Scheduling: Added the `UnadmittedWorkloadsExplicitStatus` feature gate. When enabled, newly created workloads immediately receive explicit unadmitted status conditions (`QuotaReserved=False` and `Admitted=False`) during initial reconciliation to improve queue state observability. (#12719, @j-skiba)
- Scheduling: Workloads bypassed by the scheduling equivalence cache now receive the granular failure reason (e.g., `WaitingForQuota`) and a bypass message in their `QuotaReserved` condition, improving visibility into why the workload was unadmitted. (#12821, @j-skiba)
- Security: Added `curvePreferences` to `TLSOptions`, allowing administrators to
  restrict the TLS key-exchange groups used by Kueue's TLS-enabled servers to an
  approved set. Values are specified as numeric IANA TLS Supported Group IDs; when
  the option is omitted, Go's default selection is used. (#11832, @kannon92)
- TAS: Added the `TASAssignmentsEncodingByHostnamePrefix` feature gate (Beta, enabled by default). When enabled, Kueue uses hostname-prefix encoding for all hostname-level topology assignments, improving compaction for large assignments and supporting assignments that exceed the legacy single-slice limits. 
  
  The new encoding allows the use of TAS for workloads spanning more than 100k nodes for most clusters. You can find more detailed compaction statistics in the PR description. (#11579, @ShaanveerS)
- TAS: Graduated `TASMultiLayerTopology` to Beta and enabled it by default which 
  allows to configure multi-layer slice topology constraints per workload. (#12290, @ekam-walia)
- WaitForPodsReady: Introduce the `DisableWaitForPodsReady` feature gate to allow disabling WaitForPodsReady.
  This is a temporary knob, and will be removed in a future release provided no feedback which requests a permanent
  knob. (#13107, @amirialy)
- When the `UnadmittedWorkloadsObservability` feature gate is enabled, workloads that fail to obtain a quota reservation now receive detailed diagnostic reasons in their `QuotaReserved` status condition (such as `WaitingForQuota`, `ExceedsMaxQuota`, `TopologyPlacementFailed`, or `NoMatchingFlavor`) along with an explicit `Admitted: False` condition. (#12452, @j-skiba)
- Workload: When `UnadmittedWorkloadsObservability` is enabled, clearing workload quota reservation in the ConcurrentAdmission controller reports `QuotaReserved: False` with the `PendingEvaluation` reason instead of `Pending`. (#13020, @j-skiba)
- Workload: When `UnadmittedWorkloadsObservability` is enabled, releasing quota reservation in JobFramework and StatefulSet controllers reports `QuotaReserved: False` with the `PendingEvaluation` reason instead of `Pending`. (#13019, @j-skiba)
- WorkloadAwareScheduler: Add the `SchedulerLibraryIntegration` feature gate. Perform the node domain TAS fit check using the `scheduler-library` (https://github.com/kubernetes-sigs/scheduler-library). (#13261, @kshalot)
- Workloads: Increase the maximum number of PodSets per Workload from 10 to 18. (#12819, @mcochner)

### Documentation

- Docs: Added a version dropdown to the docs site navbar for switching between v0.16, v0.17, v0.18, and the current development version. (#13040, @baoalvin1)
- Documentation: Add a deprecation note for the legacy Kubeflow Trainer v1. Users are notified that the integration
  is deprecated and will be removed in a future release. (#12601, @mimowo)
- Documentation: the Kueue webpage is redesigned and modernized. (#12412, @MichalZylinski)

### Bug or Regression

- AFS: Fixed ConsumedResources CPU truncating to zero when the sampling interval guard was bypassed by informer cache lag during initialization. (#12671, @sohankunkerkar)
- AFS: Fixed a Denial of Service (DoS) vulnerability where deleting a LocalQueue could cause the Kueue scheduler to hang during AdmissionFairSharing calculations. (#13214, @Vaishnav88sk)
- AFS: Fixed a race in Admission Fair Sharing penalty updates where concurrent workload operations could lose penalty changes, causing LocalQueues to receive incorrect priority. (#12697, @MaysaMacedo)
- AFS: Fixed a race where a sampling tick running concurrently with workload settlement could persist a skewed ConsumedResources value in LocalQueue fair-sharing status. (#12939, @apullo777)
- AFS: Fixed consumed-resources cache initialization and warm-start recovery so LocalQueue usage is not over-counted during cache seeding, and persisted historical usage is preserved after manager restarts when workload settlement runs before LocalQueue reconciliation. (#12891, @apullo777)
- CLI: Fix --dry-run flag being silently ignored in kueuectl resume/stop localqueue and clusterqueue subcommands. (#12617, @carterpewpew)
- ConfigAPI: Fixed TLS configuration validation to report all detected option errors instead of only the last one. (#12292, @kannon92)
- ConfigAPI: TLS: Fixed a bug where invalid webhook server TLS settings could be silently ignored,
  causing the webhook server to start with Go TLS defaults. Kueue now fails startup with a
  clear configuration error, matching metrics server TLS validation behavior. (#12293, @kannon92)
- DRA: Fix an integer overflow in device-count quota accounting where a ResourceClaimTemplate with very large device counts could be admitted over quota and leave a negative used-quota in the ClusterQueue status. (#12897, @thc1006)
- DRA: Fix workloads retaining DRA-mapped resource names after their DeviceClass is deleted. (#11927, @sohankunkerkar)
- DRA: Fixed a bug where byte-valued Partitionable Devices (counter-based) resources were displayed as raw byte integers in Workload and ClusterQueue status. 
  These resources are formatted using human-readable BinarySI units, such as Mi and Gi. (#12989, @amarkdotdev)
- DRA: Fixed a bug where pending Workloads using DRA extended resources were not requeued when their `DeviceClass` was deleted or its `extendedResourceName` changed. Kueue now re-evaluates affected Workloads so they do not remain in stale admission state. (#11929, @sohankunkerkar)
- DRA: Fixed a bug where workloads with device constraints (matchAttribute) or device config were incorrectly rejected as unsupported instead of being admitted for quota. (#12451, @sohankunkerkar)
- DRA: Fixed configuration validation to reject `deviceClassMappings[].sources` when the `KueueDRAIntegrationPartitionableDevices` feature gate is disabled, preventing unsupported partitionable-device configuration from being accepted. (#12134, @sohankunkerkar)
- DRA: Fixed hot reconcile loops for inadmissible Workloads with deterministic DRA resolution
  failures. Kueue now avoids requeueing permanent DRA spec or configuration errors while still
  retrying transient failures with backoff. (#12002, @thc1006)
- DRA: Fixed incorrect quota charging for invalid driver-published device counters by clamping them to the non-negative 
  int64 range before computing quota charges. (#12945, @thc1006)
- DRA: fixed a potential int64 overflow in the counter-based device quota charge computation that could under-count quota when a driver publishes very large counter values. (#12909, @thc1006)
- DRA: introduce a safeguard for invalid parameter combinations to prevent nil dereference crashes (#12889, @mykysha)
- ElasticJobsViaWorkloadSlices: Fix the bug that regular (non-elastic) workloads with the required/preferred topology
  were rejected when the feature ElasticJobsViaWorkloadSlicesWithTAS is enabled. (#11997, @yaroslava-serdiuk)
- ElasticJobsViaWorkloadSlices: Fix workload slice misordering that could finish a correctly-admitted elastic workload slice when 3+ slices were created within the same second. (#12931, @sohankunkerkar)
- ElasticJobsViaWorkloadSlices: Fixed a bug that allowed a replacement Workload slice to reference a Workload from another namespace when both used the same ClusterQueue, potentially causing the unrelated Workload to be treated and finished as the replaced slice. Workload slice replacements are now restricted to Workloads in the same namespace. (#13071, @mykysha)
- ElasticJobsViaWorkloadSlices: Fixed a bug that could cause elastic Jobs to
  stall after Pods succeeded or failed, because terminal Pods continued to count
  against the active Workload slice's admitted PodSet count and prevented
  replacement Pods from being ungated. (#13126, @garg02)
- ElasticJobsViaWorkloadSlices: Fixed a bug where an elastic job could permanently fail to start (FailedToStart) due to stale Kueue-owned annotations on the pod template, e.g. after its workload was deleted, or after eviction of a previously scaled-up job. (#12994, @mcochner)
- ElasticJobsViaWorkloadSlices: Fixed a bug where reclaimable Pod accounting after scaling down an elastic Job could reserve quota for Pods that were no longer running. Reserved quota now tracks the remaining running Pods for indexed and non-indexed Jobs. (#13178, @Shreesha001)
- ElasticJobsViaWorkloadSlices: Fixed a bug where scaling a Job below its accumulated succeeded count could permanently wedge the Workload reconciler and leak quota. (#12766, @Shreesha001)
- ElasticJobsViaWorkloadSlices: Fixed a bug where scaling down an elastic Job could leave a stale reclaimablePods count, causing Kueue to account for less quota than the Job's remaining Pods were using. (#13044, @Shreesha001)
- ElasticJobsViaWorkloadSlices: Fixed a bug where worker pods of an elastic job could be ungated after scale up, 
  past the ClusterQueue quota; ungating is now capped to the replicas granted quota across the workload-slice chain. (#12045, @mcochner)
- Helm: Fix helm chart failing to install with a manager CrashLoopBackoff when cert-manager integration is enabled. (#12859, @meln5674)
- Importer: Fixed LocalQueue namespace isolation to prevent information leakage between
  namespaces when multiple LocalQueues with the same name exist in different namespaces. (#12309, @Singularity23x0)
- Kueue-populator: Fixed `events.k8s.io` RBAC permissions for event recording. (#11975, @weizhoublue)
- Kueue-populator: Fixed a bug where an error creating a LocalQueue was logged but not returned from Reconcile, 
  preventing controller-runtime from retrying. LocalQueue creation failures are now aggregated and returned so the request is requeued. (#12795, @NasitSony)
- KueueViz: Fixed a Cross-Site WebSocket Hijacking (CSWSH) vulnerability in the KueueViz Backend by strictly validating WebSocket Origin headers to prevent unauthorized cross-origin data extraction. (#12734, @Vaishnav88sk)
- KueueViz: Fixed a Denial of Service vulnerability where an oversized WebSocket frame could exhaust backend memory (OOM). Connections now enforce an 8 KiB read limit. (#12614, @ABHIGYAN-MOHANTA)
- KueueViz: Fixed a bug where the dashboard briefly displayed zero counts for all metrics on page load before the WebSocket connection finished loading. (#11853, @YadavAkhileshh)
- KueueViz: Fixed a layout-bleed bug where switching directly between detail pages briefly rendered stale queue data from the previously visited resource. (#11878, @YadavAkhileshh)
- KueueViz: Fixed a security issue in kueueviz where WebSocket connections continued streaming cluster data after a bearer token expired or was revoked.
  Connections are now closed within 30 seconds of token invalidation. (#12698, @Vaishnav88sk)
- KueueViz: Fixed dashboard crash caused by missing optional chaining on flavor.resources (#12613, @ABHIGYAN-MOHANTA)
- KueueViz: Improved workloads dashboard performance by avoiding repeated Pod list operations per Workload (#12777, @cryo-zd)
- KueueViz: Navigating to an invalid cohort now displays a graceful error message instead of crashing the UI. (#13174, @Vaishnav88sk)
- KueueViz: Prevent workload detail pages from crashing when Kubernetes Events have missing or invalid timestamps. (#13195, @YQ-Wang)
- KueueViz: backend includes HTTP server timeouts (ReadHeaderTimeout, ReadTimeout, WriteTimeout, IdleTimeout) to prevent connection resource exhaustion. (#12590, @ABHIGYAN-MOHANTA)
- KueueViz: frontend container image now runs as a non-root user (node) to adhere to the principle of least privilege. (#12586, @ABHIGYAN-MOHANTA)
- LeaderWorkerSet: Fixed a bug where a LeaderWorkerSet with a negative or excessively large `spec.replicas` could crash the Kueue controller during reconciliation and MultiKueue workload processing. Kueue now rejects `spec.replicas` values that are negative or greater than 1000000 (#12715, @reruno)
- LocalQueues: Fixed a bug that caused LocalQueue status updates to be rejected
  when quota reservation or usage included more than 16 ResourceFlavors, leaving
  the LocalQueue condition and workload counts stale. LocalQueue status now
  supports up to 64 ResourceFlavors, matching the ClusterQueue limits. (#12082, @AsherWright)
- MultiKueue: Fixed a bug that could leave stale status for Kubernetes Jobs in the manager
  cluster when the worker-cluster Job reached steady state quickly and stopped getting
  updates while the manager-cluster Job was still suspended. (#11867, @andrewseif)
- MultiKueue: Fixed a bug where a remote Workload finishing with reason OutOfSync was mirrored as a terminal finish, leaving the manager Job stranded. Kueue now resets the MultiKueue AdmissionCheck to Retry, retries the Workload, and emits a warning event identifying the worker cluster. (#13047, @Smuger)
- MultiKueue: Fixed a bug where a remote could be marked connected too late, causing early workload events to be handled incorrectly (as if the remote was unreachable). (#12824, @Vaishnav88sk)
- MultiKueue: Fixed a bug where a transient watch reconnect to a worker cluster could evict a running admitted workload. Kueue now measures the worker-lost grace from when the worker cluster's connection first dropped, rather than from the admission check's transition time, and retries immediately only when the reserving worker is reachable but its remote workload is gone. (#12999, @kevin85421)
- MultiKueue: Fixed a bug where admitted Pod workloads could trigger unnecessary Cluster Autoscaler scale-ups
  in the manager cluster. Kueue now preserves the scheduling-gated PodScheduled condition for manager-cluster
  Pods, since they are intended to run only in worker clusters. (#12262, @fg91)
- MultiKueue: Fixed a bug where admitted workloads could remain stuck instead of being evicted and retried after `workerLostTimeout` when reconnecting to a worker cluster failed after its connection configuration changed. (#13188, @kevin85421)
- MultiKueue: Fixed a bug where creating a Job on the manager cluster could
  delete a pre-existing worker-local Job with the same namespace and name.
  MultiKueue now deletes remote Jobs only when they are owned by MultiKueue. (#11877, @mszadkow)
- MultiKueue: Fixed a bug where obsolete remote Workloads could remain on temporarily unavailable worker clusters when the manager Workload lost its reservation or was deleted. Kueue now retries cleanup after worker clusters reconnect. (#11515, @vamsikrishna-siddu)
- MultiKueue: Fixed a data race where reconnecting a remote cluster could swap the remote client while other goroutines were reading it, which could crash-loop the controller manager. (#12612, @apullo777)
- MultiKueue: Fixed custom jobs using external-framework adapters being repeatedly created and deleted on worker clusters when source-cluster metadata was copied to the remote object. (#12643, @apullo777)
- MultiKueue: Fixes an observability bug where Pods scheduled in a worker cluster could still appear unscheduled
  in the manager cluster (as `PodScheduled=False` would be preserved). The `PodScheduled` condition is now
  synchronized from the worker cluster, while preserving `SchedulingGated` for unschedulable Pods to avoid spurious 
  scale-ups. (#13136, @fg91)
- MultiKueue: Stop considering `spec.PreemptionGates` when syncing workloads. The preemption gates on the manager and worker clusters are treated independently - they are not copied from the manager to the workers and differences between them are not considered as out-of-sync. This fixes an issue where creating a MultiKueue workload with a preemption gate would cause an infinite loop of sync and deletions on the worker clusters. (#12587, @kshalot)
- Observability: Fix ClusterQueue Borrowing Limit metric to display infinity if the limit is unset. (#11894, @mszadkow)
- Observability: Fix verbose DRS logs failing to report DRS values due to JSON parsing error when handling fair sharing weight set to 0. (#13154, @kshalot)
- Observability: Fixed LocalQueue gauge metrics not being reported after a LocalQueue starts matching the configured metrics selector. (#12894, @ikchifo)
- Observability: Fixed a misleading `kueue_cluster_queue_lending_limit` metric value for ClusterQueues with unset `lendingLimit`. Kueue now reports `+Inf`, matching the actual unconstrained lending behavior instead of reporting 0. (#12143, @weizhoublue)
- Observability: Fixed a race condition that could leave stale LocalQueue metrics after a label change caused the LocalQueue to stop matching the metrics selector. (#12283, @andrewseif)
- Observability: add a safeguard check truncating the event messages to make sure the events can be successfully recorded in the API server. (#12028, @olekzabl)
- PodGroup integration: Fixed a bug that allowed Workloads corresponding to PodGroups with the `WaitingForReplacementPods=True` condition to be re-admitted immediately. (#12768, @mbobrovskyi)
- PriorityBooster: Fix a bug that events.k8s.io Events operation permission errors. (#11955, @dddwsd)
- ProvisioningRequest: Fix a bug where ProvisioningRequest owned by finished or evicted Workloads are not cleaned up. The CleanupProvisioningRequestsOnEviction feature gate allows cleanup on eviction to be enabled by default. (#12522, @MatteoFari)
- RayJob, RayCluster, RayService, JobSet, MPIJob, and Kubeflow Trainer jobs: Fixed a bug where changing a running job's pod set count, for example adding a worker group to a running RayCluster, could crash the Kueue controller during reconciliation. (#13025, @ivnovakov)
- RayJob, RayCluster, and RayServe integrations: Fixed missing quota accounting for Redis cleanup resources when GCS fault tolerance is enabled. Kueue accounts for the Redis cleanup Job resources for workloads by folding the cleanup Job requests into the Ray head PodSet. (#11260, @nerdeveloper)
- RayJob: Fix the integration controller dropping Kueue admission placement constraints (nodeSelector, tolerations, nodeAffinity) for the submitter pod when submitterPodTemplate is not explicitly set and submissionMode is K8sJobMode. (#12644, @carterpewpew)
- RayService: Fixed a bug where deleting a Kueue-managed RayService with GCS fault tolerance enabled left KubeRay's Redis cleanup Job suspended forever, leaking the RayCluster's Redis metadata namespace. Kueue now defers finalizing the RayService's Workload until the cleanup Job completes. (#12778, @kevin85421)
- ResourceTransformations: Fixed a bug where milli-valued quantities were rounded before
  resource transformation multiplication. For example, multiplying `300m` CPU by `1000`
  now correctly produces `300` instead of `3000`. (#12953, @wafrelka)
- Scheduling: Fixed a bug where a workload could be stuck pending when its node selector referenced a label key declared by a different flavor in the same resource group. (#12449, @carterpewpew)
- Scheduling: Fixed a concurrency bug in BestEffortFIFO ClusterQueues where the
  sticky Workload could change while pending Workloads were being sorted, making
  the comparison non-transitive and potentially corrupting the scheduler queue or
  visibility snapshot ordering. (#12797, @somaz94)
- Scheduling: Fixed resource accounting and validation for Pods using Kubernetes pod-level
  resources (`pod.spec.resources`), including LimitRange defaulting and request/limit
  validation. (#12334, @anuragdalvi)
- Scheduling: Fixed stale scheduling queue entries for pending Workloads that transition
  to `WorkloadOnHold`. (#12929, @anuragdalvi)
- SparkApplication: Fixed a bug where the global spec.nodeSelector could overwrite driver or executor node selectors when they were admitted to different ResourceFlavors. (#12647, @carterpewpew)
- StatefulSet: Fixed a bug where scaling a StatefulSet to zero caused its Workload to be incorrectly requeued for scheduling during the terminating-pod window, competing for quota it should no longer hold. (#12233, @gola)
- TAS & Scheduling: Fixed a bug where Workloads owned by a single Pod could be reassigned after eviction or during TAS node hot swap, even though the existing Pod could not consume the new assignment. The fix applies when the SkipReassignmentForPodOwnedWorkloads feature gate is enabled. The gate is Beta and enabled by default in 0.19+, and Alpha and disabled by default in the 0.17 and 0.18 release branches. (#12980, @yakticus)
- TAS: Added a fix for premature node replacement when a node remains NotReady while the workload's Pods are still running, which could cause the topology assignment to diverge from the actual Pod placement and corrupt per-node capacity accounting. The termination-driven behavior applies when TASReplaceNodeDueToNotReadyOverFixedTime is disabled. The gate is deprecated and disabled by default in 0.19+, and Beta and enabled by default in the 0.17 and 0.18 release branches. (#13043, @yakticus)
- TAS: Fix a bug where TAS ignores excluded or transformed resources in node capacity tracking. (#12006, @wafrelka)
- TAS: Fix a performance bug where repeatedly checking the enablement of the `TASRespectNodeAffinityPreferred` feature gate inside a hot sorting loop could significantly increase the scheduling time (14% by the attached benchmark). (#13144, @j-skiba)
- TAS: Fixed a bug that could cause workloads from ClusterQueues considered later in a scheduling cycle to remain pending for prolonged periods. This could happen because TAS assignments computed independently during nomination were likely to conflict on some topology domains. Kueue now re-evaluates TAS assignments during scheduling when needed. (#12419, @mimowo)
- TAS: Fixed a bug that permanently leaked Topology-Aware Scheduling (TAS) resources if a workload was deleted while its ClusterQueue was temporarily missing a required Topology. (#12733, @Vaishnav88sk)
- TAS: Fixed a bug where a PodSet slice size that did not evenly divide its count could make the topology ungater panic repeatedly, so the workload's Pods stayed stuck gated. The ungater no longer panics and ungates the Pods that fit the topology assignment. (#13223, @ivnovakov)
- TAS: Fixed a bug where a PodSet with `subGroupIndexLabel` set but a missing or zero `subGroupCount` could crash the tas-ungater controller. Kueue now falls back to greedy domain assignment for these pods instead of panicking. (#12807, @reruno)
- TAS: Fixed a performance bug that caused remaining capacity to be repeatedly recalculated and resource maps to be unnecessarily copied during workload evaluation, particularly when evaluating multiple preemption candidate sets in
  large clusters. The fix is guarded by the Beta `TASCachingRemainingResources` feature gate, which is enabled by default. (#13153, @j-skiba)
- TAS: Fixed elastic workload placement to preserve leader pod set assignments and capacity accounting when worker counts stay the same or scale down. (#12436, @RooobinYe)
- TAS: Fixed error handling for TAS topology assignments so Workloads are not considered
  `Fit` when topology assignment fails. Kueue now treats such assignment errors as `NoFit`
  instead of allowing the Workload to reserve quota. (#12055, @yaroslava-serdiuk)
- TAS: domain selection is now deterministic when multiple domains tie on score; ties are broken by the domains' levelValues ordering. (#12052, @mvanhorn)
- TAS: fixed excessive scheduling latency for workloads requiring preemption caused by repeatedly evaluating node selectors, tolerations, and affinity for each preemption simulation. The optimization is controlled by the beta `TASCacheNodeMatchResults` feature gate, enabled by default. (#13110, @j-skiba)
- VisibilityOnDemand: Fixed a bug where a large or negative `limit` query parameter on the pending-workloads endpoints could crash the Kueue controller manager via memory exhaustion or a panic. The `limit` is now capped at 100000. (#13029, @reruno)
- VisibilityOnDemand: Fixed a data race between the Visibility API pending-workloads endpoint and preemption requeuing that could crash the queue manager for BestEffortFIFO ClusterQueues. (#12736, @somaz94)
- VisibilityOnDemand: Fixed forbidden list/watch errors caused by unused
  MutatingAdmissionPolicy informers in the visibility server. (#11854, @kimminw00)
- Workloads: Fixed a validation bug that allowed the `queueName` of a quota-reserved Workload to be changed by first removing and then re-adding the field, bypassing its immutability constraint. Kueue now rejects adding, removing, or changing `queueName` while the Workload's quota remains reserved. (#12594, @kannon92)

### Other (Cleanup or Flake)

- ElasticJob: Added a defensive safeguard to avoid stale pending expectations in no-op pod ungate paths, following 
  the pattern from the TAS topology ungater. (#12652, @weizhoublue)
- Fair Sharing: add a safeguard to prevent a potential infinite loop when `DropQueue` is called for a standalone ClusterQueue without also calling `PopWorkload`. (#13187, @aburan28)
- Kubeflow training v1 will be deprecated in future releases. The default settings will not included training v1. (#12606, @kannon92)
- KueueViz: Fixed WebSocket backend handlers to report errors while fetching dashboard data
  instead of silently ignoring them. (#12310, @yuluo-yx)
- KueueViz: frontend and backend deployments include securityContext defaults (runAsNonRoot, readOnlyRootFilesystem, drop ALL capabilities) and httpGet liveness/readiness probes. (#12513, @ABHIGYAN-MOHANTA)
- MultiKueue: Renamed the admission-check controller's "reserving" terminology to "admitting" to match the underlying `WorkloadAdmitted` condition. The AdmissionCheckState messages and events now read "The workload was admitted on <cluster>", "Admitting remote lost", "Admitting remote no longer exists", and "Admitting remote temporarily unreachable" instead of their "reserving"/"got reservation on" wording. (#13124, @kevin85421)
- MultiKueue: `MultiKueueAllowInsecureKubeconfigs` is now locked to disabled. Remove `--feature-gates=MultiKueueAllowInsecureKubeconfigs=true` and use kubeconfigs with `certificate-authority-data` instead. (#12366, @apullo777)
- Observability: Introduced logging of node replacements by NodeHotSwap. (#13215, @dkaluza)
- Observability: Introduced logging of unhealthy nodes on workload updates. (#13271, @dkaluza)
- Reduced Kueue manager RBAC permissions for several Kueue configuration resources to avoid unnecessary spec write access. (#11791, @MatteoFari)
- TAS: Improved scheduling evaluation performance and reduced memory allocations for Topology-Aware Scheduling (TAS). (#13300, @j-skiba)
- TAS: Reduced the CPU and memory overhead of building the topology snapshot on large clusters by no longer cloning per-node usage maps on every scheduling cycle. (#12672, @akshay-pm)
- TAS: improved workload evaluation performance by optimizing domain-ordering tie-breaks for sibling node domains with equal available capacity. (#13319, @j-skiba)

