## v0.11.9

Changes since `v0.11.8`:

## Changes by Kind

### Bug or Regression

- Emit the Workload event indicating eviction when LocalQueue is stopped (#5995, @amy)
- Fix incorrect workload admission after CQ is deleted in a cohort reducing the amount of available quota. The culprit of the issue was that the cached amount of quota was not updated on CQ deletion. (#6027, @amy)

## v0.11.8

Changes since `v0.11.7`:

## Changes by Kind

### Feature

- Helm: Add an option to configure Kueue's mutation webhook with "reinvocationPolicy" (#5785, @mbobrovskyi)

### Bug or Regression

- Fix a bug where the GroupKindConcurrency in Kueue Config is not propagated to the controllers (#5830, @tenzen-y)
- TAS: Fix a bug that Kueue unintentionally gives up a workload scheduling in LeastFreeCapacity if there is at least one unmatched domain. (#5805, @PBundyra)
- TAS: Fix the bug when Kueue crashes if the preemption target, due to quota, is using a node which is already deleted. (#5844, @mimowo)

## v0.11.7

Changes since `v0.11.6`:

## Changes by Kind

### Feature

- Allow setting the controller-manager's Pod `PriorityClassName` from the Helm chart (#5650, @kaisoz)

### Bug or Regression

- Add Cohort Go client library (#5604, @tenzen-y)
- Fix the bug that Job deleted on the manager cluster didn't trigger deletion of pods on the worker cluster. (#5608, @ichekrygin)
- Fix the bug that Kueue, upon startup, would incorrectly admit and then immediately deactivate
  already deactivated Workloads.

  This bug also prevented the ObjectRetentionPolicies feature from deleting Workloads
  that were deactivated by Kueue before the feature was enabled. (#5630, @mbobrovskyi)
- Fix the bug that the webhook certificate setting under `controllerManager.webhook.certDir` was ignored by the internal cert manager, effectively always defaulting to /tmp/k8s-webhook-server/serving-certs. (#5490, @ichekrygin)
- Fixed bug that doesn't allow Kueue to admit Workload after queue-name label set. (#5715, @mbobrovskyi)
- MultiKueue: Fix a bug that batch/v1 Job final state is not synced from Workload cluster to Management cluster when disabling the `MultiKueueBatchJobWithManagedBy` feature gate. (#5707, @ichekrygin)

## v0.11.6

Changes since `v0.11.5`:

## Changes by Kind

### Bug or Regression

- Fix a bug that would allow a user to bypass localQueueDefaulting. (#5478, @dgrove-oss)
- RBAC permissions for the Cohort API to update & read by admins are now created out of the box. (#5434, @vladikkuzn)
- TAS: Fix a bug that LeastFreeCapacity Algorithm does not respect level ordering (#5468, @tenzen-y)

## v0.11.5

Changes since `v0.11.4`:

## Changes by Kind

### Bug or Regression

- Fix Kueue crash caused by race condition when deleting ClusterQueue (#5296, @gabesaba)
- Fix RayJob webhook validation when `LocalQueueDefaulting` feature is enabled. (#5073, @MaysaMacedo)
- Fix a bug where PropagateResourceRequests would always trigger an API status patch call. (#5132, @alexeldeib)
- Fix panic due to nil ptr exception in scheduler when ClusterQueue is deleted concurrently. (#5207, @sohankunkerkar)
- Fix the bug which prevented running Jobs (with queue-name label) owned by other Jobs for which Kueue does not
  have the necessary RBAC permissions (for example kserve or CronJob). (#5263, @mimowo)
- TAS: Fix RBAC configuration for the Topology API (#5122, @qti-haeyoon)
- TAS: Fix the bug where TAS workloads may be admitted after restart of the Kueue controller. (#5334, @mimowo)
- TAS: fix accounting of TAS usage for workloads with multiple PodSets. This bug could prevent admitting workloads which otherwise could fit. (#5342, @lchrzaszcz)
- TAS: fix issues with the initialization of TAS cache in case of errors in event handlers. (#5351, @mimowo)

## v0.11.4

Changes since `v0.11.3`:

## Changes by Kind

### Bug or Regression

- TAS: Add support for Node Selectors. (#5079, @mwysokin)
- Allow one to disable cohorts via a HiearachialCohort feature gate (#4913, @sohankunkerkar)
- Fix LocalQueue's status message to reference LocalQueue, rather than ClusterQueue, when its status is Ready (#4956, @PBundyra)
- Fix a bug which caused Kueue's Scheduler to build invalid SSA patch in some scenarios when  using
  admission checks. This patch would be rejected with the following error message:
  Workload.kueue.x-k8s.io "job-xxxxxx" is invalid: [admissionChecks[0].lastTransitionTime: Required value (#5086, @alexeldeib)
- Fix bug where Cohorts with FairWeight set to 0 could have workloads running within Nominal Quota preempted (#4980, @gabesaba)
- Fix bug where update to Cohort.FairSharing didn't trigger a reconcile. This bug resulted in the new weight not being used until the Cohort was modified in another way. (#4965, @gabesaba)
- Fix bug which prevented using LeaderWorkerSet with manageJobsWithoutQueueName enabled. In particular, Kueue would create redundant workloads for each Pod, resulting in worker Pods suspended, while the leader Pods could bypass quota checks. (#4936, @mbobrovskyi)
- Helm: Fix the default configuration for the metrics service. (#4905, @kannon92)
- Fix the support for pod groups in MutliKueue. (#4909, @mszadkow)
- Fixed a bug that caused Kueue to create redundant workloads for each Job when manageJobsWithoutQueueName was enabled, JobSet integration was disabled, and AppWrapper was used for JobSet. (#4924, @mbobrovskyi)
- Fixed a bug that prevented Kueue from retaining the LeaderWorkerSet workload in deactivation status.
  Fixed a bug that prevented Kueue from automatically deleting the workload when the LeaderWorkerSet was deleted. (#5015, @mbobrovskyi)
- Fixed bug that allow to create Topology without levels. (#5016, @mbobrovskyi)
- Helm: fix ServiceMonitor selecting the wrong service. This previously led to missing Kueue metrics, even with `enablePrometheus` set to `true`. (#5082, @j-vizcaino)
- TAS: Fix bug where scheduling panics when the workload using TopologyAwareScheduling has container request value specified as zero. (#4973, @qti-haeyoon)

## v0.11.3

Changes since `v0.11.2`:

## Urgent Upgrade Notes

### (No, really, you MUST read this before you upgrade)

- Fix the bug which annotated the Topology CRD as v1beta1 (along with the v1alpha1 version). This resulted in errors when Kueue was installed via Helm, for example, to list the topologies.

  The LocalQueue type for the status.flavors[].topology is changed from Topology to TopologyInfo, so if you import Kueue as code in your controller, you may need to adjust the code. (#4873, @mbobrovskyi)

## Changes by Kind

### Bug or Regression

- Fix RBAC configuration for the Topology API to allow reading and editing by the service accounts using the Kueue Batch Admin role. (#4864, @KPostOffice)

## v0.11.2

IMPORTANT: Avoid using this release due to the corrupted [Topology CRD specification](https://github.com/kubernetes-sigs/kueue/issues/4850).
When upgrading to the newer version please reinstall Kueue. If you are not using TopologyAwareScheduling the upgrading is not urgent.

Changes since `v0.11.1`:

## Changes by Kind

### Bug or Regression

- Fix bug which resulted in under-utilization of the resources in a Cohort.
  Now, when a ClusterQueue is configured with `preemption.reclaimWithinCohort: Any`,
  its resources can be lent out more freely, as we are certain that we can reclaim
  them later. Please see PR for detailed description of scenario. (#4822, @gabesaba)
- PodSetTopologyRequests are now configured only when TopologyAwareScheduling feature gate is enabled. (#4797, @mykysha)

## v0.11.1

IMPORTANT: Avoid using this release due to the corrupted [Topology CRD specification](https://github.com/kubernetes-sigs/kueue/issues/4850).
When upgrading to the newer version please reinstall Kueue. If you are not using TopologyAwareScheduling the upgrading is not urgent.

Changes since `v0.11.0`:

## Changes by Kind

### Bug or Regression

- Add Necessary RBAC to Update Cohort Status (#4723, @gabesaba)
- Fixed bug that doesn't allow to use WorkloadPriorityClass on LeaderWorkerSet. (#4725, @mbobrovskyi)

## v0.11.0

IMPORTANT: Avoid using this release due to the corrupted [Topology CRD specification](https://github.com/kubernetes-sigs/kueue/issues/4850).
When upgrading to the newer version please reinstall Kueue. If you are not using TopologyAwareScheduling the upgrading is not urgent.

Changes since `0.10.0`:

## Urgent Upgrade Notes

### (No, really, you MUST read this before you upgrade)

- If you implement the GenericJob interface for a custom Job CRD you need to update the
  implementation of the PodSets function as its signature is extended to return potential errors. (#4002, @Horiodino)
 - If you implement the GenericJob interface you need to update the implementation as
  the PodSet.Name field has changed its type from string to PodSetReference. (#4417, @vladikkuzn)
 - The configuration field `integrations.podOptions` is deprecated.

  Users who set the `Integrations.PodOptions` namespace selector to a non-default
  value should plan for migrating to use `managedJobsNamespaceSelector` instead, as the PodOptions
  selector is going to be removed in a future release. (#4256, @dgrove-oss)

## Changes by Kind

### Feature

- Add a column to multikueue indicating if it is connected to worker cluster (#4335, @highpon)
- Add an integration for AppWrappers to Kueue. (#3953, @dgrove-oss)
- Add integration for LeaderWorkerSet where Pods are managed by the pod-group integration. (#3515, @vladikkuzn)
- Add the full name of the preempting workload to applicable log lines to make debugging preemption easier. (#4398, @KPostOffice)
- Adds kueue-viz helm charts allowing installation of kueue-viz using helm (#3852, @akram)
- Adds the JobUID to preemption condition messages.  This makes it easy to get the preempting workload using `kubectl get workloads --selector=kueue.x-k8s.io/job-uid=<JobUID>` (#4524, @avrittrohwer)
- Allow mutating queue-name label in StatefulSet Webhook when ReadyReplicas equals zero. (#3520, @mbobrovskyi)
- Allow one to configure certificates for metrics (#4385, @kannon92)
- Expose the TopologyName in LocalQueue status (#4543, @mbobrovskyi)
- Helm: Adds kueueViz parameters `enableKueueViz`, `kueueViz.backend.image` and `kueueViz.frontend.image` (#4410, @akram)
- Improve error messages when the ProvisioningRequest Admission Check is not initialized due to a missing or unsupported version of the ProvisioningRequest CRD. (#4131, @Horiodino)
- Kueue exposes a new recovery mechanism as part of the WaitForPodsReady API. This evicts jobs which surpasses configured threshold for pod's recovery during runtime (#4301, @PBundyra)
- Kueue exposes a new recovery mechanism as part of the WaitForPodsReady API. This evicts jobs which surpasses configured threshold for pod's recovery during runtime (#4302, @PBundyra)
- Kueue-viz: allow to configure the application port by the KUEUE_VIZ_PORT env. variable. (#4178, @lekaf974)
- MultiKueue: Add support for Kubeflow Training-Operator Jobs  `spec.runPolicy.managedBy` field (#4116, @mszadkow)
- Support KubeRay integrations (RayJob and RayCluster) for MultiKueue via the managedBy mechanism.
  This allows for the installation of the Ray operator on the management cluster. (#4677, @mszadkow)
- Support Pod integration in MultiKueue. (#4034, @Bobbins228)
- Support RayCluster in MultiKueue, assuming only Ray CRDs are installed on the management cluster. (#3959, @mszadkow)
- Support RayJob in MultiKueue, assuming only Ray CRDs are installed on the management cluster. (#3892, @mszadkow)
- TAS: Add a new default algorithm to minimize resource fragmentation. The old algorithm is gated behind the `TASLargestFit` feature gate. (#4228, @PBundyra)
- TAS: Support cohorts and preemption within them. (#4418, @mimowo)
- TAS: Support preemption within ClusterQueue (#4171, @mimowo)
- TopologyAwareScheduling can now be used with `kueue.x-k8s.io/podset-unconstrained-topology` annotation (#4567, @PBundyra)
- TopologyAwareScheduling can now be used with profiles based on feature gates. TopologyAwareScheduling has now a new algorithm LeastFreeCapacityFit (#4576, @PBundyra)
- Upgrade Kuberay to v1.3.1. (#4568, @mszadkow)
- Use TAS for workloads implicitly (without TAS annotations) when the target CQ is TAS-only, meaning
  all the resource flavors have spec.topologyName specified. (#4519, @mimowo)
- WaitForPodsReady countdown is now measured since the admission of the workload instead of setting `PodsReady` condition to `False` (#4287, @PBundyra)
- [FSxHC] Add Cohort Fair Sharing Status and Metrics (#4561, @mbobrovskyi)
- [FSxHC] Extend Cohort API with Fair Sharing Configuration (#4288, @gabesaba)
- [FSxHC] Make Fair Sharing compatible with Hierarchical Cohorts during preemption (#4572, @gabesaba)
- [FSxHC] Make Fair Sharing compatible with Hierarchical Cohorts during scheduling (#4503, @gabesaba)

### Bug or Regression

- Add missing external types to apply configurations (#4191, @astefanutti)
- Align default value for `managedJobsNamespaceSelector` in helm chart and kustomize files. (#4262, @dgrove-oss)
- Disable the StatefulSet webhook in the kube-system and kueue-system namespaces by default.
  This aligns the default StatefulSet webhook configuration with the Pod and Deployment configurations. (#4121, @kannon92)
- Fix a bug is incorrect field path in inadmissible reasons and messages when Pod resources requests do not satisfy LimitRange constraints. (#4267, @tenzen-y)
- Fix a bug is incorrect field path in inadmissible reasons and messages when container requests exceed limits (#4216, @tenzen-y)
- Fix a bug that allowed unsupported changes to some PodSpec fields which were resulting in the StatefulSet getting stuck on Pods with schedulingGates.

  The validation blocks mutating the following Pod spec fields: `nodeSelector`, `affinity`, `tolerations`, `runtimeClassName`, `priority`, `topologySpreadConstraints`, `overhead`, `resourceClaims`, plus container (and init container) fields: `ports` and `resources.requests`.

  Mutating other fields, such as container image, command or args, remains allowed and supported. (#4081, @mbobrovskyi)
- Fix a bug that doesn't allow Kueue to delete Pods after a StatefulSet is deleted. (#4150, @mbobrovskyi)
- Fix a bug that occurs when a PodTemplate has not been created yet, but the Cluster Autoscaler attempts to process the ProvisioningRequest and marks it as failed. (#4086, @mbobrovskyi)
- Fix a bug that prevented tracking some of the controller-runtime metrics in Prometheus. (#4217, @tenzen-y)
- Fix a bug truncating AdmissionCheck condition message at `1024` characters when creation of the associated ProvisioningRequest or PodTemplate fails.
  Instead, use the `32*1024` characters limit as for condition messages. (#4190, @mbobrovskyi)
- Fix building TAS assignments for workloads with multiple PodSets (eg. JobSet or kubeflow Jobs). The assignment was computed independently for the PodSets which could result in conflicts rendering the pods unschedulable by the kube-scheduler. (#3937, @kerthcet)
- Fix populating the LocalQueue metrics: `kueue_local_queue_resource_usage` and `kueue_local_queue_resource_reservation`. (#3988, @mykysha)
- Fix the bug that prevented Kueue from updating the AdmissionCheck state in the Workload status on a ProvisioningRequest creation error. (#4114, @mbobrovskyi)
- Fix the bug that prevented scaling StatefulSets which aren't managed by Kueue when the "statefulset" integration is enabled. (#3991, @mbobrovskyi)
- Fix the permission bug which prevented adding the `kueue.x-k8s.io/resource-in-use` finalizer to the Topology objects, resulting in repeatedly logged errors. (#3910, @kerthcet)
- Fixes a bug in 0.10.0 which resulted in the kueue manager configuration not being logged. (#3876, @dgrove-oss)
- Fixes a bug that would result in default values not being properly set on creation for enabled integrations whose API was not available when the Kueue controller started. (#4547, @dgrove-oss)
- Helm: Fix the unspecified LeaderElection Role and Rolebinding namespaces (#4383, @eric-higgins-ai)
- Helm: Fixed a bug that prometheus namespace is enforced with namespace the same as kueue-controller-manager (#4484, @kannon92)
- Improve error message in the event when scheduling for TAS workload fails due to unassigned flavors. (#4204, @mimowo)
- Propagate the top-level setting of the `kueue.x-k8s.io/priority-class` label to the PodTemplate for
  Deployments and StatefulSets. This way the Workload Priority class is no longer ignored by the workloads. (#3980, @Abirdcfly)
- TAS: Do not ignore the TAS annotation if set on the template for the Ray submitter Job. (#4341, @mszadkow)
- TAS: Fix a bug that TopolologyUngator cound not be triggered the leader change when enabled HA mode (#4653, @tenzen-y)
- TAS: Fix a bug that incorrect topologies are assigned to Workloads when topology has insufficient allocatable Pods count (#4271, @tenzen-y)
- TAS: Fix a bug that unschedulable nodes (".spec.unschedulable=true") are counted as allocatable capacities (#4181, @tenzen-y)
- TAS: Fixed a bug that allows to create a JobSet with both kueue.x-k8s.io/podset-required-topology and kueue.x-k8s.io/podset-preferred-topology annotations set on the PodTemplate. (#4130, @mbobrovskyi)
- Update FairSharing to be incompatible with ClusterQueue.Preemption.BorrowWithinCohort. Using these parameters together is a no-op, and will be validated against in future releases. This change fixes an edge case which triggered an infinite preemption loop when these two parameters were combined. (#4165, @gabesaba)

### Other (Cleanup or Flake)

- MultiKueue: Do not update the status of the Job on the management cluster while the Job is suspended. This is updated  for jobs represented by JobSet, Kubeflow Jobs and MPIJob. (#4070, @IrvingMg)
- Promote WorkloadResourceRequestsSummary feature gate to stable (#4166, @dgrove-oss)
- Publish helm charts to the Kueue staging repository `http://us-central1-docker.pkg.dev/k8s-staging-images/kueue/charts`,
  so that they can be promoted to the permanent location under `registry.k8s.io/kueue/charts`. (#4680, @mimowo)
- Remove the support for Kubeflow MXJob. (#4077, @mszadkow)
- Renamed Log key from "attemptCount" to "schedulingCycleCount". This key tracks how many scheduling cycles we have done since starting Kueue. (#4108, @gabesaba)
- Support for Kubernetes 1.32 (#3820, @mbobrovskyi)
- The APF configuration, using v1beta3 API dedicated for Kubernetes 1.28 (or older), for the visibility server is no longer part of the release artifacts of Kueue. (#3983, @mbobrovskyi)
