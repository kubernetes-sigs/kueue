## v0.19.3

Changes since `v0.19.2`:

## Actions Required Before Upgrading

### (No, really, you MUST read this before you upgrade)

- **Minor releases:** Review the `.0` release notes for each new minor version you cross; see: [`v0.18.0`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.18.0), [`v0.19.0`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.19.0).
- **Patch releases:** Review the patch release notes leading up to this version, but *only* within this minor release line; see: [`v0.19.1`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.19.1), [`v0.19.2`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.19.2).

- DRA & ResourceTransformation: Fixed a bug where DRA device-class mapping or a resource transformation under the reserved resource name `pods` was silently discarded or left the Workload permanently pending.
  
  Remove or rename those entries before upgrading, or the kueue-controller-manager will fail to start. Renaming a mapping name or an `outputs` key also requires updating the matching ClusterQueue `nominalQuota` entries in the same change. (#14756, @thc1006)
 
## Changes by Kind

### Feature

- WorkloadAwareScheduler: Added the kueue.x-k8s.io/workload annotation to Pods created by Kueue-managed jobs when the SchedulerLibraryIntegration feature gate is enabled; previously only TopologyAwareScheduling added it. (#14796, @Singularity23x0)

### Bug or Regression

- AdmissionChecks: Fix a bug where the Workload has an Admitted=True condition regardless of AdmissionCheck Rejection state. (#14871, @TapanManu)
- AdmissionFairSharing: Fixed preemption ordering for Workloads from same-named LocalQueues in different namespaces so that LocalQueue usage is considered. (#14876, @tomsen02)
- DRA: Fixed config validation silently accepting capacity names with more than one slash, which produced a mapping that never matched any device. (#14953, @NasitSony)
- FairSharing: Fix a bug where Kueue could miss valid preemption targets after selecting workloads from the preemptor's own ClusterQueue and lowering its DRS. The fix is guarded by the Alpha `FairSharingReevaluatePreemptionCandidates` feature gate, which is disabled by default. Enabling the gate may increase exposure to the known fair-sharing preemption-loop issue tracked in #14543. (#14767, @lightZebra)
- Fixed a bug where a prebuilt or externally created Workload could be treated as equivalent to its Job even when the Job's pod template declared pod-level `resources` or `resourceClaims` that the Workload's PodSet omitted, letting the Workload reserve less quota than its Pods actually request. (#15016, @pujitha24)
- Fixed a controller panic triggered by Namespace updates after a ClusterQueue failed to initialize because its Cohort had a cycle. (#14983, @YQ-Wang)
- Helm: Fix a bug where user-defined metricsService labels are not propagated to the rendered manifests. (#15005, @HsiuChuanHsu)
- KueueCtl: Fixed a bug where `kueuectl delete workload` deleted a recreated owner with a different UID. (#14882, @DevaanshPathak)
- KueueCtl: Fixed the `kueuectl list clusterqueue` comand to respect KUEUECTL_LIST_REQUEST_LIMIT
  and paginate API requests instead of issuing an unbounded LIST request. (#14880, @ErikJiang)
- Kueueviz: Fixed the bug the WebSocket 1005 error would be shown on the dashboard after selecting a namespace. (#14789, @mykysha)
- MultiKueue: Fixed a bug where a Job is dispatched again due to propagated `spec.ttlSecondsAfterFinished` even after Job completion. Enable the Alpha `MultiKueueBatchJobClearingTTLSecondsAfterFinishedOnWorkerCluster` feature gate to enable fixing. (#14828, @kevin85421)
- MultiKueue: Fixed a bug where a remote workload finishing with reason
  OwnerNotFound was mirrored back verbatim, permanently finishing the manager
  Workload and leaving the manager Pod's scheduling gates stuck. Such finishes
  are now treated as a sync failure and reset for re-dispatch, matching
  existing OutOfSync handling. (#15082, @NasitSony)
- MultiKueue: Fixed a bug where the WorkloadPriorityClass controller incorrectly updated the priority of MultiKueue remote workloads when a WorkloadPriorityClass value changed. Remote workloads are now skipped during priority synchronization. (#14995, @weizhoublue)
- MultiKueue: Fixed stale observedGeneration on the AdmissionCheckActive condition after updating to a MultiKueueConfig that preserves the cluster health result. (#14915, @cryo-zd)
- MultiKueue: Truncate quota automation condition messages so unsupported manager/worker resource configurations can be reported successfully. (#14989, @cryo-zd)
- Observability: Fixed `kueue_pod_scheduling_gate_removal_seconds` observing negative durations when the controller clock trails the apiserver clock. The negative observations made the histogram's `_sum` decrease, which broke `rate()` over that series. (#14731, @Antrikshgwal)
- Observability: Scheduling hash re-computations are now logged at V5 via the contextual logger. (#15060, @apullo777)
- Pending Workloads rejected by a LimitRange are requeued when the LimitRange's max, min, or maxLimitRequestRatio change, or the LimitRange is deleted. (#15029, @tomsen02)
- Pod: Fixed a bug where a serving pod group's evicted pod could be left stuck in `Terminating` forever, since its `kueue.x-k8s.io/managed` finalizer was only removed for a Workload deletion, not for other evictions (e.g. a `recoveryTimeout` eviction). This could cause a legitimate replacement pod to be deleted as excess instead, or permanently block a same-name (StatefulSet-owned) replacement from ever being created. Kueue now removes the finalizer as soon as an evicted pod has actually terminated. (#14793, @mszadkow)
- ProvisioningRequest: Fixed a bug where the `Active` condition's `observedGeneration` on a ProvisioningRequest AdmissionCheck was not updated when a configuration change kept the check healthy, leaving `observedGeneration` permanently behind `metadata.generation`. (#14934, @weizhoublue)
- RayService: Fixed a bug where elastic (autoscaling) RayService pods could stay stuck in `SchedulingGated` on `kueue.x-k8s.io/elastic-job` after the origin workload slice was deleted, leaving the RayCluster below its desired replica count. (#15103, @kevin85421)
- Scheduling: Fix a bug in BestEffortFIFO where a workload with failed preemption could remain sticky at the queue head. (#15089, @tenzen-y)
- Scheduling: Fix workloads becoming stranded after scheduling snapshot failures, and stale pending accounting when a LocalQueue moves to another ClusterQueue. (#13885, @apullo777)
- Scheduling: Fixed a bug where Workloads differing only in PodSet names formed separate equivalence classes, so BestEffortFIFO queues re-evaluated each one individually and admission slowed on busy clusters. Controlled by the alpha `SchedulingEquivalenceHashingIgnorePodSetName` feature gate, disabled by default. When enabled, Workloads differing only in PodSet names share a scheduling equivalence class, so BestEffortFIFO queues stop re-evaluating each one individually. Requires `SchedulingEquivalenceHashing`. (#14804, @venuchitta)
- Scheduling: Fixed a bug where a Workload deactivated with a derived `DeactivatedDueTo<Cause>` reason (such as `DeactivatedDueToRequeuingLimitExceeded`) could remain stuck after reactivation because its `WorkloadRequeued` condition was not transitioned. Such Workloads are now reactivated correctly. (#14879, @adibmbrk)
- Scheduling: Fixed a bug where requeueing a Workload recomputed its scheduling equivalence hash even when neither the Workload nor its effective resource requests had changed, adding avoidable CPU and allocation overhead on the scheduler's requeue path. (#14958, @apullo777)
- Scheduling: Fixed a bug which would charge the quota based on the LimitRange (if specified) for workloads
  with only limits specified. That could create a mismatch between the charged quota and the resources actually
  used by the running Pods. (#15039, @tomsen02)
- SparkApplication: Fixed a bug where workloads using dynamic allocation could remain unready after executor scale-down. Workloads are now considered ready when the configured minimum number of executors is running. (#14984, @zhengchenyu)
- TrainJob: Fix a bug where TrainJobs are stuck by using merge patches, instead of Updates, when admitting
  or stopping TrainJobs, thus preserving the fields not represented in Kueue's vendored Trainer API. (#14842, @robert-bell)
- WorkloadPriorityClass: Fixed a bug where a Workload that had reserved quota was repeatedly written with
  a priorityClassRef the API server rejects, when its owner's WorkloadPriorityClass label was removed. (#15008, @tenzen-y)
- WorkloadPriorityClass: Fixed the WorkloadPriorityClass controller to update workloads referencing a changed class through a bounded, cancellable worker pool instead of a serial, uninterruptible loop, and to report a single update error instead of one per failed workload. (#14945, @pujitha24)

## v0.19.2

Changes since `v0.19.1`:

## Actions Required Before Upgrading

### (No, really, you MUST read this before you upgrade)

- **Minor releases:** Review the `.0` release notes for each new minor version you cross; see: [`v0.18.0`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.18.0), [`v0.19.0`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.19.0).
- **Patch releases:** Review the patch release notes leading up to this version, but *only* within this minor release line; see: [`v0.19.1`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.19.1).

## Changes by Kind

### Feature

- TAS: Reduced CPU time and memory allocations for snapshot creation by reusing cached topology trees when scheduling-relevant Node data is unchanged. Controlled by the `TASCacheTopologyTree` feature gate, which is Alpha and disabled by default. (#14639, @tenzen-y)
- WorkloadAwareScheduler: Delegated TAS node readiness and `spec.unschedulable` checks to the `scheduler-library` instead of applying them when building the TAS node cache. Controlled by the `SchedulerLibraryIntegration` feature gate, which is Alpha and disabled by default. (#14613, @alien1403)

### Bug or Regression

- AFS: Fixed entry-penalty accounting leaks that could inflate LocalQueue fair-sharing usage when a Workload was re-admitted or exited before settlement. (#14153, @apullo777)
- DRA: Fixed a bug where a negative extended-resource request quantity (reachable only when the `WorkloadValidateResourcesAreNonNegative` validation is disabled, or on a Workload created before that validation existed) could be merged as a negative DRA quota charge, silently offsetting a legitimate charge on the same logical resource. Negative extended-resource requests are now dropped the same way zero-valued ones already are. (#14655, @pujitha24)
- DRA: Fixed quota undercount when two extended resource names sharing a deviceClassMappings key were requested by different containers in the same PodSet. (#14200, @pujitha24)
- FairSharing: Collapsed the per-candidate FairSharing preemption log into one entry per ClusterQueue and serialize its DominantResourceShare values, reducing scheduler log volume at verbosity 4. (#14348, @venuchitta)
- FairSharing: skip the FairSharing preemption tournament when the preemptor's dominant resource share is +Inf, since no candidate can be preempted, avoiding wasted per-candidate evaluation and its V(4) log volume. (#14671, @venuchitta)
- Importer: Fixed a bug where the Pod importer picked a single ResourceFlavor for the whole Pod, so Pods whose resources map to different flavors could be imported with a wrong flavor assignment. Flavors are now resolved per requested resource. (#14579, @mszadkow)
- JobFramework: Fixed ancestor resolution to verify that each controller ownerReference's UID matches the referenced object. Previously an object whose ownerReference named a Kueue-managed ancestor with a stale or mismatched UID was treated as managed by that ancestor and was skipped by Kueue (not suspended/gated and no Workload created). (#14658, @vladikkuzn)
- LeaderWorkerSet: Fixed a bug where Pods of a LeaderWorkerSet admitted before the queue-name write moved into the LWS webhook could stay permanently SchedulingGated after an upgrade, eventually deactivating the Workload. Kueue now sets `kueue.x-k8s.io/queue-name` on LeaderWorkerSet Pods when adopting them and reconciles it on already-adopted gated Pods. (#14602, @anguszzzz)
- MPIJob: Fixed TAS defaulting for runLauncherAsWorker jobs with missing or additional replica-spec entries, preventing a webhook panic and preserving rank-based topology placement. (#14527, @thc1006)
- MultiKueue: Fixed a bug where a stale `status.nominatedClusterNames` could cause Server-Side Apply field manager conflicts with external dispatchers. Kueue now clears the field through a MutatingAdmissionPolicy when a Workload is admitted or evicted. (#14643, @vic-comm)
- MultiKueue: Fixed watch establishment to prevent timeouts from blocking indefinitely on delayed watch responses. (#14550, @Dasmat13)
- Observability: Fixed a bug where the `kueue_pod_scheduling_gate_removal_seconds` metric was missing the `replica_role` label (`leader`, `follower`, or `standalone`) carried by the other Kueue metrics. (#14488, @gangadhar-res)
- PodGroup integration: Fixed a bug where a Pod could bypass ClusterQueue quota by setting `kueue.x-k8s.io/pod-group-name` to another Workload's name. Kueue now only adopts Workloads created by the pod-group framework (stamped with `kueue.x-k8s.io/is-group-workload`), and no longer finalizes a foreign Workload that merely shares the pod group name, which previously marked it Finished and released its quota while its pods were still running. (#14630, @vladikkuzn)
- RayJob, RayCluster, RayService, and SparkApplication: Fixed a bug where removing the `kueue.x-k8s.io/queue-name` label from an unsuspended job was accepted, so the job stopped being managed by Kueue while its pods kept running and its resources were no longer counted against quota. Removing the label is now rejected, both from an unsuspended job and from a suspended job in a namespace with a default LocalQueue. Controlled by the `ValidateRayAndSparkJobUpdates` feature gate, which is Beta and enabled by default. (#14666, @ivnovakov)
- Scheduling: Fix preemption thrashing/loops caused by desynchronized eviction completion times by prioritizing preemptor workloads at the head of the scheduling queue. This is guarded by the PrioritizePreemptorWorkloads Alpha feature gate, disabled by default. (#14682, @Nilsachy)
- TAS: Fixed a bug where cross-flavor TAS usage was matched against topology domains a ResourceFlavor does not hold, adding redundant per-node work and V(3) log lines to every scheduling cycle. (#14174, @venuchitta)
- TAS: Fixed a bug where node replacement treated sibling topology domains with a common string prefix as the same domain. (#14452, @tomsen02)
- TAS: Fixed a bug where replacing an unhealthy node could assign a workload to a node already claimed by another workload in the same scheduling cycle, leaving its pod permanently Unschedulable until the PodsReady timeout evicted it. (#14647, @varunsyal)
- VisibilityOnDemand: Fixed a bug where the `PositionInLocalQueue` on the ClusterQueue `pendingworkloads` was being inflated when two LocalQueues in different namespaces share the same name (for example, the auto-created `default` LocalQueue). (#14435, @pujitha24)
- VisibilityOnDemand: Fixed a panic in the pending-workloads endpoints when prebuilt Workloads (BYOW) w/o priority are created (#14418, @thc1006)
- WaitForPodsReady: Fixed a bug where the `kueue_ready_wait_time_seconds`, `kueue_admitted_until_ready_wait_time`, `kueue_local_queue_ready_wait_time_seconds` and `kueue_local_queue_admitted_until_ready_wait_time_seconds` metrics were emitted after failure recovery, skewing the metric towards longer wait times. (#14638, @kshalot)

## v0.19.1

Changes since `v0.19.0`:

## Actions Required Before Upgrading

### (No, really, you MUST read this before you upgrade)

- **Minor releases:** Review the `.0` release notes for each new minor version you cross; see: [`v0.18.0`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.18.0), [`v0.19.0`](https://github.com/kubernetes-sigs/kueue/releases/tag/v0.19.0).

- LeaderWorkerSet: Fixed a quota bypass where raising `spec.leaderWorkerTemplate.size` on an already-admitted, Kueue-managed LeaderWorkerSet ran more pods per group than the reserved quota covered. `spec.leaderWorkerTemplate.size` is now immutable while the LeaderWorkerSet is managed by Kueue, behind the new `LWSImmutableGroupSize` feature gate (Beta, enabled by default). `spec.replicas` stays mutable.
  
  If you change `spec.leaderWorkerTemplate.size` on a Kueue-managed LeaderWorkerSet, recreate it at the new size instead, or disable the `LWSImmutableGroupSize` feature gate to keep the previous behavior, which also restores the quota bypass. (#13809, @ivnovakov)
 - TAS: Enforce stricter slice-size validation for Workloads. When podSetSliceRequiredTopology is specified, podSetSliceSize must also be specified and must be greater than 0. Non-positive slice sizes in topology constraints are also rejected.
  
  If you create Workload objects directly (or via custom controllers), update manifests before upgrade so that:
  
  - podSetSliceRequiredTopology is never set without podSetSliceSize
  - podSetSliceSize is always greater than 0
  - podSetSliceSize is not set when podSetSliceRequiredTopology is absent
  - every podsetSliceRequiredTopologyConstraints entry has size greater than 0
  
  If you need a phased rollout, temporarily disable TASValidateWorkloadSliceSize, clean up invalid Workloads, then re-enable it. (#13737, @mszadkow)
 - TAS: Fix a bug where TASRecomputeAssignmentWithinSchedulingCycle can be enabled even if TopologyAwareScheduling is disabled.
  
  If you disable TopologyAwareScheduling, also set TASRecomputeAssignmentWithinSchedulingCycle=false before upgrading. (#14257, @tenzen-y)
 
## Changes by Kind

### Feature

- Helm: the controller-manager Deployment now supports optional `controllerManager.strategy`, `controllerManager.hostNetwork`, and `controllerManager.dnsPolicy` values. (#13825, @dinhxuanvu)
- MultiKueue: Forwarded in-place `serveConfigV2` (Ray Serve application config) updates on a `RayService` from the manager to the worker cluster, so editing the Serve config on the manager now takes effect on the worker promptly. Changes to `rayClusterConfig`/`upgradeStrategy` (zero-downtime upgrade) are not yet propagated. (#14036, @kevin85421)

### Bug or Regression

- AFS: Fixed a bug that could modify cached Workload data while calculating LocalQueue fair-sharing usage, potentially producing inconsistent scheduling snapshots. (#13568, @aburan28)
- AFS: Fixed a bug where a LocalQueue with `fairSharing.weight: 0` could be prioritized for admission instead of deprioritized when AdmissionFairSharing is enabled. (#13559, @sumanthd032)
- AFS: Fixed pending Workload snapshot ordering when a referenced LocalQueue is missing. (#13515, @YQ-Wang)
- AdmissionFairSharing: Fixed a bug where resource usage smaller than one milli-unit was truncated to zero before it could accumulate, so with a long `usageHalfLifeTime` the `consumedResources` for CPU and extended resources such as GPUs stayed at `0` permanently and were ignored by fair sharing. (#13761, @Shreesha001)
- AdmissionFairSharing: Fixed a bug where workloads admitted via AdmissionChecks could keep their entry penalty permanently, inflating LocalQueue fair-sharing usage and deprioritizing later workloads. (#13795, @apullo777)
- AdmissionFairSharing: Fixed stale fair-sharing usage caused by entry penalties being applied to non-usage-based ClusterQueues or reapplied during second scheduling passes. (#13851, @apullo777)
- AdmissionFairSharing: Fixed transient LocalQueue lookup errors causing a pending-Workload snapshot to mix
  fair-sharing comparisons with standard queue ordering, resulting in a non-transitive comparator and inconsistent
  admission order. When a lookup fails, the entire snapshot now falls back to standard queue ordering. (#13546, @YQ-Wang)
- ClusterQueue: Fixed a bug where a terminating ClusterQueue (one with a deletion timestamp still retained by the resource-in-use finalizer because workloads are reserving quota) stopped updating `status.pendingWorkloads`, `status.admittedWorkloads`, and `status.reservingWorkloads` and never set its `Active` condition to `Terminating`, leaving stale status. Kueue now keeps the status of a terminating ClusterQueue accurate. (#13757, @kaushik229)
- ConcurrentAdmission: Fix preemption ordering by waiting for more-preferred variants to be evaluated for admission before opening the preemption gate for a less-preferred variant. (#14281, @yuluo-yx)
- Corrected invalid PodSet info errors to report the expected and actual PodSet counts in the correct order. (#13672, @cryo-zd)
- DRA: Fixed DeviceClass validation errors reporting a duplicated request field path with an incorrect request index in counter-based and capacity-based quota paths. (#13899, @cryo-zd)
- DRA: Fixed a bug where extended resource quota could be charged against a DeviceClass the scheduler would not allocate from when multiple DeviceClasses share the same `extendedResourceName`. (#14124, @thc1006)
- DRA: Fixed a startup crash when `KueueDRAIntegrationPartitionableDevices` or `KueueDRAIntegrationConsumableCapacity` feature gates are enabled but the ResourceSlice API (`resource.k8s.io/v1`) is not available on the cluster. (#13720, @MaysaMacedo)
- ElasticJobsViaWorkloadSlices & ProvisioningRequest: Fixed scale-from-zero admission for elastic jobs. Kueue now
  omits zero-count PodSets, which are invalid in a ProvisioningRequest. If there are no other PodSets requiring ProvisioningRequests the AdmissionCheck is marked Ready. (#14210, @neilb-dotcom)
- ElasticJobsViaWorkloadSlices: Fixed elastic jobs (e.g. autoscaling RayClusters via `ElasticJobsViaWorkloadSlices`) leaving scaled-up pods stuck `SchedulingGated` after the origin workload slice was deleted. (#14139, @dinhxuanvu)
- ElasticJobsViaWorkloadSlices: Fixed the bug that changes to the `kueue.x-k8s.io/priority-class` label were not
  reflected on the live Workload slices. (#13871, @thc1006)
- Fixed a bug where a ClusterQueue with `flavorFungibility.preference: PreemptionOverBorrowing` could leave workloads pending indefinitely. A flavor that required preemption but had no preemption candidates could outrank a later flavor that fits, purely because its quota was sourceable at a shallower borrowing level in the cohort tree. (#13896, @YQ-Wang)
- Fixed a bug where a Workload could be re-nominated to the same ResourceFlavor indefinitely and never reach the remaining flavors of its ResourceGroup. The flavor scan progress recorded for a Workload was discarded whenever the ClusterQueue's allocatable resource generation advanced, whenever the Workload was skipped due to in-cycle contention, or whenever the Workload was updated, all of which happen continuously on a busy Cohort. This most visibly affected Topology-Aware Scheduling, where a Workload whose topology cannot be placed on the flavor selected by quota needs to fall through to the next flavor. Controlled by the new `PreserveFlavorScanProgress` feature gate, enabled by default. (#13956, @varunsyal)
- Fixed a bug where a transient ProvisioningRequest or PodTemplate creation error could remain in Workload status and later be reported as the cause of an unrelated deactivation. (#13874, @apullo777)
- Fixed a bug where deleting a child object whose owner was already deleted (e.g. mixed foreground/background propagation during namespace teardown) could leave the child stuck in Terminating, because Kueue webhooks denied the garbage collector's finalizer-removal request with "workload owner not found". The tolerance applies only to objects already being deleted, and is gated by the new `SkipAncestorCheckForDeletedWorkloads` feature gate (Beta, enabled by default). (#13857, @tomsen02)
- Fixed a bug where elastic-job worker pods could remain SchedulingGated for up to ~90s after a scale rollover when the ungater requeued a slice that had already finished. (#14277, @dinhxuanvu)
- Fixed a bug where, with TASFailedNodeReplacementFailFast disabled, replacement pods for a workload whose node became unhealthy were ungated onto that same unhealthy node and immediately terminated, exhausting the pod recreation budget instead of waiting for a replacement domain. (#14119, @varunsyal)
- Fixed a quantity larger than `int64` on a resource other than `cpu` being converted to a number of another magnitude, or of another sign, when Kueue computes a Workload's requests. A large enough resource transformation product could arrive negative and then be floored to zero, so the Workload was admitted against no quota at all. (#14112, @thc1006)
- Fixed elastic job pods being ungated against a workload slice that was already being evicted, which allowed more pods to start than the slice still holding the reservation granted. (#13923, @thc1006)
- Fixed missing UpdatedWorkload event when the AdmissionGatedBy annotation is propagated from a StatefulSet to its Workload. (#14120, @Shreesha001)
- Fixed overly broad ClusterRole permissions by scoping webhook configuration and CRD access to only Kueue's own resources using `resourceNames` (#13610, @prash2512)
- Fixed resource totals wrapping to a negative number when two contributions to the same resource sum past the int64 range. Both Requests implementations now saturate in Add and Sub, as they already did in Mul, so an unrepresentable total is no longer read as an empty request. (#14108, @thc1006)
- HA: Fix a data race between concurrent reconciles in non-leading replicas, where the leader-aware decorator used one shared object as the destination for every lookup. (#14039, @thc1006)
- Helm: Add `kueueViz.{backend,frontend}.ingress.tlsEnabled` to explicitly enable or disable TLS independently of `tlsSecretName`, allowing TLS without a chart-managed Secret. When unset, the existing `tlsSecretName`-based behavior is preserved. (#13891, @meln5674)
- Job: Fixed a bug where failed indexes of Indexed Jobs using "backoffLimitPerIndex" continued to hold quota after being recorded in "status.failedIndexes". (#13717, @garg02)
- KueueViz: Fixed a bug that displayed thousands of duplicate error notifications when a Workload was preempted. Users receive a single notification for each preemption. (#13442, @Vaishnav88sk)
- KueueViz: Fixed crashes that occurred when the UI displayed error details containing values that could not be serialized. (#14038, @Dasmat13)
- LeaderWorkerSet & StatefulSet: Fixed reconciliation errors in one independent branch cancelling the other branches. 
  LeaderWorkerSet Workload creation, update, and deletion branches now continue independently, as do StatefulSet 
  Pod finalization and Workload reconciliation. (#14286, @thc1006)
- LeaderWorkerSet: Fixed a race when an existing Workload’s queue name and the LWS’s
  kueue.x-k8s.io/admission-gated-by annotation changed during the same reconciliation. Kueue now persists both
  changes atomically, preventing the Workload from entering the queue without its admission gate if the second
  update is delayed or fails. (#14144, @tenzen-y)
- MPIJob: Hardened `orderedReplicaTypes` against a nil `ReplicaSpec` value in `mpiReplicaSpecs`, avoiding a nil pointer dereference if such an object is ever constructed. Kubernetes API server schema pruning already prevents this from being reached through normal cluster usage. (#13739, @pujitha24)
- ManagedJobsNamespaceSelector: Fixed a bug that added the queue-name label and suspended Jobs in excluded namespaces. Jobs in excluded namespaces are left unchanged.` (#13459, @PannagaRao)
- MultiKueue & LeaderWorkerSet: Fixed a bug that prevented workloads from using PrebuiltWorkloads whose names exceeded the 63-character label limit when "WorkloadIdentifierAnnotations" was disabled. Kueue falls back to annotations for these Workloads. (#13650, @Dasmat13)
- MultiKueue: Fixed a bug where scaling an elastic job managed through workload slices could delete the running remote objects of the replaced slice mid-handover, disrupting the job's pods. The replaced slice is now finished with reason `WorkloadSliceReplaced`, matching the scheduler, so its remote objects are kept during the handover. (#13575, @kevin85421)
- MultiKueue: Fixed an issue where remote-cluster watcher goroutines could continue running after a worker cluster was removed, disconnected, or reconfigured. (#13829, @andrewseif)
- MultiKueue: The example `create-multikueue-kubeconfig.sh` now grants `update` and `patch` on `ray.io/rayclusters` to the MultiKueue worker ServiceAccount. Without this, elastic RayCluster worker-group replica changes made on the management cluster (via the `ElasticJobsViaWorkloadSlices` feature gate) were rejected on the worker cluster with a 403 Forbidden and never propagated. (#13653, @kevin85421)
- MultiKueue: The example worker-cluster RBAC generated by `create-multikueue-kubeconfig.sh` now grants `update` on `workloads`, which is required to propagate scale-down of elastic workloads (`ElasticJobsViaWorkloadSlices`) to the worker cluster. Without it, scaling an elastic workload down failed with a Forbidden error and the Workload reconcile looped. (#13693, @kevin85421)
- MultiKueue: an elastic RayCluster (ElasticJobsViaWorkloadSlices) managed by MultiKueue is now rejected at admission if `enableInTreeAutoscaling` is set, as MultiKueue does not support Ray autoscaling yet. Previously such a RayCluster was accepted but deleted right after admission due to inconsistent autoscaler-sidecar accounting between the manager and the worker. (#13563, @kevin85421)
- Observability: Fixed a bug where Kueue metrics could silently report incorrect quota and usage values for very large resource quantities due to integer overflow, potentially misleading dashboards and alerts. Metrics now preserve large values correctly and report unlimited quotas as "+Inf". (#14049, @tenzen-y)
- Observability: Fixed a bug where a LocalQueue could continue reporting stale admitted/reserving workload counts and resource usage after its referenced ClusterQueue was deleted. (#13835, @andrewseif)
- Observability: Fixed a bug where the `kueue_cluster_queue_resource_pending` metric could be permanently inflated when a LocalQueue resync pushed a workload that was already tracked as inadmissible in the ClusterQueue. (#13756, @RooobinYe)
- RayCluster: Fixed an unclear validation error for Kueue-managed RayClusters that enable in-tree autoscaling without being configured as elastic jobs. The error explains that "ElasticJobsViaWorkloadSlices" and the "kueue.x-k8s.io/elastic-job: "true"" annotation are required. (#14003, @kevin85421)
- RayJob: Fixed a bug where Workloads created for KubeRay RayJobs that ended in `ValidationFailed` could remain admitted and continue holding quota indefinitely. (#14192, @mszadkow)
- ResourceTransformations × DRA: Fixed negative generated totals so they no longer reduce retained Pod requests or DRA logical-resource charges. Negative outputs can still offset other generated outputs, and contributions to the same resource are now summed deterministically. Also fixed `multiplyBy` to scale generated outputs only; with `Retain`, the original input quantity remains unchanged. (#14032, @thc1006)
- Scheduling: Fixed a bug where editing the `nodeTaints` of a non-TAS ResourceFlavor did not retry workloads that had been made inadmissible by the taint, leaving them pending until an unrelated event triggered a retry. (#13688, @tomsen02)
- Scheduling: Fixed a bug where editing the `tolerations` or `nodeLabels` of a non-TAS ResourceFlavor did not retry workloads that had been left inadmissible by the previous spec, leaving them pending until an unrelated event triggered a retry. (#13734, @tomsen02)
- Scheduling: Fixed a bug where negative container resource requests or limits
  could create artificial ClusterQueue quota credit, allowing Workloads to bypass
  configured quota limits. Kueue now floors negative values to zero during quota
  accounting and rejects them during Workload validation by default. The
  validation is controlled by the Beta
  `WorkloadValidateResourcesAreNonNegative` feature gate. (#13391, @vladikkuzn)
- Scheduling: Kueue now recomputes an assignment calculated during nomination if its preemption targets overlap
  with targets selected for workloads processed earlier in the same scheduling cycle. This fixes a starvation scenario
  in which a large “hero” workload, on a busy cluster, could repeatedly conflict with earlier workloads on preemption
  targets and remain unscheduled; see #13320 for details.
  
  The behavior is controlled by the Beta `RecomputeAssignmentUponPreemptionTargetsOverlap` feature gate. (#14246, @tenzen-y)
- SparkApplication: Fixed a bug where errors adding volumes or volume mounts to driver and executor Pods were silently ignored. Configuration errors are reported during reconciliation instead. (#13562, @onkar717)
- TAS: Fixed a bug that could prevent admission of otherwise feasible LeaderWorkerSet workloads when the selected leader domain reduced the capacity available to workers. For example, a 1-CPU leader and four 2-CPU workers can now be placed across 2-, 4-, and 3-CPU hosts in one rack by assigning the leader to the 3-CPU host, leaving capacity for all four workers. (#13766, @YQ-Wang)
- TAS: Fixed a bug where a grouped PodSet (e.g. an LWS leader) with no requests for the TAS-managed resource was rejected with "no TAS flavor assigned". (#13859, @mszadkow)
- TAS: Fixed a bug where a node whose hostname matched the value of the topology's top level was silently excluded from placement. The node stayed Ready with free capacity but never received pods, because its domain was recorded as its own parent and never registered as a topology root. (#14232, @akshay-pm)
- TAS: Fixed a bug where in-place pod resize or node migration of a non-TAS pod on a TAS-relevant node never updated the scheduler's usage cache, causing TAS workloads to see stale capacity until the pod terminated. (#13822, @sohankunkerkar)
- TAS: Fixed a bug where inadmissible TAS workloads were not automatically requeued when non-TAS pods terminated, potentially leaving workloads stuck pending despite available capacity. (#13772, @sohankunkerkar)
- TAS: Fixed a bug where resource accounting was incorrect after a ResourceFlavor was deleted and recreated, including ClusterQueues with multiple TAS flavors, allowing workloads to be admitted against topology capacity already used by other admitted workloads. (#13612, @tomsen02)
- TAS: Fixed a bug where scaling up an elastic workload (`ElasticJobsViaWorkloadSlicesWithTAS`) with a leader/workers pod set group could overwrite the running leader pod's `TopologyAssignment` with a newly computed placement, causing the leader pod to be restarted and lose state. Kueue now preserves the leader's existing assignment and only places the newly added workers. (#13813, @RooobinYe)
- TAS: Fixed a bug where the scheduler panicked and crash-looped when logging the snapshot at verbosity `>= 6`, if a topology domain had capacity but no admitted TAS workloads. (#13686, @venuchitta)
- TAS: Fixed a bug where updating `spec.nodeTaints` on a ResourceFlavor with `spec.topologyName` set did not retry inadmissible workloads, leaving them Pending until an unrelated event triggered a requeue. (#13656, @tomsen02)
- TAS: Fixed a bug where workloads were rejected when a node capacity-to-request ratio exceeded the int32 range and the VectorizedResourceRequests feature gate was disabled. (#13530, @tomsen02)
- TAS: Fixed an issue in queue management where workloads requiring a second pass of scheduling could be pre-queued multiple times concurrently, causing duplicate backoff timer callbacks. (#13747, @j-skiba)
- TAS: Fixed an issue where workloads taking a second pass to complete a delayed topology assignment or replace a failed node could lose their existing quota reservation when `waitForPodsReady.blockAdmission` was enabled. Replacing a failed node could also clear the admission of a running workload. (#13736, @apullo777)
- TAS: Fixed inconsistent use of the vectorized `SliceRequests` implementation introduced in #2953 to 
  optimize TAS hot paths. The remaining direct uses of `MapRequests` in non-hot paths are now replaced
  with the `Requests` abstraction, with the implementation selected by factory functions based on the
  `VectorizedResourceRequests` feature gate. The previous `MapRequests` implementation remains available
  when the feature gate is disabled. (#13487, @j-skiba)
- TAS: Fixed regression where admission failure events for Topology-Aware Scheduling (TAS) missed reporting the limiting resource when a node's remaining capacity was zero. (#13400, @j-skiba)
- TAS: Reduced excessively large assumptions-violation logs to a short summary. Operators can view the individual leaf domain IDs at verbosity 6 when detailed diagnostics are needed. (#14259, @tenzen-y)
- The Workload validating webhook panicked when the `QuotaReserved` condition was set while `status.admission` was absent, so the API server refused the request with an internal error rather than one naming the field. It is now refused as a validation error. An update that leaves a Workload in the state it was already in is allowed through, so an object that entered etcd without passing through admission, from a restore or a migration, can still be updated and removed. (#14014, @thc1006)
- Workload: Fixed a bug where Workloads with invalid labels or annotations in PodSet template metadata could be admitted and fail later when creating Pods. Kueue now rejects them during admission, controlled by the WorkloadValidationForPodSetMetadata feature gate (Beta, enabled by default). (#13679, @Dasmat13)

### Other (Cleanup or Flake)

- Helm: Aligned the default integration framework ordering with the Kustomize
  controller configuration. This does not change the set of enabled integrations
  or their runtime behavior. (#13384, @YQ-Wang)
- KueueViz: Removed debug console.log and console.error statements from the frontend. WebSocket connection events, flavor data updates, and message-parse errors no longer appear in the browser developer console. Errors are still visible in the KueueViz UI through normal React state handling. (#14172, @Dasmat13)
- Observability: Fixed logs at verbosity 6 and below that did not conform to the JSON Lines format, allowing log collectors to parse them consistently. (#13666, @Dasmat13)
- Scheduling: Unified the remaining resource-request construction paths to use factory methods that select either the `MapRequests` or `SliceRequests` implementation based on the `VectorizedResourceRequests` feature gate. (#13755, @Vaishnav88sk)

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

