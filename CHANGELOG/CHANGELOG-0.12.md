## v0.12.2

Changes since `v0.12.1`:

## Changes by Kind

### Bug or Regression

- Fix a bug that would allow a user to bypass localQueueDefaulting. (#5460, @dgrove-oss)
- Helm: Fix a templating bug when configuring managedJobsNamespaceSelector. (#5396, @mtparet)
- RBAC permissions for the Cohort API to update & read by admins are now created out of the box. (#5433, @vladikkuzn)
- TAS: Fix a bug that LeastFreeCapacity Algorithm does not respect level ordering (#5470, @tenzen-y)
- TAS: Fix bug which prevented admitting any workloads if the first resource flavor is reservation, and the fallback is using ProvisioningRequest. (#5462, @mimowo)

## v0.12.1

Changes since `v0.12.0`:

## Urgent Upgrade Notes

### (No, really, you MUST read this before you upgrade)

- Move the API Priority and Fairness configuration for the visibility endpoint to a separate manifest file.
  This fixes the installation issues on GKE.

  If you relied on the configuration in 0.12.0, then consider installing it as opt-in from
  the visibility-apf.yaml manifest. (#5380, @mbobrovskyi)

## v0.12.0

> [!NOTE]
> This release is not deployable on GKE. GKE users should use Kueue 0.12.1 or newer.

Changes since `v0.11.0`:

## Urgent Upgrade Notes

### (No, really, you MUST read this before you upgrade)

- Fix the bug which annotated the Topology CRD as v1beta1 (along with the v1alpha1 version). This resulted in errors when Kueue was installed via Helm, for example, to list the topologies.

  The LocalQueue type for the status.flavors[].topology is changed from Topology to TopologyInfo, so if you import Kueue as code in your controller, you may need to adjust the code. (#4866, @mbobrovskyi)
 - Removed `IsManagingObjectsOwner` field from the `IntegrationCallbacks` struct.

  If you have implemented the `IsManagingObjectsOwner` field, you will need to remove it manually from your codebase. (#5011, @mbobrovskyi)
 - The API Priority and Fairness configuration for the visibility endpoint is installed by default.

  If your cluster is using k8s 1.28 or older, you will need to either update your version of k8s (to 1.29+) or remove the  FlowSchema and PriorityLevelConfiguration from the installation manifests of Kueue. (#5043, @mbobrovskyi)

## Changes by Kind

### API Change

- Added optional garbage collector for deactivated workloads. (#5177, @mbobrovskyi)
- Support specifying PodSetUpdates based on ProvisioningClassDetails by the configuration in ProvisioningRequestConfig. (#5204, @mimowo)
- TAS: Support ProvisioningRequests. (#5052, @mimowo)

### Feature

- Add 'kueue.x-k8s.io/podset' label to every admitted Job resource (#5215, @mszadkow)
- Add readOnlyRootFilesystem as default option for kueue deployment. (#5059, @kannon92)
- Added the following optional metrics, when waitForPodsReady is enabled:
  kueue_ready_wait_time_seconds - The time between a workload was created or requeued until ready, per 'cluster_queue'
  kueue_admitted_until_ready_wait_time_seconds - The time between a workload was admitted until ready, per 'cluster_queue'
  kueue_local_queue_ready_wait_time_seconds - The time between a workload was created or requeued until ready, per 'local_queue'
  kueue_local_queue_admitted_until_ready_wait_time_seconds - The time between a workload was admitted until ready, per 'local_queue' (#5136, @mszadkow)
- Allow users to update the priority of a workload from within a job. (#5197, @IrvingMg)
- Cert-manager: allow for external self-signed certificates for the metrics endpoint. (#4800, @sohankunkerkar)
- Evicted_workloads_once_total - counts number of unique workload evictions. (#5259, @mszadkow)
- Helm: Add an option to configure Kueue's mutation webhook with "reinvocationPolicy" (#5063, @yuvalaz99)
- Introduce ObjectRetentionPolicies.FinishedWorkloadRetention flag, which allows to configure a retention policy for Workloads, Workloads will be deleted when their retention period elapses, by default this new behavior is disabled (#2686, @mwysokin)
- Introduce the ProvisioningRequestConfig API which allows to instruct Kueue to create a ProvisioningRequest with a single PodSet with aggregated resources from multiple PodSets in the Workload (eg. PyTorchJob) created by the user. (#5290, @mszadkow)
- Output events to indicate which Worker Cluster was admitted to the Manager Cluster's workload. (#4750, @highpon)
- Promoted LocalQueueDefaulting to Beta and enabled by default. (#5038, @MaysaMacedo)
- Support a new fair sharing alpha feature `Admission Fair Sharing`, along with the new API. It orders workloads based on the recent usage coming from a LocalQueue the workload was submitted to. The recent usage is more important than the priority of workloads (#4687, @PBundyra)
- Support for JAX in the training-operator (#4613, @vladikkuzn)
- TAS: This supports Node replacement for TAS Workloads with an annotation `nodeToReplace`. It updates Workload's TopologyAssignments without requeuing it (#5287, @PBundyra)
- Update AppWrapper to v1.1.1 (#4728, @dgrove-oss)

### Bug or Regression

- Add Necessary RBAC to Update Cohort Status (#4703, @gabesaba)
- Allow one to disable cohorts via a HiearachialCohort feature gate (#4870, @kannon92)
- Fix Kueue crash caused by race condition when deleting ClusterQueue (#5292, @gabesaba)
- Fix LocalQueue's status message to reference LocalQueue, rather than ClusterQueue, when its status is Ready (#4955, @PBundyra)
- Fix RBAC configuration for the Topology API (#5120, @qti-haeyoon)
- Fix RBAC configuration for the Topology API to allow reading and editing by the service accounts using the Kueue Batch Admin role. (#4858, @KPostOffice)
- Fix RayJob webhook validation when `LocalQueueDefaulting` feature is enabled. (#5073, @MaysaMacedo)
- Fix a bug where PropagateResourceRequests would always trigger an API status patch call. (#5110, @alexeldeib)
- Fix a bug which caused Kueue's Scheduler to build invalid SSA patch in some scenarios when  using
  admission checks. This patch would be rejected with the following error message:
  Workload.kueue.x-k8s.io "job-xxxxxx" is invalid: [admissionChecks[0].lastTransitionTime: Required value (#4935, @alexeldeib)
- Fix bug where Cohorts with FairWeight set to 0 could have workloads running within Nominal Quota preempted (#4962, @gabesaba)
- Fix bug where update to Cohort.FairSharing didn't trigger a reconcile. This bug resulted in the new weight not being used until the Cohort was modified in another way. (#4963, @gabesaba)
- Fix bug which prevented using LeaderWorkerSet with manageJobsWithoutQueueName enabled. In particular, Kueue would create redundant workloads for each Pod, resulting in worker Pods suspended, while the leader Pods could bypass quota checks. (#4808, @mbobrovskyi)
- Fix bug which resulted in under-utilization of the resources in a Cohort.
  Now, when a ClusterQueue is configured with `preemption.reclaimWithinCohort: Any`,
  its resources can be lent out more freely, as we are certain that we can reclaim
  them later. Please see PR for detailed description of scenario. (#4813, @gabesaba)
- Fix classical preemption in the case of hierarchical cohorts. (#4806, @pajakd)
- Fix kueue-viz nil pointer error when defaulting kueue-viz backend/frontend images (#4727, @kannon92)
- Fix panic due to nil ptr exception in scheduler when ClusterQueue is deleted concurrently. (#5138, @sohankunkerkar)
- Fix the bug which prevented running Jobs (with queue-name label) owned by other Jobs for which Kueue does not
  have the necessary RBAC permissions (for example kserve or CronJob). (#5252, @mimowo)
- Fix the support for pod groups in MutliKueue. (#4854, @mszadkow)
- Fixed a bug that caused Kueue to create redundant workloads for each Job when manageJobsWithoutQueueName was enabled, JobSet integration was disabled, and AppWrapper was used for JobSet. (#4824, @mbobrovskyi)
- Fixed a bug that prevented Kueue from retaining the LeaderWorkerSet workload in deactivation status.
  Fixed a bug that prevented Kueue from automatically deleting the workload when the LeaderWorkerSet was deleted. (#4790, @mbobrovskyi)
- Fixed bug that allow to create Topology without levels. (#5013, @mbobrovskyi)
- Fixed bug that doesn't allow to use WorkloadPriorityClass on LeaderWorkerSet. (#4711, @mbobrovskyi)
- Helm: Fix missing namespace selector on webhook manifest. ManagedJobsNamespaceSelector option was only applied to pods/deployment/statefulset, now it is applied to all kind of jobs.
  Kube-system and Kueue release namespaces are still not excluded automatically by default. (#5323, @mtparet)
- Helm: Fix the default configuration for the metrics service. (#4903, @kannon92)
- Helm: fix ServiceMonitor selecting the wrong service. This previously led to missing Kueue metrics, even with `enablePrometheus` set to `true`. (#5074, @j-vizcaino)
- PodSetTopologyRequests are now configured only when TopologyAwareScheduling feature gate is enabled. (#4742, @mykysha)
- TAS: Add support for Node Selectors. (#4989, @mwysokin)
- TAS: Fix bug where scheduling panics when the workload using TopologyAwareScheduling has container request value specified as zero. (#4971, @qti-haeyoon)
- TAS: Fix the bug where TAS workloads may be admitted after restart of the Kueue controller. (#5276, @mimowo)
- TAS: fix accounting of TAS usage for workloads with multiple PodSets. This bug could prevent admitting workloads which otherwise could fit. (#5325, @lchrzaszcz)
- TAS: fix issues with the initialization of TAS cache in case of errors in event handlers. (#5309, @mimowo)

### Other (Cleanup or Flake)

- ATTENTION: Many renaming changes to prepare KueueViz to be suitable for production:
  - KueueViz is now mentioned KueueViz for its name, label and description
  - Path names and image names are now kueueviz (without hyphen) for consistency and naming restrictions
  - Helm chart values `.Values.kueueViz` remains unchanged as all parameters in helm chart values starts with lower case. (#4753, @akram)
- New kueueviz logo from CNCF design team. (#5247, @akram)
- Remove deprecated AdmissionCheckValidationRules feature gate. (#4995, @mszadkow)
- Remove deprecated InactiveWorkload condition reason. (#5050, @mbobrovskyi)
- Remove deprecated KeepQuotaForProvReqRetry feature gate. (#5030, @mbobrovskyi)
