## v0.17.8

Changes since `v0.17.7`:

## Actions Required Before Upgrading

### (No, really, you MUST read this before you upgrade)

- **Minor releases:** Review the `.0` release notes for each new minor version you cross; see: [`v0.16.0`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.16.0), [`v0.17.0`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.0).
- **Patch releases:** Review the patch release notes leading up to this version, but *only* within this minor release line; see: [`v0.17.1`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.1), [`v0.17.2`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.2), [`v0.17.3`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.3), [`v0.17.4`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.4), [`v0.17.5`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.5), [`v0.17.6`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.6), [`v0.17.7`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.7).

## Changes by Kind

### Feature

- Helm: Added enableVisibilityAuthReaderRoleBinding Helm value (default: true) to make the visibility server's auth-reader RoleBinding in kube-system optional. Set to false when deploying under a GitOps project that cannot manage resources in kube-system, and create the RoleBinding out-of-band instead. (#13049, @amy)

### Bug or Regression

- AFS: Fixed a Denial of Service (DoS) vulnerability where deleting a LocalQueue could cause the Kueue scheduler to hang during AdmissionFairSharing calculations. (#13264, @Vaishnav88sk)
- AFS: Fixed a race in Admission Fair Sharing penalty updates where concurrent workload operations could lose penalty changes, causing LocalQueues to receive incorrect priority. (#13287, @MaysaMacedo)
- ElasticJobsViaWorkloadSlices: Fixed a bug that allowed a replacement Workload slice to reference a Workload from another namespace when both used the same ClusterQueue, potentially causing the unrelated Workload to be treated and finished as the replaced slice. Workload slice replacements are now restricted to Workloads in the same namespace. (#13082, @mykysha)
- ElasticJobsViaWorkloadSlices: Fixed a bug that could cause elastic Jobs to
  stall after Pods succeeded or failed, because terminal Pods continued to count
  against the active Workload slice's admitted PodSet count and prevented
  replacement Pods from being ungated. (#13203, @garg02)
- ElasticJobsViaWorkloadSlices: Fixed a bug where an elastic job could permanently fail to start (FailedToStart) due to stale Kueue-owned annotations on the pod template, e.g. after its workload was deleted, or after eviction of a previously scaled-up job. (#13132, @mcochner)
- ElasticJobsViaWorkloadSlices: Fixed a bug where reclaimable Pod accounting after scaling down an elastic Job could reserve quota for Pods that were no longer running. Reserved quota now tracks the remaining running Pods for indexed and non-indexed Jobs. (#13178, @Shreesha001)
- ElasticJobsViaWorkloadSlices: Fixed a bug where scaling down an elastic Job could leave a stale reclaimablePods count, causing Kueue to account for less quota than the Job's remaining Pods were using. (#13044, @Shreesha001)
- KueueViz: Fixed a security issue in kueueviz where WebSocket connections continued streaming cluster data after a bearer token expired or was revoked.
  Connections are now closed within 30 seconds of token invalidation. (#13164, @Vaishnav88sk)
- KueueViz: Navigating to an invalid cohort now displays a graceful error message instead of crashing the UI. (#13247, @Vaishnav88sk)
- KueueViz: Prevent workload detail pages from crashing when Kubernetes Events have missing or invalid timestamps. (#13208, @YQ-Wang)
- MultiKueue: Fixed a bug where a remote Workload finishing with reason OutOfSync was mirrored as a terminal finish, leaving the manager Job stranded. Kueue now resets the MultiKueue AdmissionCheck to Retry, retries the Workload, and emits a warning event identifying the worker cluster. (#13087, @Smuger)
- MultiKueue: Fixed a bug where a transient watch reconnect to a worker cluster could evict a running admitted workload. Kueue now measures the worker-lost grace from when the worker cluster's connection first dropped, rather than from the admission check's transition time, and retries immediately only when the reserving worker is reachable but its remote workload is gone. (#12999, @kevin85421)
- MultiKueue: Fixed a bug where admitted workloads could remain stuck instead of being evicted and retried after `workerLostTimeout` when reconnecting to a worker cluster failed after its connection configuration changed. (#13188, @kevin85421)
- MultiKueue: Fixes an observability bug where Pods scheduled in a worker cluster could still appear unscheduled
  in the manager cluster (as `PodScheduled=False` would be preserved). The `PodScheduled` condition is now
  synchronized from the worker cluster, while preserving `SchedulingGated` for unschedulable Pods to avoid spurious 
  scale-ups. (#13198, @fg91)
- Observability: Fix verbose DRS logs failing to report DRS values due to JSON parsing error when handling fair sharing weight set to 0. (#13156, @kshalot)
- RayJob, RayCluster, RayService, JobSet, MPIJob, and Kubeflow Trainer jobs: Fixed a bug where changing a running job's pod set count, for example adding a worker group to a running RayCluster, could crash the Kueue controller during reconciliation. (#13111, @ivnovakov)
- TAS & Scheduling: Fixed a bug where Workloads owned by a single Pod could be reassigned after eviction or during TAS node hot swap, even though the existing Pod could not consume the new assignment. The fix applies when the SkipReassignmentForPodOwnedWorkloads feature gate is enabled. The gate is Beta and enabled by default in 0.19+, and Alpha and disabled by default in the 0.17 and 0.18 release branches. (#12980, @yakticus)
- TAS: Added a fix for premature node replacement when a node remains NotReady while the workload's Pods are still running, which could cause the topology assignment to diverge from the actual Pod placement and corrupt per-node capacity accounting. The termination-driven behavior applies when TASReplaceNodeDueToNotReadyOverFixedTime is disabled. The gate is deprecated and disabled by default in 0.19+, and Beta and enabled by default in the 0.17 and 0.18 release branches. (#13097, @yakticus)
- TAS: Fixed a bug where a PodSet slice size that did not evenly divide its count could make the topology ungater panic repeatedly, so the workload's Pods stayed stuck gated. The ungater no longer panics and ungates the Pods that fit the topology assignment. (#13266, @ivnovakov)
- TAS: Fixed a bug where a PodSet with `subGroupIndexLabel` set but a missing or zero `subGroupCount` could crash the tas-ungater controller. Kueue now falls back to greedy domain assignment for these pods instead of panicking. (#13072, @reruno)
- TAS: Fixed a performance bug that caused remaining capacity to be repeatedly recalculated and resource maps to be unnecessarily copied during workload evaluation, particularly when evaluating multiple preemption candidate sets in
  large clusters. The fix is guarded by the Beta `TASCachingRemainingResources` feature gate, which is enabled by default. (#13234, @j-skiba)
- TAS: domain selection is now deterministic when multiple domains tie on score; ties are broken by the domains' levelValues ordering. (#12052, @mvanhorn)
- TAS: fixed excessive scheduling latency for workloads requiring preemption caused by repeatedly evaluating node selectors, tolerations, and affinity for each preemption simulation. The optimization is controlled by the beta `TASCacheNodeMatchResults` feature gate, enabled by default. (#13205, @j-skiba)
- VisibilityOnDemand: Fixed a bug where a large or negative `limit` query parameter on the pending-workloads endpoints could crash the Kueue controller manager via memory exhaustion or a panic. The `limit` is now capped at 100000. (#13052, @reruno)

### Other (Cleanup or Flake)

- Observability: Introduced logging of node replacements by NodeHotSwap. (#13215, @dkaluza)
- Observability: Introduced logging of unhealthy nodes on workload updates. (#13272, @dkaluza)
- TAS: Improved scheduling evaluation performance and reduced memory allocations for Topology-Aware Scheduling (TAS). (#13326, @j-skiba)
- TAS: improved workload evaluation performance by optimizing domain-ordering tie-breaks for sibling node domains with equal available capacity. (#13332, @j-skiba)

## v0.17.7

Changes since `v0.17.6`:

## Actions Required Before Upgrading

### (No, really, you MUST read this before you upgrade)

- **Minor releases:** Review the `.0` release notes for each new minor version you cross; see: [`v0.16.0`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.16.0), [`v0.17.0`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.0).
- **Patch releases:** Review the patch release notes leading up to this version, but *only* within this minor release line; see: [`v0.17.1`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.1), [`v0.17.2`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.2), [`v0.17.3`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.3), [`v0.17.4`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.4), [`v0.17.5`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.5), [`v0.17.6`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.6).

- RayJob: Fixed a bug where the Ray job submitter container's resources were not counted against quota when `submissionMode: SidecarMode` was used, causing the head PodSet to be under-counted. The head PodSet now includes the submitter sidecar (KubeRay's default `500m` CPU / `200Mi` memory).
  
  After upgrading, RayJobs using `submissionMode: SidecarMode` reserve the submitter sidecar's resources (default `500m` CPU / `200Mi` memory) on the head. ClusterQueues sized without this headroom may fail to admit such RayJobs; increase the affected ClusterQueue's CPU/memory quota accordingly. (#12725, @kevin85421)
 
## Changes by Kind

### Bug or Regression

- AFS: Fixed ConsumedResources CPU truncating to zero when the sampling interval guard was bypassed by informer cache lag during initialization. (#12694, @sohankunkerkar)
- AFS: Fixed a race where a sampling tick running concurrently with workload settlement could persist a skewed ConsumedResources value in LocalQueue fair-sharing status. (#12946, @mimowo)
- AFS: Fixed consumed-resources cache initialization and warm-start recovery so LocalQueue usage is not over-counted during cache seeding, and persisted historical usage is preserved after manager restarts when workload settlement runs before LocalQueue reconciliation. (#12891, @apullo777)
- CLI: Fix --dry-run flag being silently ignored in kueuectl resume/stop localqueue and clusterqueue subcommands. (#12627, @carterpewpew)
- ElasticJobsViaWorkloadSlices: Fix workload slice misordering that could finish a correctly-admitted elastic workload slice when 3+ slices were created within the same second. (#12965, @mimowo)
- ElasticJobsViaWorkloadSlices: Fixed a bug where scaling a Job below its accumulated succeeded count could permanently wedge the Workload reconciler and leak quota. (#12963, @mimowo)
- ElasticJobsViaWorkloadSlices: Fixed a bug where worker pods of an elastic job could be ungated after scale up, 
  past the ClusterQueue quota; ungating is now capped to the replicas granted quota across the workload-slice chain. (#12045, @mcochner)
- Helm: Fix helm chart failing to install with a manager CrashLoopBackoff when cert-manager integration is enabled. (#12877, @meln5674)
- Kueue-populator: Fixed a bug where an error creating a LocalQueue was logged but not returned from Reconcile, 
  preventing controller-runtime from retrying. LocalQueue creation failures are now aggregated and returned so the request is requeued. (#12930, @NasitSony)
- KueueViz: Fixed a Cross-Site WebSocket Hijacking (CSWSH) vulnerability in the KueueViz Backend by strictly validating WebSocket Origin headers to prevent unauthorized cross-origin data extraction. (#12876, @Vaishnav88sk)
- KueueViz: Fixed a Denial of Service vulnerability where an oversized WebSocket frame could exhaust backend memory (OOM). Connections now enforce an 8 KiB read limit. (#12703, @ABHIGYAN-MOHANTA)
- KueueViz: Fixed dashboard crash caused by missing optional chaining on flavor.resources (#12667, @ABHIGYAN-MOHANTA)
- KueueViz: Improved workloads dashboard performance by avoiding repeated Pod list operations per Workload (#12858, @cryo-zd)
- KueueViz: backend includes HTTP server timeouts (ReadHeaderTimeout, ReadTimeout, WriteTimeout, IdleTimeout) to prevent connection resource exhaustion. (#12867, @ABHIGYAN-MOHANTA)
- KueueViz: frontend container image now runs as a non-root user (node) to adhere to the principle of least privilege. (#12586, @ABHIGYAN-MOHANTA)
- LeaderWorkerSet: Fixed a bug where a LeaderWorkerSet with a negative or excessively large `spec.replicas` could crash the Kueue controller during reconciliation and MultiKueue workload processing. Kueue now rejects `spec.replicas` values that are negative or greater than 1000000 (#12756, @reruno)
- MultiKueue: Fixed a bug where obsolete remote Workloads could remain on temporarily unavailable worker clusters when the manager Workload lost its reservation or was deleted. Kueue now retries cleanup after worker clusters reconnect. (#11515, @vamsikrishna-siddu)
- MultiKueue: Fixed a data race where reconnecting a remote cluster could swap the remote client while other goroutines were reading it, which could crash-loop the controller manager. (#12612, @apullo777)
- MultiKueue: Fixed custom jobs using external-framework adapters being repeatedly created and deleted on worker clusters when source-cluster metadata was copied to the remote object. (#12643, @apullo777)
- Observability: Fixed LocalQueue gauge metrics not being reported after a LocalQueue starts matching the configured metrics selector. (#12912, @ikchifo)
- PodGroup integration: Fixed a bug that allowed Workloads corresponding to PodGroups with the `WaitingForReplacementPods=True` condition to be re-admitted immediately. (#12873, @mbobrovskyi)
- ProvisioningRequest: Fix a bug where ProvisioningRequest owned by finished or evicted Workloads are not cleaned up. The CleanupProvisioningRequestsOnEviction feature gate allows cleanup on eviction to be enabled by default. (#12654, @MatteoFari)
- RayJob: Fix the integration controller dropping Kueue admission placement constraints (nodeSelector, tolerations, nodeAffinity) for the submitter pod when submitterPodTemplate is not explicitly set and submissionMode is K8sJobMode. (#12695, @carterpewpew)
- RayService: Fixed a bug where deleting a Kueue-managed RayService with GCS fault tolerance enabled left KubeRay's Redis cleanup Job suspended forever, leaking the RayCluster's Redis metadata namespace. Kueue now defers finalizing the RayService's Workload until the cleanup Job completes. (#12778, @kevin85421)
- ResourceTransformations: Fixed a bug where milli-valued quantities were rounded before
  resource transformation multiplication. For example, multiplying `300m` CPU by `1000`
  now correctly produces `300` instead of `3000`. (#12962, @mimowo)
- Scheduling: Fixed resource accounting and validation for Pods using Kubernetes pod-level
  resources (`pod.spec.resources`), including LimitRange defaulting and request/limit
  validation. (#12780, @anuragdalvi)
- Scheduling: Fixed stale scheduling queue entries for pending Workloads that transition
  to `WorkloadOnHold`. (#12948, @mimowo)
- SparkApplication: Fixed a bug where the global spec.nodeSelector could overwrite driver or executor node selectors when they were admitted to different ResourceFlavors. (#12688, @carterpewpew)
- StatefulSet: Fixed a bug where scaling a StatefulSet to zero caused its Workload to be incorrectly requeued for scheduling during the terminating-pod window, competing for quota it should no longer hold. (#12657, @gola)
- TAS: Fixed a bug that permanently leaked Topology-Aware Scheduling (TAS) resources if a workload was deleted while its ClusterQueue was temporarily missing a required Topology. (#12752, @Vaishnav88sk)
- VisibilityOnDemand: Fixed a data race between the Visibility API pending-workloads endpoint and preemption requeuing that could crash the queue manager for BestEffortFIFO ClusterQueues. (#12736, @somaz94)

### Other (Cleanup or Flake)

- TAS: Reduced the CPU and memory overhead of building the topology snapshot on large clusters by no longer cloning per-node usage maps on every scheduling cycle. (#12706, @akshay-pm)

## v0.17.6

Changes since `v0.17.5`:

## Actions Required Before Upgrading

### (No, really, you MUST read this before you upgrade)

- **Minor releases:** Review the `.0` release notes for each new minor version you cross; see: [`v0.16.0`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.16.0), [`v0.17.0`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.0).
- **Patch releases:** Review the patch release notes leading up to this version, but *only* within this minor release line; see: [`v0.17.1`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.1), [`v0.17.2`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.2), [`v0.17.3`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.3), [`v0.17.4`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.4), [`v0.17.5`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.5).

- KueuePopulator Helm: `helm uninstall` removes the ClusterQueue, ResourceFlavor, Topology, ConfigMap, and RBAC created by the chart, which previously leaked after uninstall.
  
  If you installed a previous version of the kueue-populator chart, its ConfigMap and RBAC (`*-kueue-hook-*` ServiceAccount/ClusterRole/ClusterRoleBinding and the `*-kueue-resources` ConfigMap) were created as Helm hooks and are not adopted by the new release. Delete them manually before upgrading to avoid `helm upgrade`/`install` ownership conflicts. (#12450, @kevin85421)
 
## Changes by Kind

### Bug or Regression

- Importer: Fixed LocalQueue namespace isolation to prevent information leakage between
  namespaces when multiple LocalQueues with the same name exist in different namespaces. (#12348, @Singularity23x0)
- KueueViz: Fixed WebSocket backend handlers to report errors while fetching dashboard data
  instead of silently ignoring them. (#12347, @yuluo-yx)
- MultiKueue: Creating a Job on the manager cluster deletes any pre-existing remote worker Job that happens to share the same NamespacedName. (#12383, @mszadkow)
- MultiKueue: Fixed a bug where admitted Pod workloads could trigger unnecessary Cluster Autoscaler scale-ups
  in the manager cluster. Kueue now preserves the scheduling-gated PodScheduled condition for manager-cluster
  Pods, since they are intended to run only in worker clusters. (#12273, @fg91)
- RayJob, RayCluster, and RayServe integrations: Fixed missing quota accounting for Redis cleanup resources when GCS fault tolerance is enabled. Kueue accounts for the Redis cleanup Job resources for workloads by folding the cleanup Job requests into the Ray head PodSet. (#11260, @nerdeveloper)
- Scheduling: Fixed a bug where a workload could be stuck pending when its node selector referenced a label key declared by a different flavor in the same resource group. (#12449, @carterpewpew)
- TAS: Fixed a bug that could cause workloads from ClusterQueues considered later in a scheduling cycle to remain pending for prolonged periods. This could happen because TAS assignments computed independently during nomination were likely to conflict on some topology domains. Kueue now re-evaluates TAS assignments during scheduling when needed. (#12523, @mimowo)

## v0.17.5

Changes since `v0.17.4`:

## Actions Required Before Upgrading

### (No, really, you MUST read this before you upgrade)

- **Minor releases:** Review the `.0` release notes for each new minor version you cross; see: [`v0.16.0`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.16.0), [`v0.17.0`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.0).
- **Patch releases:** Review the patch release notes leading up to this version, but *only* within this minor release line; see: [`v0.17.1`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.1), [`v0.17.2`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.2), [`v0.17.3`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.3), [`v0.17.4`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.4).

## Changes by Kind

### Bug or Regression

- DRA: Fixed a bug where the kueue-controller-manager startup fails when DRA v1 APIs are not available (#11813, @tenzen-y)
- DRA: Fixed hot reconcile loops for inadmissible Workloads with deterministic DRA resolution
  failures. Kueue now avoids requeueing permanent DRA spec or configuration errors while still
  retrying transient failures with backoff. (#12094, @thc1006)
- ElasticJobsViaWorkloadSlices: Fix the bug that regular (non-elastic) workloads with the required/preferred topology
  were rejected when the feature ElasticJobsViaWorkloadSlicesWithTAS is enabled. (#12043, @yaroslava-serdiuk)
- Fixed LocalQueue status updates being rejected ("status.flavorsReservation: Too many: ... must have at most 16 items") when the referenced ClusterQueue has more than 16 flavors, by raising the LocalQueue status flavor limits to 64 to match the ClusterQueue limits. (#12089, @AsherWright)
- Kueue-populator: Fixed `events.k8s.io` RBAC permissions for event recording. (#12032, @weizhoublue)
- KueueViz: Fixed a bug where the dashboard briefly displayed zero counts for all metrics on page load before the WebSocket connection finished loading. (#12040, @YadavAkhileshh)
- KueueViz: Fixed a layout-bleed bug where switching directly between detail pages briefly rendered stale queue data from the previously visited resource. (#12018, @YadavAkhileshh)
- Observability: Fix ClusterQueue Borrowing Limit metric to display infinity if the limit is unset. (#12106, @mszadkow)
- Observability: Fixed a misleading `kueue_cluster_queue_lending_limit` metric value for ClusterQueues with unset `lendingLimit`. Kueue now reports `+Inf`, matching the actual unconstrained lending behavior instead of reporting 0. (#12171, @weizhoublue)
- Observability: add a safeguard check truncating the event messages to make sure the events can be successfully recorded in the API server. (#12090, @olekzabl)
- TAS: Fix a bug where TAS ignores excluded or transformed resources in node capacity tracking. (#12035, @wafrelka)
- TAS: Fixed error handling for TAS topology assignments so Workloads are not considered
  `Fit` when topology assignment fails. Kueue now treats such assignment errors as `NoFit`
  instead of allowing the Workload to reserve quota. (#12188, @mimowo)
- VisibilityOnDemand: Fixed forbidden list/watch errors caused by unused
  MutatingAdmissionPolicy informers in the visibility server. (#11875, @kimminw00)

## v0.17.4

Changes since `v0.17.3`:

## Actions Required Before Upgrading

### (No, really, you MUST read this before you upgrade)

- **Minor releases:** Review the `.0` release notes for each new minor version you cross; see: [`v0.16.0`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.16.0), [`v0.17.0`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.0).
- **Patch releases:** Review the patch release notes leading up to this version, but *only* within this minor release line; see: [`v0.17.1`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.1), [`v0.17.2`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.2), [`v0.17.3`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.3).

## Changes by Kind

### Bug or Regression

- ElasticJobsViaWorkloadSlices: Fix a bug where the workload-slice-name annotation was incorrectly set on all workloads when the ElasticJobsViaWorkloadSlices feature gate was enabled, instead of only on elastic workloads. (#11623, @sohankunkerkar)
- ElasticJobsViaWorkloadSlices: Fix quota leak during elastic workload scale-up where old slice was finished before replacement slice was admitted. (#11557, @sohankunkerkar)
- ElasticJobsViaWorkloadSlices: Fixed a bug where rapid elastic Job scaling could leave duplicate replacement Workload slices admitted indefinitely, causing quota to remain reserved. (#11553, @sohankunkerkar)
- FeatureGates: Fix a bug that TAS-enhanced features can be enabled even if the dependent TopologyAwareScheduling or TASFailedNodeReplacement feature gates are disabled (#11800, @tenzen-y)
- FeatureGates: Fixed a bug that user-specified feature gate parameters are not verified. (#11288, @MaysaMacedo)
- Fix a bug where finished Workloads could remain stuck after object retention deletion if they still had Kueue's resource-in-use finalizer. (#11308, @ShaanveerS)
- Fix waitForPodsReady timeout not triggering for pod groups with fast-admission when group members arrive after the first pod is Ready. (#11686, @sohankunkerkar)
- Fixed a bug that prevented finishing the Workloads corresponding to Jobs deleted with --cascade=orphan. (#11689, @mbobrovskyi)
- KueueViz: Fixed a bug where list pages briefly flashed "No resources found" empty state messages before data finished loading over the WebSocket connection. (#11771, @YadavAkhileshh)
- KueueViz: Fixed a bug where non-JSON WebSocket messages from the backend could crash
  the frontend. KueueViz now reports such messages as errors instead of freezing the UI. (#11667, @YadavAkhileshh)
- KueueViz: Fixed a bug where workload and local queue detail pages would get stuck on a loading spinner instead of showing connection/fetching error messages on failure. (#11709, @YadavAkhileshh)
- KueueViz: Fixed frontend crashes caused by non-string errors in the error display. (#11666, @YadavAkhileshh)
- LeaderWorkerSet & Pods: Fixed a bug where LeaderWorkerSets with names longer than 39 characters failed to create pods with a `metadata.labels: Invalid value` error. This happened when the `kueue.x-k8s.io/pod-group-name` and
  `kueue.x-k8s.io/prebuilt-workload-name` labels, set by the LWS integration, exceeded 63 characters. 
  
  The `WorkloadIdentifierAnnotations` feature gate (disabled by default) resolves this by supporting these identifiers
  as both labels and annotations. LeaderWorkerSet now utilizes the annotation counterparts to support names up to
  52 characters. Labels, now along with annotations, remain the user-facing API for manually defining PodGroups. (#11409, @ivnovakov)
- MultiKueue: Add reconnect backoff guardrail to suppress redundant cluster reconciles for the MultiKueueCluster reconciler. (#11276, @reruno)
- MultiKueue: Fixed a bug in the AllAtOnce dispatcher where workloads evicted from a
  worker cluster could fail to be re-admitted. Kueue now waits for the ongoing eviction to
  complete before starting a new nomination and re-admission cycle. (#11472, @mszadkow)
- MultiKueue: Fixed a bug where a hung watch connection to one remote cluster could block
  reconciliation of other MultiKueueClusters, leaving them inactive and preventing workload
  admission. Kueue now applies a circuit-breaking timeout while establishing remote-cluster
  watches: the timeout starts at 1 minute and backs off exponentially on consecutive failures,
  up to 10 minutes. (#11328, @trilamsr)
- MultiKueue: Fixed a bug where a lost connection to a worker cluster failed to trigger the "workerLostTimeout" delay mechanism for workload requeuing. (#11559, @yuluo-yx)
- MultiKueue: Fixed a bug where one slow or unresponsive remote cluster could stall
  reconciliation for other MultiKueueClusters, even when
  `controller.groupKindConcurrency["MultiKueueCluster.kueue.x-k8s.io"]` was set above 1.
  This could delay or block admission through other healthy clusters. (#11333, @trilamsr)
- MultiKueue: Fixed admission for Kubernetes Jobs on Kubernetes 1.36 clusters by ensuring all Job status updates comply with the updated Kubernetes Job validation rules. See kubernetes/kubernetes#139281 for more details. (#11762, @olekzabl)
- MultiKueue: Fixed unnecessary `status.nominatedClusterNames` updates from the AllAtOnce
  dispatcher when the set of nominated clusters did not change. (#11508, @mszadkow)
- ObjectRetentionPolicies: Fixed a bug that doesn't allow the deletion of orphaned finished workloads. (#11754, @mbobrovskyi)
- Observability: fix the missing "replica-role" information from the logs generated by the controller managing the
  MultiKueueCluster instances. (#11272, @reruno)
- Scheduling: Fix a race condition within the admission process that could cause workloads waiting indefinitely for a preemption, causing head-of-line blocking of the affected ClusterQueues. (#11648, @kshalot)
- Scheduling: Fixed a bug where Kueue could inject duplicate tolerations when a
  ResourceFlavor toleration and a PodTemplate toleration differed only by `operator: ""`
  versus `operator: "Equal"`, which represent the same Kubernetes toleration. This could
  cause update rejections and leave Pods `scheduleGated`. (#11757, @benkermani)
- SparkApplication: Fixed a bug where `PodsReady` returned true based on driver state alone, so workloads
  with stuck executors never reached `waitForPodsReady.timeout`. (#11777, @hahahaheihei)
- TAS: Fixed a scheduling bug where a workload with multiple PodSets could be admitted even when the combined PodSets exceeded node pod capacity. (#11326, @yuluo-yx)
- TAS: ensure that `Snapshot()` does not perform update the list of workloads under the read-lock. (#11294, @mimowo)
- TAS: fix over-subscription of nodes that belong to multiple ResourceFlavors sharing the same hostname-leaf Topology.
  The TASHandleOverlappingFlavors is introduced as an alpha feature gate (disabled by default). (#11760, @tenzen-y)
- Workloads: Fixed a bug where, with the `FinishOrphanedWorkloads` feature gate enabled,
  Workloads could be marked `Finished` immediately after owner Job or JobSet creation. (#11534, @mbobrovskyi)

## v0.17.3

Changes since `v0.17.2`:

## Actions Required Before Upgrading

### (No, really, you MUST read this before you upgrade)

- **Minor releases:** Review the `.0` release notes for each new minor version you cross; see: [`v0.16.0`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.16.0), [`v0.17.0`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.0).
- **Patch releases:** Review the patch release notes leading up to this version, but *only* within this minor release line; see: [`v0.17.1`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.1), [`v0.17.2`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.17.2).

## Changes by Kind

### Feature

- Observability: Improved FairSharing strategy-evaluation logs by including DRS share values and emitting them at verbosity level V(4). (#11187, @PBundyra)

### Bug or Regression

- ElasticJobsViaWorkloadSlices: Fixed a bug where workload slices with identical creation timestamps could be incorrectly sorted, potentially leading to quota leaks during scale-up. (#11201, @KumarADITHYA123)
- Fixed a regression where Kueue could mark newly created Workloads as finished, potentially blocking queues. The FinishOrphanedWorkloads feature gate has been downgraded to Alpha. (#11018, @mbobrovskyi)
- Fixed multi-arch image builds for importer, kueue-populator, and kueueviz backend images so runtime images
  and binaries are built for the target platform, preventing wrong-architecture containers and exec format error
  for non-amd64 target platforms, such as arm64, ppc64le, and s390x. (#10916, @carterpewpew)
- Fixed vulnerability where two podsets with total requests exceeding max int64 would lead to integer overflow and break quota limits. (#11140, @pajakd)
- Helm: Fixed manager probe templates so periodSeconds correctly uses the configured periodSeconds value,
  rather than initialDelaySeconds. (#10980, @cixuuz)
- Helm: Fixed the FlowSchema priorityLevelConfiguration reference to use the Helm fullname template, preventing APF configuration from breaking when fullnameOverride or nameOverride is set. (#10983, @cixuuz)
- KueueViz: Fixed RBAC permissions for WorkloadPriorityClass objects by using the correct plural workloadpriorityclasses resource name. (#10984, @cixuuz)
- KueueViz: Fixed the LocalQueue details page to show only workloads from the selected queue. (#11212, @ManthanNimodiya)
- LeaderWorkerSet & StatefulSet: Fixed a race condition bug that could occasionally result in reverting, at the level of the Workload object, manual changes to the queue-name label for LeaderWorkerSet and StatefulSet. (#11193, @mbobrovskyi)
- Scheduling: Fixed a bug where in-flight workloads that were concurrently marked as finished (`Finished=True`) or deactivated could be requeued by Kueue's scheduler, causing re-scheduling attempts which were interfering with the scheduling of other workloads. (#11020, @mbobrovskyi)
- TAS: Fixed NodeHotSwap with TASReplaceNodeOnNodeTaints enabled to evaluate node taints using effective Workload tolerations, including tolerations from AdmissionCheck PodSetUpdates. (#11228, @Ladicle)
- TAS: Fixed a bug where multi-resource workloads, such as workloads requesting both CPU and memory,
  could fail admission during second-pass scheduling for ProvisioningRequests or NodeHotSwap because one
  resource's usage was double-counted against quota. (#11039, @cvgenesis)
- TAS: Fixed cache cleanup for non-TAS Pods that reach a terminal phase without Kueue observing the expected status update, preventing stale Pod usage from remaining in the TAS cache. (#11146, @amy)
- TAS: optimize performance of building the snapshot by pre-aggregating the node usage coming from non-TAS Pods. (#11041, @jzhaojieh)

## v0.17.2

Changes since `v0.17.1`:

## Changes by Kind

### Bug or Regression

- FailureRecovery: Forcefully delete pods that are Failed/Succeeded and scheduled on unreachable nodes.
  This unblocks cases like a JobSet deleting a Job with foreground cascade being stuck because a pod in a terminal phase exists on one of the unhealthy nodes. (#10856, @kshalot)
- Fix a race-condition bug that a deleted ClusterQueue may be kept by a finalizer, even after deletion of all workloads and LQs. (#10833, @ShaanveerS)
- Fixed a bug in Kueue's cache that could leave stale SubtreeQuota values in ancestor cohorts after a child Cohort
  was deleted, leading to potential over-admission of workloads and incorrect metrics reporting. (#10840, @mszadkow)
- Fixed a bug where admitted Workloads could fail to patch through the v1beta1 API due to CEL validation of the `priorityClassSource` immutability rule. (#10631, @kannon92)
- Observability: Fix kueue_cohort_subtree_quota and kueue_cohort_subtree_resource_reservations metrics incorrectly reporting raw milliCPU values instead of CPU units for CPU resources. (#10754, @baoalvin1)
- Observability: downgrade the non-compatible flavor error logs to Info level (v3). (#10639, @maishivamhoo123)
- TAS: Fix a bug where admitted workloads with unhealthy nodes were not evicted when an AdmissionCheck entered Retry or when the PodsReady recovery timeout was exceeded. (#10692, @pajakd)
- TAS: Fix handling of PodSet groups which could lead in some scenarios to empty topologyAssignment. (#10841, @mimowo)
- TAS: Fix nil pointer panic in TAS node reconciler when unadmitted workloads exist in the cluster. (#10653, @j-skiba)
- TAS: Refine the NodeHotSwap logic to ensure that UnhealthyNodes are only updated for workloads currently assigned to a Node via a topology topology assignment. This prevents "late pods" from stale topologies from triggering inaccurate health reporting. (#10837, @j-skiba)
- VisibilityOnDemand: Fixed a bug in the visibility endpoint, that listing workloads from a local queue includes
  workloads from other LocalQueues in different namespaces, if the other LocalQueues have the same name. (#10679, @mbobrovskyi)

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

