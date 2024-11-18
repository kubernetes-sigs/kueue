## v0.9.1

Changes since `v0.9.0`:

## Changes by Kind

### Bug or Regression

- Change, and in some scenarios fix, the status message displayed to user when a workload doesn't fit in available capacity. (#3549, @gabesaba)
- Determine borrowing more accurately, allowing preempting workloads which fit in nominal quota to schedule faster (#3550, @gabesaba)
- Fix accounting for usage coming from TAS workloads using multiple resources. The usage was multiplied
  by the number of resources requested by a workload, which could result in under-utilization of the cluster.
  It also manifested itself in the message in the workload status which could contain negative numbers. (#3513, @mimowo)
- Fix computing the topology assignment for workloads using multiple PodSets requesting the same
  topology. In particular, it was possible for the set of topology domains in the assignment to be empty,
  and as a consequence the pods would remain gated forever as the TopologyUngater would not have
  topology assignment information. (#3524, @mimowo)
- Fix running Job when parallelism < completions, before the fix the replacement pods for the successfully
  completed Pods were not ungated. (#3561, @mimowo)
- Fix the flow of deactivation for workloads due to rejected AdmissionChecks.
  Now, all AdmissionChecks are reset back to the Pending state on eviction (and deactivation in particular),
  and so an admin can easily re-activate such a workload manually without tweaking the checks. (#3518, @KPostOffice)

## v0.9.0

Changes since `v0.8.0`:

## Urgent Upgrade Notes

### (No, really, you MUST read this before you upgrade)

- Changed the `type` of `Pending` events, emitted when a Workload can't be admitted, from `Normal` to `Warning`.

  Update tools that process this event if they depend on the event `type`. (#3264, @kebe7jun)
 - Deprecated SingleInstanceInClusterQueue and FlavorIndependent status conditions.

  the Admission check status conditions "FlavorIndependent" and "SingleInstanceInClusterQueue" are no longer supported by default.
  If you were using any of these conditions for your external AdmissionCheck you need to enable the `AdmissionCheckValidationRules` feature gate.
  For the future releases you will need to provide validation by an external controller. (#3254, @mszadkow)
 - Promote MultiKueue API and feature gate to Beta. The MultiKueue feature gate is now beta and enabled by default.

  The MultiKueue specific types are now part of the Kueue's `v1beta1` API. `v1alpha` types are no longer supported. (#3230, @trasc)
 - Promoted VisibilityOnDemand to Beta and enabled by default.

  The v1alpha1 Visibility API is deprecated and will be removed in the next release. Please use v1beta1 instead. (#3008, @mbobrovskyi)
 - Provides more details on the reasons for ClusterQueues being inactive.
  If you were watching for the reason `CheckNotFoundOrInactive` in the ClusterQueue condition, watch `AdmissionCheckNotFound` and `AdmissionCheckInactive` instead. (#3127, @trasc)
 - The QueueVisibility feature and its corresponding API was deprecated.

  The QueueVisibility feature and its corresponding API was deprecated and will be removed in the v1beta2. Please use VisibilityOnDemand (https://kueue.sigs.k8s.io/docs/tasks/manage/monitor_pending_workloads/pending_workloads_on_demand/) instead. (#3110, @mbobrovskyi)

## Upgrading steps

### 1. Backup MultiKueue Resources (skip if you are not using MultiKueue):
```
kubectl get multikueueclusters.kueue.x-k8s.io,multikueueconfigs.kueue.x-k8s.io -A -o yaml > mk.yaml
```

### 2. Update apiVersion in Backup File (skip if you are not using MultiKueue):
Replace `v1alpha1` with `v1beta1` in `mk.yaml` for all resources:
```
sed -i -e 's/v1alpha1/v1beta1/g' mk.yaml
```

### 3. Delete old CRDs:
```
kubectl delete crd multikueueclusters.kueue.x-k8s.io
kubectl delete crd multikueueconfigs.kueue.x-k8s.io
```

### 4.Install Kueue v0.9.x:
Follow the instruction [here](https://kueue.sigs.k8s.io/docs/installation/#install-a-released-version) to install.

### 5. Restore MultiKueue Resources (skip if you are not using MultiKueue):
```
kubectl apply -f mk.yaml
```

## Changes by Kind

### Feature

- Add gauge metric admission_cycle_preemption_skips that reports the number of Workloads in a ClusterQueue
  that got preemptions candidates, but had to be skipped in the last cycle. (#2919, @alculquicondor)
- Add integration for Deployment, where each Pod is treated as a separate Workload. (#2813, @vladikkuzn)
- Add integration for StatefulSet where Pods are managed by the pod-group integration. (#3001, @vladikkuzn)
- Added FlowSchema and PriorityLevelConfiguration for Visibility API. (#3043, @mbobrovskyi)
- Added a new optional `resource.transformations` section to the `Configuration` API  that enables limited customization
  of how the resource requirements of a Workload are computed from the resource requests and limits of a Job. (#3026, @dgrove-oss)
- Added a way to specify dependencies between job integrations. (#2768, @trasc)
- Best effort support for scenarios when the Job is created at the same time as prebuilt workload or momentarily before the workload. In that case an error is logged to indicate that creating a Job before prebuilt-workload is outside of the intended use. (#3255, @mbobrovskyi)
- CLI: Added EXEC TIME column on kueuectl list workload command. (#2977, @mbobrovskyi)
- CLI: Added list pods for a job command. (#2280, @Kavinraja-G)
- CLI: Use protobuf encoding for core K8s APIs in kueuectl. (#3077, @tosi3k)
- Calculate AllocatableResourceGeneration more accurately. This fixes a bug where a workload might not have the Flavors it was assigned in a previous scheduling cycle invalidated, when the resources in the Cohort had changed. This bug could occur when other ClusterQueues were deleted from the Cohort. (#2984, @gabesaba)
- Detect and enable support for job CRDs installed after Kueue starts. (#2574, @ChristianZaccaria)
- Exposed available ResourceFlavors from the ClusterQueue in the LocalQueue status. (#3143, @mbobrovskyi)
- Graduated LendingLimit to Beta and enabled by default. (#2909, @macsko)
- Graduated MultiplePreemptions to Beta and enabled by default. (#2864, @macsko)
- Helm: Support the topologySpreadConstraints and PodDisruptionBudget (#3295, @woehrl01)
- Hierarchical Cohorts, introduced with the v1alpha1 Cohorts API, allow users to group resources in an arbitrary tree structure. Additionally, quotas and limits can now be defined directly at the Cohort level. See #79 for more details. (#2693, @gabesaba)
- Included visibility-api.yaml as a part of main.yaml (#3084, @mbobrovskyi)
- Introduce the "kueue.x-k8s.io/pod-group-fast-admission" annotation to Plain Pod integration.

  If the PlainPod has the annotation and is part of the Plain PodGroup, the Kueue will admit the Plain Pod regardless of whether all PodGroup Pods are created. (#3189, @vladikkuzn)
- Introduce the new PodTemplate annotation kueue.x-k8s.io/workload, and label kueue.x-k8s.io/podset.
  The annotation and label are alpha-level and gated by the new TopologyAwareScheduling feature gate. (#3228, @mimowo)
- Label `kueue.x-k8s.io/managed` is now added to PodTemplates created via ProvisioningRequest by Kueue (#2877, @PBundyra)
- MultiKueue: Add support for  MPIJob  `spec.runPolicy.managedBy` field (#3289, @mszadkow)
- MultiKueue: Support for the Kubeflow MPIJob (#2880, @mszadkow)
- MultiKueue: Support for the Kubeflow PaddleJob (#2744, @mszadkow)
- MultiKueue: Support for the Kubeflow PyTorchJob (#2735, @mszadkow)
- MultiKueue: Support for the Kubeflow TFJob (#2626, @mszadkow)
- MultiKueue: Support for the Kubeflow XGBoostJob (#2746, @mszadkow)
- ProvisioningRequest: Record the ProvisioningRequest creation errors to event and ProvisioningRequest status. (#3056, @IrvingMg)
- ProvisioningRequestConfig API has now `RetryStrategy` field that allows users to configure retries per ProvisioningRequest class. By default retry releases allocated quota in Kueue. (#3375, @PBundyra)
- Publish images via artifact registry (#2476, @IrvingMg)
- Support Topology Aware Scheduling (TAS) in Kueue in the Alpha version, along with the new Topology API
  to specify the ordered list of node labels corresponding to the different levels of hierarchy in data-centers
  (like racks or blocks).

  Additionally, we introduce the pair of Job-level annotations: `http://kueue.x-k8s.io/podset-required-topology`
  and `kueue.x-k8s.io/podset-preferred-topology` which users can use to indicate their preference for the
  Jobs to run all their Pods within a topology domain at the indicated level. (#3235, @mimowo)
- Support for JobSet 0.6 (#3034, @kannon92)
- Support for Kubernetes 1.31 (#2402, @mbobrovskyi)
- Support the Job-level API label, called `kueue.x-k8s.io/max-exec-time-seconds`, that users
  can use to enforce the maximum execution time for their job. The execution time is only
  accumulated when the Job is running (the corresponding Workload is admitted).
  The corresponding Workload is deactivated after the time is exceeded. (#3191, @trasc)

### Documentation

- Adds installing kubectl-kueue plugin via Krew guide. (#2666, @mbobrovskyi)
- Documentation on how to use Kueue for Deployments is added (#2698, @vladikkuzn)

### Bug or Regression

- CLI: Delete the corresponding Job when deleting a Workload. (#2992, @mbobrovskyi)
- CLI: Support `-` and `.` in the resource flavor name on `create cq` (#2703, @trasc)
- Fix a bug that could delay the election of a new leader in the Kueue with multiple replicas env. (#3093, @tenzen-y)
- Fix over-admission after deleting resources from borrowing ClusterQueue. (#2873, @mbobrovskyi)
- Fix resource consumption computation for partially admitted workloads. (#3118, @trasc)
- Fix restoring parallelism on eviction for partially admitted batch/Jobs. (#3153, @trasc)
- Fix some scenarios for partial admission which are affected by wrong calculation of resources
  used by the incoming workload which is partially admitted and preempting. (#2826, @trasc)
- Fix support for kuberay 1.2.x (#2960, @mbobrovskyi)
- Fix webook validation for batch/Job to allow partial admission of a Job to use all available resources.
  It also fixes a scenario of partial re-admission when some of the Pods are already reclaimed. (#3152, @trasc)
- Helm: Fix a bug for "unclosed action error". (#2683, @mbobrovskyi)
- Prevent infinite preemption loop when PrioritySortingWithinCohort=false
  is used together with borrowWithinCohort. (#2807, @mimowo)
- Prevent job webhooks from dropping fields for newer API fields when Kueue libraries are behind the latest released CRDs. (#3132, @alculquicondor)
- RayJob's implementation of Finished() now inspects at JobDeploymentStatus (#3120, @andrewsykim)
- Support for helm charts in the us-central1-docker.pkg.dev/k8s-staging-images/charts repository (#2680, @IrvingMg)
- Update Flavor selection logic to prefer Flavors which allow reclamation of lent nominal quota, over Flavors which require preempting workloads within the ClusterQueue. This matches the behavior in the single Flavor case. (#2811, @gabesaba)
- Workload is requeued with all AdmissionChecks set to Pending if there was an AdmissionCheck in Retry state. (#3323, @PBundyra)
- Account for NumOfHosts when calculating PodSet assignments for RayJob and RayCluster (#3384, @andrewsykim)

### Other (Cleanup or Flake)

- Add a jobframework.BaseWebhook that can be used for custom job integrations (#3102, @alculquicondor)
