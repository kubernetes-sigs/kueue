## v0.12.7

Changes since `v0.12.6`:

## Changes by Kind

### Bug or Regression

- Fix support for PodGroup integration used by external controllers, which determine the
  the target LocalQueue and the group size only later. In that case the hash would not be
  computed resulting in downstream issues for ProvisioningRequest.

  Now such an external controller can indicate the control over the PodGroup by adding
  the `kueue.x-k8s.io/pod-suspending-parent` annotation, and later patch the Pods by setting
  other metadata, like the kueue.x-k8s.io/queue-name label to initiate scheduling of the PodGroup. (#6463, @pawloch00)
- TAS: fix the bug that Kueue is crashing when PodSet has size 0, eg. no workers in LeaderWorkerSet instance. (#6524, @mimowo)

## v0.12.6

Changes since `v0.12.5`:

## Urgent Upgrade Notes

### (No, really, you MUST read this before you upgrade)

- Rename kueue-metrics-certs to kueue-metrics-cert cert-manager.io/v1 Certificate name in cert-manager manifests when installing Kueue using the Kustomize configuration.

  If you're using cert-manager and have deployed Kueue using the Kustomize configuration, you must delete the existing kueue-metrics-certs cert-manager.io/v1 Certificate before applying the new changes to avoid conflicts. (#6361, @mbobrovskyi)

## Changes by Kind

### Bug or Regression

- Fix accounting for the `evicted_workloads_once_total` metric:
  - the metric wasn't incremented for workloads evicted due to stopped LocalQueue (LocalQueueStopped reason)
  - the reason used for the metric was "Deactivated" for workloads deactivated by users and Kueue, now the reason label can have the following values: Deactivated, DeactivatedDueToAdmissionCheck, DeactivatedDueToMaximumExecutionTimeExceeded, DeactivatedDueToRequeuingLimitExceeded. This approach aligns the metric with `evicted_workloads_total`.
  - the metric was incremented during preemption before the preemption request was issued. Thus, it could be incorrectly over-counted in case of the preemption request failure.
  - the metric was not incremented for workload evicted due to NodeFailures (TAS)

  The existing and introduced DeactivatedDueToXYZ reason label values will be replaced by the single "Deactivated" reason label value and underlying_cause in the future release. (#6363, @mimowo)
- Fix the bug which could occasionally cause workloads evicted by the built-in AdmissionChecks
  (ProvisioningRequest and MultiKueue) to get stuck in the evicted state which didn't allow re-scheduling.
  This could happen when the AdmissionCheck controller would trigger eviction by setting the
  Admission check state to "Retry". (#6301, @mimowo)
- Fixed a bug that prevented adding the kueue- prefix to the secretName field in cert-manager manifests when installing Kueue using the Kustomize configuration. (#6344, @mbobrovskyi)
- ProvisioningRequest: Fix a bug that Kueue didn't recreate the next ProvisioningRequest instance after the
  second (and consecutive) failed attempt. (#6330, @PBundyra)
- Support disabling client-side ratelimiting in Config API clientConnection.qps with a negative value (e.g., -1) (#6306, @tenzen-y)
- TAS: Fix a bug that the node failure controller tries to re-schedule Pods on the failure node even after the Node is recovered and reappears (#6348, @pajakd)

## v0.12.5

Changes since `v0.12.4`:

## Changes by Kind

### Bug or Regression

- Emit the Workload event indicating eviction when LocalQueue is stopped (#5994, @amy)
- Fix incorrect workload admission after CQ is deleted in a cohort reducing the amount of available quota. The culprit of the issue was that the cached amount of quota was not updated on CQ deletion. (#6026, @amy)

## v0.12.4

Changes since `v0.12.3`:

## Changes by Kind

### Feature

- Helm: support for specifying nodeSelector and tolerations for all Kueue components (#5869, @zmalik)

### Bug or Regression

- Fix a bug where the GroupKindConcurrency in Kueue Config is not propagated to the controllers (#5826, @tenzen-y)
- TAS: Fix a bug for the incompatible NodeFailureController name with Prometheus (#5824, @tenzen-y)
- TAS: Fix a bug that Kueue unintentionally gives up a workload scheduling in LeastFreeCapacity if there is at least one unmatched domain. (#5804, @PBundyra)
- TAS: Fix a bug that the tas-node-failure-controller unexpectedly is started under the HA mode even though the replica is not the leader. (#5851, @tenzen-y)
- TAS: Fix the bug when Kueue crashes if the preemption target, due to quota, is using a node which is already deleted. (#5843, @mimowo)

### Other (Cleanup or Flake)

- KueueViz: reduce the image size from 1.14 GB to 267MB, resulting in faster pull and shorter startup time. (#5875, @mbobrovskyi)

## v0.12.3

Changes since `v0.12.2`:

## Urgent Upgrade Notes

### (No, really, you MUST read this before you upgrade)

- Helm:

  - Fixed KueueViz installation when enableKueueViz=true is used with default values for the image specifying parameters.
  - Split the image specifying parameters into separate repository and tag, both for KueueViz backend and frontend.

  If you are using Helm charts and installing KueueViz using custom images,
  then you need to specify them by kueueViz.backend.image.repository, kueueViz.backend.image.tag,
  kueueViz.fontend.image.repository and kueueViz.frontend.image.tag parameters. (#5514, @mbobrovskyi)

## Changes by Kind

### Feature

- Allow setting the controller-manager's Pod `PriorityClassName` from the Helm chart (#5649, @kaisoz)

### Bug or Regression

- Add Cohort Go client library (#5603, @tenzen-y)
- Fix the bug that Job deleted on the manager cluster didn't trigger deletion of pods on the worker cluster. (#5607, @ichekrygin)
- Fix the bug that Kueue, upon startup, would incorrectly admit and then immediately deactivate
  already deactivated Workloads.

  This bug also prevented the ObjectRetentionPolicies feature from deleting Workloads
  that were deactivated by Kueue before the feature was enabled. (#5629, @mbobrovskyi)
- Fix the bug that the webhook certificate setting under `controllerManager.webhook.certDir` was ignored by the internal cert manager, effectively always defaulting to /tmp/k8s-webhook-server/serving-certs. (#5491, @ichekrygin)
- Fixed bug that doesn't allow Kueue to admit Workload after queue-name label set. (#5714, @mbobrovskyi)
- MultiKueue: Fix a bug that batch/v1 Job final state is not synced from Workload cluster to Management cluster when disabling the `MultiKueueBatchJobWithManagedBy` feature gate. (#5706, @ichekrygin)
- TAS: fix the bug which would trigger unnecessary second pass scheduling for nodeToReplace
  in the following scenarios:
  1. Finished workload
  2. Evicted workload
  3. node to replace is not present in the workload's TopologyAssignment domains (#5591, @mimowo)
- TAS: fix the scenario when deleted workload still lives in the cache. (#5605, @mimowo)
- Use simulation of preemption for more accurate flavor assignment.
  In particular, in certain scenarios when preemption while borrowing is enabled,
  the previous heuristic would wrongly state that preemption was possible. (#5700, @gabesaba)
- Use simulation of preemption for more accurate flavor assignment.
  In particular, the previous heuristic would wrongly state that preemption
  in a flavor was possible even if no preemption candidates could be found.

  Additionally, in scenarios when preemption while borrowing is enabled,
  the flavor in which reclaim is possible is preferred over flavor where
  priority-based preemption is required. This is consistent with prioritizing
  flavors when preemption without borrowing is used. (#5740, @gabesaba)

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
