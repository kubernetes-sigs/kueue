## v0.15.0

Changes since `v0.14.0`:

## Urgent Upgrade Notes 

### (No, really, you MUST read this before you upgrade)

- MultiKueue: validate remote client kubeconfigs and reject insecure kubeconfigs by default; add feature gate MultiKueueAllowInsecureKubeconfigs to temporarily allow insecure kubeconfigs until v0.17.0.
  
  if you are using MultiKueue kubeconfigs which are not passing the new validation please
  enable the `MultiKueueAllowInsecureKubeconfigs` feature gate and let us know so that we can re-consider
  the deprecation plans for the feature gate. (#7439, @mszadkow)
 - The .status.flavors in LocalQueue is deprecated, which will be removed in the future release.
  
  You can consider migrating from the field usage to VisibilityOnDemand. (#7337, @iomarsayed)
 - Update DRA API used from `v1beta2` to `v1`
  
  in order to use DRA integration by enabling the DynamicResourceAllocation feature gate in Kueue you need to use k8s 1.34+. (#7212, @harche)
 - V1beta2: Expose the v1beta2 API for CRD serving. 
  
  V1beta1 remains supported in this release and used as storage, but please plan for migration.
  
  We would highly recommend preparing the Kueue CustomResources API version upgrade (v1beta1 -> v1beta2)
  since we plan to use v1beta2 for storage in 0.16, and discontinue the support for v1beta1 in 0.17. (#7304, @mimowo)
 
## Changes by Kind

### API Change

- Removed the deprecated workload annotation key "kueue.x-k8s.io/queue-name".
  
  Please ensure you are using the workload label "kueue.x-k8s.io/queue-name" instead. (#7271, @ganczak-commits)
- V1beta2: Delete .enable field from FairSharing API in config (#7583, @mbobrovskyi)
- V1beta2: Delete .enable field from WaitForPodsReady API in config (#7628, @mbobrovskyi)
- V1beta2: FlavorFungibility: introduce `MayStopSearch` in place of `Borrow`/`Preempt`, which are now deprecated in v1beta1. (#7117, @ganczak-commits)
- V1beta2: Graduate Config API to v1beta2. v1beta1 remains supported for this release, but please plan for migration. (#7375, @mbobrovskyi)
- V1beta2: Make .waitForPodsReady.timeout required field in the Config API (#7952, @tenzen-y)
- V1beta2: Make fairSharing.premptionStrategies required field in Config API (#7948, @tenzen-y)
- V1beta2: Remove deprecated PodIntegrationOptions (podOptions field) from v1beta2 Configuration.
  
   If you are using the podOptions in the configMap, you need to migrate to using  managedJobsNamespaceSelector (https://kueue.sigs.k8s.io/docs/tasks/run/plain_pods/) before
  the upgrade. (#7406, @nerdeveloper)
- V1beta2: Remove deprecated QueueVisibility in configMap (it was already non-functional). (#7319, @bobsongplus)
- V1beta2: Remove deprecated retryDelayMinutes field from v1beta2 AdmissionCheckSpec (it was already non-functional). (#7407, @nerdeveloper)
- V1beta2: Remove never used .status.fairSharing.admissionFairSharing field from ClusterQueue and Cohort (#7793, @tenzen-y)
- V1beta2: Removed deprecated Preempt/Borrow from FlavorFungibility API (#7527, @mbobrovskyi)
- V1beta2: The internal representation of TopologyAssignment (in WorkloadStatus) has been reorganized to allow using TAS for larger workloads. (More specifically, under the assumptions described in issue #7220, it allows to increase the maximal workload size from approx. 20k to approx. 60k nodes). (#7544, @olekzabl)
- V1beta2: change default for waitForPodsReady.blockAdmission to false (#7687, @mbobrovskyi)
- V1beta2: drop deprecated Flavors field from LocalQueueStatus (#7449, @mbobrovskyi)
- V1beta2: graduate the visibility API (#7411, @mbobrovskyi)
- V1beta2: introduce PriorityClassRef instead of PriorityClassSource and PriorityClassName (#7540, @mbobrovskyi)
- V1beta2: remove deprecated .spec.admissionChecks field from ClusterQueue API in favor of .spec.admissionChecksStrategy. (#7490, @nerdeveloper)
- `ReclaimablePods` feature gate is introduced to enable users switching on and off the reclaimable Pods feature (#7525, @PBundyra)

### Feature

- AdmissionChecks: introduce new optional fields in the workload status for admission checks to control the delay by 
  external and internal admission check controllers:
  - requeueAfterSeconds: specifies minimum wait time before retry
  - retryCount: Tracks retry attempts per admission check (#7620, @sohankunkerkar)
- AdmissionFairSharing: promote the feature to beta (enabled by default). (#7463, @kannon92)
- FailureRecovery: Introduce a mechanism to terminate Pods "stuck" in a terminating state due to node failures.
  The feature is activated by enabling the alpha FailureRecoveryPolicy feature gate (disabled by default).
  Only Pods with the kueue.x-k8s.io/safe-to-forcefully-terminate annotation are handled by the mechanism. (#7312, @kshalot)
- FlavorFungability: introduce the ClusterQueue's API for flavorFungability: `.spec.flavorFungability.preference` to indicate
  the user's preference for borrowing or preemption when there is no flavor which avoids both.
  This new field is a replacement for the alpha feature gate FlavorFungibilityImplicitPreferenceDefault which is considered as deprecated in 0.15 and will be removed in 0.16. (#7316, @vladikkuzn)
- Integrations: the Pod integration is no longer required to be enabled explicitly in the configMap when you are using LeaderWorkerSet, StatefulSet, or Deployment frameworks. (#6736, @IrvingMg)
- JobFramework: Introduce an optional interface for custom Jobs, called JobWithCustomWorkloadActivation, which can be used to deactivate or active a custom CRD workload. (#7199, @tg123)
- KueuePopulator: release of the new experimental sub-project called "kueue-populator". It allows to create the default ClusterQueue, ResourceFlavor and Topology. It also creates default LocalQueues in all namespaces managed by Kueue. (#7940, @mbobrovskyi)
- MultiKueue: Graduate the support for running external jobs to Beta. (#7669, @khrm)
- MultiKueue: It supports Topology Aware Scheduling (TAS) and ProvisioningRequest integration. (#5361, @IrvingMg)
- MultiKueue: Promote MultiKueueBatchJobWithManagedBy to beta which allows to synchronize the Job status periodically during Job execution between the worker and the management cluster for k8s batch Jobs. (#7341, @kannon92)
- MultiKueue: Support for authentication to worker clusters using ClusterProfile API. (#7570, @hdp617)
- Observability: Adjust the `cluster_queue_weighted_share` and `cohort_weighted_share` metrics to report the precise value for the Weighted share, rather than the value rounded to an integer. Also, expand the `cluster_queue_weighted_share` metric with the "cohort" label. (#7338, @j-skiba)
- Observability: Improve the messages presented to the user in scheduling events, by clarifying the reason for "insufficient quota" in case of workloads with multiple PodSets. 
  Before: "insufficient quota for resource-type in flavor example-flavor, request > maximum capacity (24 > 16)"
  After: "insufficient quota for resource-type in flavor example-flavor, previously considered podsets requests (16) + current podset request (8) > maximum capacity (16)" (#7232, @iomarsayed)
- Observability: Summarize the list of flavors considered for admission in the release cycle, but not used eventually for a workload which reserved the quota. 
  The summary is present in the message for the QuotaReserved condition, and in the event.
  Before: "Quota reserved in ClusterQueue tas-main, wait time since queued was 9223372037s"
  After: "Quota reserved in ClusterQueue tas-main, wait time since queued was 9223372037s; Flavors considered: one: default(NoFit;Flavor \"default\" does not support TopologyAwareScheduling)" (#7646, @mykysha)
- Observability: improve the message for the Preempted condition: include preemptor and preemptee object paths to make it easier to locate the objects involved in a preemption.
  Before: "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort"
  After: "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /r/c/q; preemptee path: /r/q_borrowing" (#7522, @mszadkow)
- Promote ManagedJobsNamespaceSelectorAlwaysRespected feature to Beta (#7493, @PannagaRao)
- Scheduling: support mutating the "kueue.x-k8s.io/workloadpriorityclass" label for Jobs with reserved quota. (#7289, @mbobrovskyi)
- TAS: It supports the Kubeflow TrainJob (#7249, @kaisoz)
- TAS: The balanced placement is introduced with the TASBalancedPlacement feature gate. (#6851, @pajakd)
- TAS: change the algorithm used in case of "unconstrained" mode (enabled by the kueue.x-k8s.io/podset-unconstrained-topology annotation, or when the "implicit" mode s used) from "BestFit" to "LeastFreeCapacity". 
  
  This allows to optimize the fragmentation for workloads which don't require bin-packing. (#7416, @iomarsayed)
- Transition QuotaReserved to false whenever setting Finished conditions (#7724, @mbobrovskyi)

### Documentation

- V1beta2: Adjust the documentation examples to use v1beta2 consistently. (#7910, @mszadkow)

### Bug or Regression

- AdmissionFairSharing: Fix the bug that occasionally a workload may get admitted from a busy LocalQueue,
  bypassing the entry penalties. (#7780, @IrvingMg)
- Fix a bug that an error during workload preemption could leave the scheduler stuck without retrying. (#7665, @olekzabl)
- Fix a bug that the cohort client-go lib is for a Namespaced resource, even though the cohort is a Cluster-scoped resource. (#7799, @tenzen-y)
- Fix a bug where a workload would not get requeued after eviction due to failed hotswap. (#7376, @pajakd)
- Fix eviction of jobs with memory requests in decimal format (#7430, @brejman)
- Fix existing workloads not being re-evaluated when new clusters are added to MultiKueueConfig. Previously, only newly created workloads would see updated cluster lists. (#6732, @ravisantoshgudimetla)
- Fix handling of RayJobs which specify the spec.clusterSelector and the "queue-name" label for Kueue. These jobs should be ignored by kueue as they are being submitted to a RayCluster which is where the resources are being used and was likely already admitted by kueue. No need to double admit.
  Fix on a panic on kueue managed jobs if spec.rayClusterSpec wasn't specified. (#7218, @laurafitzgerald)
- Fix integration of `manageJobWithoutQueueName` and `managedJobsNamespaceSelector` with JobSet by ensuring that jobSets without a queue are  not managed by Kueue if are not selected by the  `managedJobsNamespaceSelector`. (#7703, @MaysaMacedo)
- Fix invalid annotations path being reported in `JobSet` topology validations. (#7189, @kshalot)
- Fix issue #6711 where an inactive workload could transiently get admitted into a queue. (#7913, @olekzabl)
- Fix malformed annotations paths being reported for `RayJob` and `RayCluster` head group specs. (#7183, @kshalot)
- Fix the bug for the StatefulSet integration that the scale up could get stuck if
  triggered immediately after scale down to zero. (#7479, @IrvingMg)
- Fix the bug that a workload which was deactivated by setting the `spec.active=false` would not have the 
  `wl.Status.RequeueState` cleared. (#7734, @sohankunkerkar)
- Fix the bug that the kubernetes.io/job-name label was not propagated from the k8s Job to the PodTemplate in
  the Workload object, and later to the pod template in the ProvisioningRequest. 
  
  As a consequence the ClusterAutoscaler could not properly resolve pod affinities referring to that label,
  via podAffinity.requiredDuringSchedulingIgnoredDuringExecution.labelSelector. For example, 
  such pod affinities can be used to request ClusterAutoscaler to provision a single node which is large enough
  to accommodate all Pods on a single Node.
  
  We also introduce the PropagateBatchJobLabelsToWorkload feature gate to disable the new behavior in case of 
  complications. (#7613, @yaroslava-serdiuk)
- Fix the kueue-controller-manager startup failures.
  
  This fixed the Kueue CrashLoopBackOff due to the log message: "Unable to setup indexes","error":"could not setup multikueue indexer: setting index on workloads admission checks: indexer conflict. (#7432, @IrvingMg)
- Fix the race condition which could result that the Kueue scheduler occasionally does not record the reason
  for admission failure of a workload if the workload was modified in the meanwhile by another controller. (#7845, @mbobrovskyi)
- Fixed a bug that Kueue would keep sending empty updates to a Workload, along with sending the "UpdatedWorkload" event, even if the Workload didn't change. This would happen for Workloads using any other mechanism for setting
  the priority than the WorkloadPriorityClass, eg. for Workloads for PodGroups. (#7299, @mbobrovskyi)
- Fixed the bug that prevented managing workloads with duplicated environment variable names in containers. This issue manifested when creating the Workload via the API. (#7425, @mbobrovskyi)
- Kueue now properly validates and rejects unsupported DRA (Dynamic Resource Allocation) features with clear error messages instead of silently failing or producing misleading "DeviceClass not mapped" errors. Unsupported features include: AllocationMode 'All', CEL Selectors, Device Constraints, Device Config, FirstAvailable device selection, and AdminAccess. (#7226, @harche)
- MultiKueue x ElasticJobs: fix webhook validation bug which prevented scale up operation when any other
  than the default "AllAtOnce" MultiKueue dispatcher was used. (#7278, @mszadkow)
- MultiKueue: Remove remoteClient from clusterReconciler when kubeconfig is detected as invalid or insecure, preventing workloads from being admitted to misconfigured clusters. (#7486, @mszadkow)
- RBAC: Add rbac for train job for kueue-batch-admin and kueue-batch-user. (#7196, @kannon92)
- Scheduling: With BestEffortFIFO enabled, we will keep attempting to schedule a workload as long as
  it is waiting for preemption targets to complete. This fixes a bugs where an inadmissible
  workload went back to head of queue, in front of the preempting workload, allowing
  preempted workloads to reschedule (#7157, @gabesaba)
- Services: fix the setting of the `app.kubernetes.io/component` label to discriminate between different service components within Kueue as follows:
  - controller-manager-metrics-service for kueue-controller-manager-metrics-service 
  - visibility-service for kueue-visibility-server
  - webhook-service for kueue-webhook-service (#7371, @rphillips)
- TAS: Fix the `requiredDuringSchedulingIgnoredDuringExecution` node affinity setting being ignored in topology-aware scheduling. (#7899, @kshalot)
- TAS: Increase the number of Topology levels limitations for localqueue and workloads to 16 (#7423, @kannon92)
- TAS: Introduce missing validation against using incompatible `PodSet` grouping configuration in `JobSet, `MPIJob`, `LeaderWorkerSet`, `RayJob` and `RayCluster`. 
  
  Now, only groups of two `PodSet`s can be defined and one of the grouped `PodSet`s has to have only a single `Pod`.
  The `PodSet`s within a group must specify the same topology request via one of the `kueue.x-k8s.io/podset-required-topology` and `kueue.x-k8s.io/podset-preferred-topology` annotations. (#7061, @kshalot)
- Visibility API: Fix a bug that the Config clientConnection is not respected in the visibility server. (#7223, @tenzen-y)
- WorkloadRequestUseMergePatch: use "strict" mode for admission patches during scheduling which
  sends the ResourceVersion of the workload being admitted for comparing by kube-apiserver. 
  This fixes the race-condition issue that Workload conditions added concurrently by other controllers
  could be removed during scheduling. (#7246, @mszadkow)

### Other (Cleanup or Flake)

- RBAC: Restrict access to secrets for the Kueue controller manager only to secrets in the Kueue system namespace, ie
  kueue-system by default, or the one specified during installation with Helm. (#7188, @sbgla-sas)

