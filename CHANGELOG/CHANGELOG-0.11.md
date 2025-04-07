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

Changes since `v0.11.1`:

## Changes by Kind

### Bug or Regression

- Fix bug which resulted in under-utilization of the resources in a Cohort.
  Now, when a ClusterQueue is configured with `preemption.reclaimWithinCohort: Any`,
  its resources can be lent out more freely, as we are certain that we can reclaim
  them later. Please see PR for detailed description of scenario. (#4822, @gabesaba)
- PodSetTopologyRequests are now configured only when TopologyAwareScheduling feature gate is enabled. (#4797, @mykysha)

## v0.11.1

Changes since `v0.11.0`:

## Changes by Kind

### Bug or Regression

- Add Necessary RBAC to Update Cohort Status (#4723, @gabesaba)
- Fixed bug that doesn't allow to use WorkloadPriorityClass on LeaderWorkerSet. (#4725, @mbobrovskyi)

## v0.11.0

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
