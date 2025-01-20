## v0.10.1

Changes since `v0.10.0`:

## Changes by Kind

### Bug or Regression

- Disable the unnecessary Validating Admission Policy for the visibility server, and drop the associated RBAC permissions to make the server minimal. This also prevents periodic error logging on clusters above Kubernetes 1.29+. (#3946, @varshaprasad96)
- Fix building TAS assignments for workloads with multiple PodSets (eg. JobSet or kubeflow Jobs). The assignment was computed independently for the PodSets which could result in conflicts rendering the pods unschedulable by the kube-scheduler. (#3970, @kerthcet)
- Fix populating the LocalQueue metrics: `kueue_local_queue_resource_usage` and `kueue_local_queue_resource_reservation`. (#3990, @mykysha)
- Fix the bug that prevented scaling StatefulSets which aren't managed by Kueue when the "statefulset" integration is enabled. (#3998, @mbobrovskyi)
- Fix the permission bug which prevented adding the `kueue.x-k8s.io/resource-in-use` finalizer to the Topology objects, resulting in repeatedly logged errors. (#3911, @kerthcet)
- Fixes a bug in 0.10.0 which resulted in the kueue manager configuration not being logged. (#3877, @dgrove-oss)

## v0.10.0

Changes since `v0.9.0`:

## Urgent Upgrade Notes

### (No, really, you MUST read this before you upgrade)

- PodSets for RayJobs now account for submitter Job when spec.submissionMode=k8sJob is used.

  if you used the RayJob integration you may need to revisit your quota settings,
  because now Kueue accounts for the resources required by the KubeRay submitter Job
  when the spec.submissionMode=k8sJob (by default 500m CPU and 200Mi memory) (#3729, @andrewsykim)
 - Removed the v1alpha1 Visibility API.

  The v1alpha1 Visibility API is deprecated. Please use v1beta1 instead. (#3499, @mbobrovskyi)
 - The InactiveWorkload reason for the Evicted condition is renamed to Deactivated.
  Also, the reasons for more detailed situations are renamed:
    - InactiveWorkloadAdmissionCheck -> DeactivatedDueToAdmissionCheck
    - InactiveWorkloadRequeuingLimitExceeded -> DeactivatedDueToRequeuingLimitExceeded

  If you were watching for the "InactiveWorkload" reason in the "Evicted" condition, you need
  to start watching for the "Deactivated" reason. (#3593, @mbobrovskyi)

## Changes by Kind

### Feature

- Adds a managedJobsNamespaceSelector to the Kueue configuration that enables namespace-based control of whether Jobs submitted without a `kueue.x-k8s.io/queue-name` label are managed by Kueue for all supported Job Kinds. (#3712, @dgrove-oss)
- Allow mutating the queue-name label for non-running Deployments. (#3528, @mbobrovskyi)
- Allowed StatefulSet scaling down to zero and scale up from zero. (#3487, @mbobrovskyi)
- Extend the GenericJob interface to allow implementations of custom Job CRDs to use
  Topology-Aware Scheduling with rank-based ordering. (#3704, @PBundyra)
- Introduce alpha feature, behind the LocalQueueMetrics feature gate, which allows users to get the prometheus LocalQueues metrics:
  local_queue_pending_workloads
  local_queue_quota_reserved_workloads_total
  local_queue_quota_reserved_wait_time_seconds
  local_queue_admitted_workloads_total
  local_queue_admission_wait_time_seconds
  local_queue_admission_checks_wait_time_seconds
  local_queue_evicted_workloads_total
  local_queue_reserving_active_workloads
  local_queue_admitted_active_workloads
  local_queue_status
  local_queue_resource_reservation
  local_queue_resource_usage (#3673, @KPostOffice)
- Introduce the LocalQueue defaulting, enabled by the LocalQueueDefaulting feature gate.
  When a new workload is created without the "queue-name" label,  and the LocalQueue
  with name "default" name exists in the workload's namespace, then the value of the
  "queue-name" is defaulted to "default". (#3610, @yaroslava-serdiuk)
- Kueue-viz: A Dashboard for kueue (#3727, @akram)
- Optimize the size of the Workload object when Topology-Aware Scheduling is used, and the
  `kubernetes.io/hostname` is defined as the lowest Topology level. In that case the `TopologyAssignment`
  in the Workload's Status contains value only for this label, rather than for all levels defined. (#3677, @PBundyra)
- Promote MultiplePreemptions feature gate to stable, and drop the legacy preemption logic. (#3602, @gabesaba)
- Promoted ConfigurableResourceTransformations and WorkloadResourceRequestsSummary to Beta and enabled by default. (#3616, @dgrove-oss)
- ResourceFlavorSpec that defines topologyName is not immutable (#3738, @PBundyra)
- Respect node taints in Topology-Aware Scheduling when the lowest topology level is kubernetes.io/hostname. (#3678, @mimowo)
- Support `.featureGates` field in the configuration API to enable and disable the Kueue features (#3805, @kannon92)
- Support rank-based ordering of Pods with Topology-Aware Scheduling.
  The Pod indexes are determined based on the "kueue.x-k8s.io/pod-group-index" label which
  can be set by an external controller managing the group. (#3649, @PBundyra)
- TAS: Support rank-based ordering for StatefulSet. (#3751, @mbobrovskyi)
- TAS: The CQ referencing a Topology is deactivated if the topology does not exist. (#3770, @mimowo)
- TAS: support rank-based ordering for JobSet (#3591, @mimowo)
- TAS: support rank-based ordering for Kubeflow (#3604, @mbobrovskyi)
- TAS: support rank-ordering of Pods for the Kubernetes batch Job. (#3539, @mimowo)
- TAS: validate that kubernetes.io/hostname can only be at the lowest level (#3714, @mbobrovskyi)

### Bug or Regression

- Added validation for Deployment queue-name to fail fast (#3555, @mbobrovskyi)
- Added validation for StatefulSet queue-name to fail fast. (#3575, @mbobrovskyi)
- Change, and in some scenarios fix, the status message displayed to user when a workload doesn't fit in available capacity. (#3536, @gabesaba)
- Determine borrowing more accurately, allowing preempting workloads which fit in nominal quota to schedule faster (#3547, @gabesaba)
- Fix Kueue crashing when the node for an admitted workload is deleted. (#3715, @mimowo)
- Fix a bug which occasionally prevented updates to the PodTemplate of the Job on the management cluster
  when starting a Job (e.g. updating nodeSelectors), when using `MultiKueueBatchJobWithManagedBy` enabled. (#3685, @IrvingMg)
- Fix accounting for usage coming from TAS workloads using multiple resources. The usage was multiplied
  by the number of resources requested by a workload, which could result in under-utilization of the cluster.
  It also manifested itself in the message in the workload status which could contain negative numbers. (#3490, @mimowo)
- Fix computing the topology assignment for workloads using multiple PodSets requesting the same
  topology. In particular, it was possible for the set of topology domains in the assignment to be empty,
  and as a consequence the pods would remain gated forever as the TopologyUngater would not have
  topology assignment information. (#3514, @mimowo)
- Fix dropping of reconcile requests for non-leading replica, which was resulting in workloads
  getting stuck pending after the rolling restart of Kueue. (#3612, @mimowo)
- Fix memory leak due to workload entries left in MultiKueue cache. The leak affects the 0.9.0 and 0.9.1
  releases which enable MultiKueue by default, even if MultiKueue is not explicitly used on the cluster. (#3835, @mimowo)
- Fix misleading log messages from workload_controller indicating not existing LocalQueue or
  Cluster Queue. For example "LocalQueue for workload didn't exist or not active; ignored for now"
  could also be logged the ClusterQueue does not exist. (#3605, @7h3-3mp7y-m4n)
- Fix preemption when using Hierarchical Cohorts by considering as preemption candidates workloads
  from ClusterQueues located further in the hierarchy tree than direct siblings. (#3691, @gabesaba)
- Fix running Job when parallelism < completions, before the fix the replacement pods for the successfully
  completed Pods were not ungated. (#3559, @mimowo)
- Fix scheduling in TAS by considering tolerations specified in the ResourceFlavor. (#3723, @mimowo)
- Fix scheduling of workload which does not include the toleration for the taint in ResourceFlavor's spec.nodeTaints,
  if the toleration is specified on the ResourceFlavor itself. (#3722, @PBundyra)
- Fix the bug which prevented the use of MultiKueue if there is a CRD which is not installed
  and removed from the list of enabled integrations. (#3603, @mszadkow)
- Fix the flow of deactivation for workloads due to rejected AdmissionChecks.
  Now, all AdmissionChecks are reset back to the Pending state on eviction (and deactivation in particular),
  and so an admin can easily re-activate such a workload manually without tweaking the checks. (#3350, @KPostOffice)
- Fixed rolling update for StatefulSet integration (#3684, @mbobrovskyi)
- Make topology levels immutable to prevent issues with inconsistent state of the TAS cache. (#3641, @mbobrovskyi)
- TAS: Fixed bug that doesn't allow to update cache on delete Topology. (#3615, @mbobrovskyi)

### Other (Cleanup or Flake)

- Eliminate webhook validation in case Pod integration is used on 1.26 or earlier versions of Kubernetes. (#3247, @vladikkuzn)
- Replace deprecated gcr.io/kubebuilder/kube-rbac-proxy with registry.k8s.io/kubebuilder/kube-rbac-proxy. (#3747, @mbobrovskyi)
