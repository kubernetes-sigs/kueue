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

