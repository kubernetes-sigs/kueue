## v0.18.4

Changes since `v0.18.3`:

## Actions Required Before Upgrading

### (No, really, you MUST read this before you upgrade)

- **Minor releases:** Review the `.0` release notes for each new minor version you cross; see: [`v0.17.0`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.0), [`v0.18.0`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.18.0).
- **Patch releases:** Review the patch release notes leading up to this version, but *only* within this minor release line; see: [`v0.18.1`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.18.1), [`v0.18.2`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.18.2), [`v0.18.3`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.18.3).

## Changes by Kind

### Feature

- Helm: Added enableVisibilityAuthReaderRoleBinding Helm value (default: true) to make the visibility server's auth-reader RoleBinding in kube-system optional. Set to false when deploying under a GitOps project that cannot manage resources in kube-system, and create the RoleBinding out-of-band instead. (#13048, @amy)

### Bug or Regression

- AFS: Fixed a Denial of Service (DoS) vulnerability where deleting a LocalQueue could cause the Kueue scheduler to hang during AdmissionFairSharing calculations. (#13214, @Vaishnav88sk)
- AFS: Fixed a race in Admission Fair Sharing penalty updates where concurrent workload operations could lose penalty changes, causing LocalQueues to receive incorrect priority. (#13285, @MaysaMacedo)
- DRA: Fixed a bug where byte-valued Partitionable Devices (counter-based) resources were displayed as raw byte integers in Workload and ClusterQueue status. 
  These resources are formatted using human-readable BinarySI units, such as Mi and Gi. (#13038, @amarkdotdev)
- DRA: introduce a safeguard for invalid parameter combinations to prevent nil dereference crashes (#12979, @mykysha)
- ElasticJobsViaWorkloadSlices: Fixed a bug that allowed a replacement Workload slice to reference a Workload from another namespace when both used the same ClusterQueue, potentially causing the unrelated Workload to be treated and finished as the replaced slice. Workload slice replacements are now restricted to Workloads in the same namespace. (#13081, @mykysha)
- ElasticJobsViaWorkloadSlices: Fixed a bug that could cause elastic Jobs to
  stall after Pods succeeded or failed, because terminal Pods continued to count
  against the active Workload slice's admitted PodSet count and prevented
  replacement Pods from being ungated. (#13204, @garg02)
- ElasticJobsViaWorkloadSlices: Fixed a bug where an elastic job could permanently fail to start (FailedToStart) due to stale Kueue-owned annotations on the pod template, e.g. after its workload was deleted, or after eviction of a previously scaled-up job. (#13131, @mcochner)
- ElasticJobsViaWorkloadSlices: Fixed a bug where reclaimable Pod accounting after scaling down an elastic Job could reserve quota for Pods that were no longer running. Reserved quota now tracks the remaining running Pods for indexed and non-indexed Jobs. (#13263, @Shreesha001)
- ElasticJobsViaWorkloadSlices: Fixed a bug where scaling down an elastic Job could leave a stale reclaimablePods count, causing Kueue to account for less quota than the Job's remaining Pods were using. (#13060, @Shreesha001)
- KueueViz: Fixed a security issue in kueueviz where WebSocket connections continued streaming cluster data after a bearer token expired or was revoked.
  Connections are now closed within 30 seconds of token invalidation. (#13130, @Vaishnav88sk)
- KueueViz: Navigating to an invalid cohort now displays a graceful error message instead of crashing the UI. (#13246, @Vaishnav88sk)
- KueueViz: Prevent workload detail pages from crashing when Kubernetes Events have missing or invalid timestamps. (#13207, @YQ-Wang)
- MultiKueue: Fixed a bug where a remote Workload finishing with reason OutOfSync was mirrored as a terminal finish, leaving the manager Job stranded. Kueue now resets the MultiKueue AdmissionCheck to Retry, retries the Workload, and emits a warning event identifying the worker cluster. (#13086, @Smuger)
- MultiKueue: Fixed a bug where a transient watch reconnect to a worker cluster could evict a running admitted workload. Kueue now measures the worker-lost grace from when the worker cluster's connection first dropped, rather than from the admission check's transition time, and retries immediately only when the reserving worker is reachable but its remote workload is gone. (#12999, @kevin85421)
- MultiKueue: Fixed a bug where admitted workloads could remain stuck instead of being evicted and retried after `workerLostTimeout` when reconnecting to a worker cluster failed after its connection configuration changed. (#13188, @kevin85421)
- MultiKueue: Fixes an observability bug where Pods scheduled in a worker cluster could still appear unscheduled
  in the manager cluster (as `PodScheduled=False` would be preserved). The `PodScheduled` condition is now
  synchronized from the worker cluster, while preserving `SchedulingGated` for unschedulable Pods to avoid spurious 
  scale-ups. (#13197, @fg91)
- Observability: Fix verbose DRS logs failing to report DRS values due to JSON parsing error when handling fair sharing weight set to 0. (#13157, @kshalot)
- RayJob, RayCluster, RayService, JobSet, MPIJob, and Kubeflow Trainer jobs: Fixed a bug where changing a running job's pod set count, for example adding a worker group to a running RayCluster, could crash the Kueue controller during reconciliation. (#13104, @ivnovakov)
- TAS & Scheduling: Fixed a bug where Workloads owned by a single Pod could be reassigned after eviction or during TAS node hot swap, even though the existing Pod could not consume the new assignment. The fix applies when the SkipReassignmentForPodOwnedWorkloads feature gate is enabled. The gate is Beta and enabled by default in 0.19+, and Alpha and disabled by default in the 0.17 and 0.18 release branches. (#12980, @yakticus)
- TAS: Added a fix for premature node replacement when a node remains NotReady while the workload's Pods are still running, which could cause the topology assignment to diverge from the actual Pod placement and corrupt per-node capacity accounting. The termination-driven behavior applies when TASReplaceNodeDueToNotReadyOverFixedTime is disabled. The gate is deprecated and disabled by default in 0.19+, and Beta and enabled by default in the 0.17 and 0.18 release branches. (#13096, @yakticus)
- TAS: Fix a performance bug where repeatedly checking the enablement of the `TASRespectNodeAffinityPreferred` feature gate inside a hot sorting loop could significantly increase the scheduling time (14% by the attached benchmark). (#13145, @j-skiba)
- TAS: Fixed a bug where a PodSet slice size that did not evenly divide its count could make the topology ungater panic repeatedly, so the workload's Pods stayed stuck gated. The ungater no longer panics and ungates the Pods that fit the topology assignment. (#13268, @ivnovakov)
- TAS: Fixed a bug where a PodSet with `subGroupIndexLabel` set but a missing or zero `subGroupCount` could crash the tas-ungater controller. Kueue now falls back to greedy domain assignment for these pods instead of panicking. (#13065, @reruno)
- TAS: Fixed a performance bug that caused remaining capacity to be repeatedly recalculated and resource maps to be unnecessarily copied during workload evaluation, particularly when evaluating multiple preemption candidate sets in
  large clusters. The fix is guarded by the Beta `TASCachingRemainingResources` feature gate, which is enabled by default. (#13235, @j-skiba)
- TAS: domain selection is now deterministic when multiple domains tie on score; ties are broken by the domains' levelValues ordering. (#13031, @mvanhorn)
- TAS: fixed excessive scheduling latency for workloads requiring preemption caused by repeatedly evaluating node selectors, tolerations, and affinity for each preemption simulation. The optimization is controlled by the beta `TASCacheNodeMatchResults` feature gate, enabled by default. (#13206, @j-skiba)
- VisibilityOnDemand: Fixed a bug where a large or negative `limit` query parameter on the pending-workloads endpoints could crash the Kueue controller manager via memory exhaustion or a panic. The `limit` is now capped at 100000. (#13053, @reruno)

### Other (Cleanup or Flake)

- Observability: Introduced logging of node replacements by NodeHotSwap. (#13215, @dkaluza)
- Observability: Introduced logging of unhealthy nodes on workload updates. (#13273, @dkaluza)
- TAS: Improved scheduling evaluation performance and reduced memory allocations for Topology-Aware Scheduling (TAS). (#13327, @j-skiba)
- TAS: improved workload evaluation performance by optimizing domain-ordering tie-breaks for sibling node domains with equal available capacity. (#13331, @j-skiba)

## v0.18.3

Changes since `v0.18.2`:

## Actions Required Before Upgrading

### (No, really, you MUST read this before you upgrade)

- **Minor releases:** Review the `.0` release notes for each new minor version you cross; see: [`v0.17.0`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.0), [`v0.18.0`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.18.0).
- **Patch releases:** Review the patch release notes leading up to this version, but *only* within this minor release line; see: [`v0.18.1`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.18.1), [`v0.18.2`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.18.2).

- RayJob: Fixed a bug where the Ray job submitter container's resources were not counted against quota when `submissionMode: SidecarMode` was used, causing the head PodSet to be under-counted. The head PodSet now includes the submitter sidecar (KubeRay's default `500m` CPU / `200Mi` memory).
  
  After upgrading, RayJobs using `submissionMode: SidecarMode` reserve the submitter sidecar's resources (default `500m` CPU / `200Mi` memory) on the head. ClusterQueues sized without this headroom may fail to admit such RayJobs; increase the affected ClusterQueue's CPU/memory quota accordingly. (#12726, @kevin85421)
 
## Changes by Kind

### Bug or Regression

- AFS: Fixed ConsumedResources CPU truncating to zero when the sampling interval guard was bypassed by informer cache lag during initialization. (#12691, @sohankunkerkar)
- AFS: Fixed a race where a sampling tick running concurrently with workload settlement could persist a skewed ConsumedResources value in LocalQueue fair-sharing status. (#12940, @apullo777)
- AFS: Fixed consumed-resources cache initialization and warm-start recovery so LocalQueue usage is not over-counted during cache seeding, and persisted historical usage is preserved after manager restarts when workload settlement runs before LocalQueue reconciliation. (#12891, @apullo777)
- CLI: Fix --dry-run flag being silently ignored in kueuectl resume/stop localqueue and clusterqueue subcommands. (#12624, @carterpewpew)
- DRA: Fix an integer overflow in device-count quota accounting where a ResourceClaimTemplate with very large device counts could be admitted over quota and leave a negative used-quota in the ClusterQueue status. (#12901, @thc1006)
- DRA: Fixed incorrect quota charging for invalid driver-published device counters by clamping them to the non-negative 
  int64 range before computing quota charges. (#12947, @thc1006)
- DRA: fixed a potential int64 overflow in the counter-based device quota charge computation that could under-count quota when a driver publishes very large counter values. (#12928, @thc1006)
- ElasticJobsViaWorkloadSlices: Fix workload slice misordering that could finish a correctly-admitted elastic workload slice when 3+ slices were created within the same second. (#12964, @mimowo)
- ElasticJobsViaWorkloadSlices: Fixed a bug where scaling a Job below its accumulated succeeded count could permanently wedge the Workload reconciler and leak quota. (#12959, @Shreesha001)
- ElasticJobsViaWorkloadSlices: Fixed a bug where worker pods of an elastic job could be ungated after scale up, 
  past the ClusterQueue quota; ungating is now capped to the replicas granted quota across the workload-slice chain. (#12045, @mcochner)
- Helm: Fix helm chart failing to install with a manager CrashLoopBackoff when cert-manager integration is enabled. (#12878, @meln5674)
- Kueue-populator: Fixed a bug where an error creating a LocalQueue was logged but not returned from Reconcile, 
  preventing controller-runtime from retrying. LocalQueue creation failures are now aggregated and returned so the request is requeued. (#12905, @NasitSony)
- KueueViz: Fixed a Cross-Site WebSocket Hijacking (CSWSH) vulnerability in the KueueViz Backend by strictly validating WebSocket Origin headers to prevent unauthorized cross-origin data extraction. (#12875, @Vaishnav88sk)
- KueueViz: Fixed a Denial of Service vulnerability where an oversized WebSocket frame could exhaust backend memory (OOM). Connections now enforce an 8 KiB read limit. (#12704, @ABHIGYAN-MOHANTA)
- KueueViz: Fixed dashboard crash caused by missing optional chaining on flavor.resources (#12668, @ABHIGYAN-MOHANTA)
- KueueViz: Improved workloads dashboard performance by avoiding repeated Pod list operations per Workload (#12857, @cryo-zd)
- KueueViz: backend includes HTTP server timeouts (ReadHeaderTimeout, ReadTimeout, WriteTimeout, IdleTimeout) to prevent connection resource exhaustion. (#12866, @ABHIGYAN-MOHANTA)
- KueueViz: frontend container image now runs as a non-root user (node) to adhere to the principle of least privilege. (#12702, @ABHIGYAN-MOHANTA)
- LeaderWorkerSet: Fixed a bug where a LeaderWorkerSet with a negative or excessively large `spec.replicas` could crash the Kueue controller during reconciliation and MultiKueue workload processing. Kueue now rejects `spec.replicas` values that are negative or greater than 1000000 (#12755, @reruno)
- MultiKueue: Fixed a bug where obsolete remote Workloads could remain on temporarily unavailable worker clusters when the manager Workload lost its reservation or was deleted. Kueue now retries cleanup after worker clusters reconnect. (#11515, @vamsikrishna-siddu)
- MultiKueue: Fixed a data race where reconnecting a remote cluster could swap the remote client while other goroutines were reading it, which could crash-loop the controller manager. (#12612, @apullo777)
- MultiKueue: Fixed custom jobs using external-framework adapters being repeatedly created and deleted on worker clusters when source-cluster metadata was copied to the remote object. (#12677, @apullo777)
- Observability: Fixed LocalQueue gauge metrics not being reported after a LocalQueue starts matching the configured metrics selector. (#12903, @ikchifo)
- PodGroup integration: Fixed a bug that allowed Workloads corresponding to PodGroups with the `WaitingForReplacementPods=True` condition to be re-admitted immediately. (#12872, @mbobrovskyi)
- ProvisioningRequest: Fix a bug where ProvisioningRequest owned by finished or evicted Workloads are not cleaned up. The CleanupProvisioningRequestsOnEviction feature gate allows cleanup on eviction to be enabled by default. (#12632, @MatteoFari)
- RayJob: Fix the integration controller dropping Kueue admission placement constraints (nodeSelector, tolerations, nodeAffinity) for the submitter pod when submitterPodTemplate is not explicitly set and submissionMode is K8sJobMode. (#12696, @carterpewpew)
- RayService: Fixed a bug where deleting a Kueue-managed RayService with GCS fault tolerance enabled left KubeRay's Redis cleanup Job suspended forever, leaking the RayCluster's Redis metadata namespace. Kueue now defers finalizing the RayService's Workload until the cleanup Job completes. (#12778, @kevin85421)
- ResourceTransformations: Fixed a bug where milli-valued quantities were rounded before
  resource transformation multiplication. For example, multiplying `300m` CPU by `1000`
  now correctly produces `300` instead of `3000`. (#12961, @mimowo)
- Scheduling: Fixed resource accounting and validation for Pods using Kubernetes pod-level
  resources (`pod.spec.resources`), including LimitRange defaulting and request/limit
  validation. (#12731, @anuragdalvi)
- Scheduling: Fixed stale scheduling queue entries for pending Workloads that transition
  to `WorkloadOnHold`. (#12942, @anuragdalvi)
- SparkApplication: Fixed a bug where the global spec.nodeSelector could overwrite driver or executor node selectors when they were admitted to different ResourceFlavors. (#12687, @carterpewpew)
- StatefulSet: Fixed a bug where scaling a StatefulSet to zero caused its Workload to be incorrectly requeued for scheduling during the terminating-pod window, competing for quota it should no longer hold. (#12650, @gola)
- TAS: Fixed a bug that permanently leaked Topology-Aware Scheduling (TAS) resources if a workload was deleted while its ClusterQueue was temporarily missing a required Topology. (#12751, @Vaishnav88sk)
- VisibilityOnDemand: Fixed a data race between the Visibility API pending-workloads endpoint and preemption requeuing that could crash the queue manager for BestEffortFIFO ClusterQueues. (#12754, @somaz94)

### Other (Cleanup or Flake)

- TAS: Reduced the CPU and memory overhead of building the topology snapshot on large clusters by no longer cloning per-node usage maps on every scheduling cycle. (#12705, @akshay-pm)

## v0.18.2

Changes since `v0.18.1`:

## Actions Required Before Upgrading

### (No, really, you MUST read this before you upgrade)

- **Minor releases:** Review the `.0` release notes for each new minor version you cross; see: [`v0.17.0`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.0), [`v0.18.0`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.18.0).
- **Patch releases:** Review the patch release notes leading up to this version, but *only* within this minor release line; see: [`v0.18.1`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.18.1).

- KueuePopulator Helm: `helm uninstall` removes the ClusterQueue, ResourceFlavor, Topology, ConfigMap, and RBAC created by the chart, which previously leaked after uninstall.
  
  If you installed a previous version of the kueue-populator chart, its ConfigMap and RBAC (`*-kueue-hook-*` ServiceAccount/ClusterRole/ClusterRoleBinding and the `*-kueue-resources` ConfigMap) were created as Helm hooks and are not adopted by the new release. Delete them manually before upgrading to avoid `helm upgrade`/`install` ownership conflicts. (#12432, @kevin85421)
 
## Changes by Kind

### Bug or Regression

- DRA: Fixed a bug where workloads with device constraints (matchAttribute) or device config were incorrectly rejected as unsupported instead of being admitted for quota. (#12471, @sohankunkerkar)
- Importer: Fixed LocalQueue namespace isolation to prevent information leakage between
  namespaces when multiple LocalQueues with the same name exist in different namespaces. (#12349, @Singularity23x0)
- KueueViz: Fixed WebSocket backend handlers to report errors while fetching dashboard data
  instead of silently ignoring them. (#12346, @yuluo-yx)
- MultiKueue: Creating a Job on the manager cluster deletes any pre-existing remote worker Job that happens to share the same NamespacedName. (#12380, @mszadkow)
- MultiKueue: Fixed a bug that could leave stale status for Kubernetes Jobs in the manager
  cluster when the worker-cluster Job reached steady state quickly and stopped getting
  updates while the manager-cluster Job was still suspended. (#12297, @andrewseif)
- MultiKueue: Fixed a bug where admitted Pod workloads could trigger unnecessary Cluster Autoscaler scale-ups
  in the manager cluster. Kueue now preserves the scheduling-gated PodScheduled condition for manager-cluster
  Pods, since they are intended to run only in worker clusters. (#12272, @fg91)
- Observability: Fixed a race condition that could leave stale LocalQueue metrics after a label change caused the LocalQueue to stop matching the metrics selector. (#12291, @andrewseif)
- RayJob, RayCluster, and RayServe integrations: Fixed missing quota accounting for Redis cleanup resources when GCS fault tolerance is enabled. Kueue accounts for the Redis cleanup Job resources for workloads by folding the cleanup Job requests into the Ray head PodSet. (#12395, @nerdeveloper)
- Scheduling: Fixed a bug where a workload could be stuck pending when its node selector referenced a label key declared by a different flavor in the same resource group. (#12449, @carterpewpew)
- TAS: Fixed a bug that could cause workloads from ClusterQueues considered later in a scheduling cycle to remain pending for prolonged periods. This could happen because TAS assignments computed independently during nomination were likely to conflict on some topology domains. Kueue now re-evaluates TAS assignments during scheduling when needed. (#12521, @mimowo)

## v0.18.1

Changes since `v0.18.0`:

## Actions Required Before Upgrading

### (No, really, you MUST read this before you upgrade)

- **Minor releases:** Review the `.0` release notes for each new minor version you cross; see: [`v0.17.0`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.0), [`v0.18.0`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.18.0).

## Changes by Kind

### Bug or Regression

- DRA: Fixed a bug where pending Workloads using DRA extended resources were not requeued when their `DeviceClass` was deleted or its `extendedResourceName` changed. Kueue now re-evaluates affected Workloads so they do not remain in stale admission state. (#12093, @sohankunkerkar)
- DRA: Fixed configuration validation to reject `deviceClassMappings[].sources` when the `KueueDRAIntegrationPartitionableDevices` feature gate is disabled, preventing unsupported partitionable-device configuration from being accepted. (#12152, @sohankunkerkar)
- DRA: Fixed hot reconcile loops for inadmissible Workloads with deterministic DRA resolution
  failures. Kueue now avoids requeueing permanent DRA spec or configuration errors while still
  retrying transient failures with backoff. (#12057, @thc1006)
- ElasticJobsViaWorkloadSlices: Fix the bug that regular (non-elastic) workloads with the required/preferred topology
  were rejected when the feature ElasticJobsViaWorkloadSlicesWithTAS is enabled. (#12044, @yaroslava-serdiuk)
- Fixed LocalQueue status updates being rejected ("status.flavorsReservation: Too many: ... must have at most 16 items") when the referenced ClusterQueue has more than 16 flavors, by raising the LocalQueue status flavor limits to 64 to match the ClusterQueue limits. (#12087, @AsherWright)
- Kueue-populator: Fixed `events.k8s.io` RBAC permissions for event recording. (#12031, @weizhoublue)
- KueueViz: Fixed a bug where the dashboard briefly displayed zero counts for all metrics on page load before the WebSocket connection finished loading. (#12039, @YadavAkhileshh)
- KueueViz: Fixed a layout-bleed bug where switching directly between detail pages briefly rendered stale queue data from the previously visited resource. (#12019, @YadavAkhileshh)
- Observability: Fix ClusterQueue Borrowing Limit metric to display infinity if the limit is unset. (#12105, @mszadkow)
- Observability: Fixed a misleading `kueue_cluster_queue_lending_limit` metric value for ClusterQueues with unset `lendingLimit`. Kueue now reports `+Inf`, matching the actual unconstrained lending behavior instead of reporting 0. (#12168, @weizhoublue)
- Observability: add a safeguard check truncating the event messages to make sure the events can be successfully recorded in the API server. (#12048, @olekzabl)
- PriorityBooster: Fix a bug that events.k8s.io Events operation permission errors. (#11969, @dddwsd)
- TAS: Fix a bug where TAS ignores excluded or transformed resources in node capacity tracking. (#12034, @wafrelka)
- TAS: Fixed error handling for TAS topology assignments so Workloads are not considered
  `Fit` when topology assignment fails. Kueue now treats such assignment errors as `NoFit`
  instead of allowing the Workload to reserve quota. (#12172, @yaroslava-serdiuk)
- VisibilityOnDemand: Fixed forbidden list/watch errors caused by unused
  MutatingAdmissionPolicy informers in the visibility server. (#11874, @kimminw00)

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
- Increased the maximum number of PodSets per Workload from 8 to 10. As a result, RayCluster, RayJob, and RayService integrations now allow up to 9 worker groups, since one PodSet is reserved for the Ray head group. (#11388, @yuluo-yx)
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

