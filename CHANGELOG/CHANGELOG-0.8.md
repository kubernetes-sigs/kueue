## v0.8.4

Changes since `v0.8.3`:

## Changes by Kind

### Bug or Regression

- Change, and in some scenarios fix, the status message displayed to user when a workload doesn't fit in available capacity (#3551, @gabesaba)
- Determine borrowing more accurately, allowing preempting workloads which fit in nominal quota to schedule faster (#3551, @gabesaba)

## v0.8.3

Changes since `v0.8.2`:

## Changes by Kind

### Bug or Regression

- Workload is requeued with all AdmissionChecks set to Pending if there was an AdmissionCheck in Retry state. (#3323, @PBundyra)
- Account for NumOfHosts when calculating PodSet assignments for RayJob and RayCluster (#3384, @andrewsykim)

## v0.8.2

Changes since `v0.8.1`:

### Feature

- Helm: Support the topologySpreadConstraints and PodDisruptionBudget (#3282, @woehrl01)

### Bug or Regression

- Fix a bug that could delay the election of a new leader in the Kueue with multiple replicas env. (#3096, @tenzen-y)
- Fix resource consumption computation for partially admitted workloads. (#3206, @trasc)
- Fix restoring parallelism on eviction for partially admitted batch/Jobs. (#3208, @trasc)
- Fix some scenarios for partial admission which are affected by wrong calculation of resources
  used by the incoming workload which is partially admitted and preempting. (#3205, @trasc)
- Fix webook validation for batch/Job to allow partial admission of a Job to use all available resources.
  It also fixes a scenario of partial re-admission when some of the Pods are already reclaimed. (#3207, @trasc)
- Prevent job webhooks from dropping fields for newer API fields when Kueue libraries are behind the latest released CRDs. (#3358, @mbobrovskyi)
- RayJob's implementation of Finished() now inspects at JobDeploymentStatus (#3128, @andrewsykim)

### Other (Cleanup or Flake)

- Add a jobframework.BaseWebhook that can be used for custom job integrations (#3355, @mbobrovskyi)

## v0.8.1

Changes since `v0.8.0`:

### Feature

- Add gauge metric admission_cycle_preemption_skips that reports the number of Workloads in a ClusterQueue
  that got preemptions candidates, but had to be skipped in the last cycle. (#2942, @alculquicondor)
- Publish images via artifact registry (#2832, @alculquicondor)

### Bug or Regression

- CLI: Support `-` and `.` in the resource flavor name on `create cq` (#2706, @trasc)
- Detect and enable support for job CRDs installed after Kueue starts. (#2991, @ChristianZaccaria)
- Fix over-admission after deleting resources from borrowing ClusterQueue. (#2879, @mbobrovskyi)
- Fix support for kuberay 1.2.x (#2983, @mbobrovskyi)
- Helm: Fix a bug for "unclosed action error". (#2688, @mbobrovskyi)
- Prevent infinite preemption loop when PrioritySortingWithinCohort=false
  is used together with borrowWithinCohort. (#2831, @mimowo)
- Support for helm charts in the us-central1-docker.pkg.dev/k8s-staging-images/charts repository (#2834, @IrvingMg)
- Update Flavor selection logic to prefer Flavors which allow reclamation of lent nominal quota, over Flavors which require preempting workloads within the ClusterQueue. This matches the behavior in the single Flavor case. (#2829, @gabesaba)

## v0.8.0

Changes since `v0.7.0`:

### Urgent Upgrade Notes 

#### (No, really, you MUST read this before you upgrade)

- Use a single rate limiter for all API types clients.
  
  Consider adjusting `clientConnection.qps` and `clientConnection.burst` if you observe any performance degradation. (#2462, @trasc)
 
### Feature

- Add a column to workload indicating if it is finished (#2615, @highpon)
- Add preempted_workloads_total metric that tracks the number of preemptions issued by a ClusterQueue) (#2538, @vladikkuzn)
- Add the following events for eviction on the workload indicating the reason for eviction:
  - "EvictedDueToPodsReadyTimeout"
  - "EvictedDueToAdmissionCheck"
  - "EvictedDueToClusterQueueStopped"
  - "EvictedDueToInactiveWorkload" (renamed from InactiveWorkload)
  
  If you were watching for the typed Normal event with `InactiveWorkload` reason, use `EvictedDueToInactiveWorkload` reason one instead. (#2376, @mbobrovskyi)
- AdmissionChecks: A workload with a Rejected AdmissionCheck gets deactivated (#2363, @PBundyra)
- Allow stoping admission from a specific LocalQueue. (#2173, @mbobrovskyi)
- Allow usage of the pod integration for pods belonging to jobs that Kueue supports, if the support for the job type is explicitly disabled (#2493, @trasc)
- CLI: Add stop/resume localqueue commands (#2415, @rainfd)
- CLI: Added Node Labels column on resource flavor list. (#2557, @mbobrovskyi)
- CLI: Added create resourceflavor command. (#2517, @mbobrovskyi)
- CLI: Added list resourceflavor command. (#2525, @mbobrovskyi)
- CLI: Added resourceflavor to pass-through commands. (#2518, @mbobrovskyi)
- CLI: Added version command. (#2346, @mbobrovskyi)
- CLI: Adds `for` filter to list workloads. (#2238, @IrvingMg)
- CLI: Adds create clusterqueue command. (#2201, @IrvingMg)
- CLI: Support autocompletion (#2314, @mbobrovskyi)
- CLI: Support paging on kueue CLI list commands. (#2313, @mbobrovskyi)
- CLI: kubectl-kueue tar.gz archives is part of the release artifacts. (#2513, @mbobrovskyi)
- Do not start Kueue when the visibility server cannot be started, but is requested. (#2636, @mbobrovskyi)
- Experimental support for helm charts in the gcr.io/k8s-staging-kueue/charts/kueue repository (#2377, @IrvingMg)
- Improved logging for scheduling and preemption in levels 4 and 5 (#2504, @alculquicondor)
- Introduce the MultiplePreemptions flag, which allows more than one
  preemption to occur in the same scheduling cycle, even with overlapping
  FlavorResources (#2641, @gabesaba)
- More granular Preemption condition reasons: PriorityReclamation, InCohortReclamation, InCohortFairSharing, InCohortReclaimWhileBorrowing (#2411, @vladikkuzn)
- MultiKueue: Allow for defaulting of the spec.managedBy field for Jobs managed by MultiKueue.
  The defaulting is enabled by the MultiKueueBatchJobWithManagedBy feature-gate. (#2401, @vladikkuzn)
- MultiKueue: Remove remote objects synchronously when the worker cluster is reachable. (#2347, @trasc)
- MultiKueue: Use batch/Job `spec.managedBy` field (#2331, @trasc)
- Multikueue: Batch reconcile events for remote workloads. (#2380, @trasc)
- ProvisioningRequest: Support for ProvisioningRequest's condition `BookingExpired` (#2445, @PBundyra)
- ProvisioningRequets: Support for ProvisioningRequest's condition `CapacityRevoked`. ProvisioningRequests objects persist until the corresponding Job or the Workload is deleted (#2196, @PBundyra)

### Documentation

- Added details documentation for kubectl-kueue plugin. (#2613, @mbobrovskyi)
- Improve the documentation for the waitForPodsReady (#2541, @mimowo)

### Bug or Regression

- Added raycluster roles to manifests.yaml (#2618, @mbobrovskyi)
- CLI: Fixed no Auth Provider found for name "oidc" error. (#2602, @Kavinraja-G)
- Fix check that prevents preemptions when a workload requests 0 for a resource that is at nominal or over it. (#2520, @alculquicondor)
- Fix for the scenario when a workload doesn't match some resource flavors due to affinity or taints
  could cause the workload to be continuously retried. (#2407, @KunWuLuan)
- Fix missing fairSharingStatus in ClusterQueue (#2424, @mbobrovskyi)
- Fix missing metric cluster_queue_status (#2474, @mbobrovskyi)
- Fix panic that could occur when a ClusterQueue is deleted while Kueue was updating the ClusterQueue status. (#2461, @mbobrovskyi)
- Fix panic when there is not enough quota to assign flavors to a Workload in the cohort, when FairSharing is enabled. (#2439, @mbobrovskyi)
- Fix performance issue in logging when processing LocalQueues. (#2485, @alexandear)
- Fix race condition on delete workload from queue manager. (#2460, @mbobrovskyi)
- Fix race condition on requeue workload. (#2509, @mbobrovskyi)
- Fix race condition on run garbage collection in multikueuecluster reconciler. (#2479, @mbobrovskyi)
- Fix the validation messages, to report the new value rather than old, for the following immutable labels: `kueue.x-k8s.io/queue-name`, `kueue.x-k8s.io/prebuilt-workload-name`, and `kueue.x-k8s.io/priority-class`. (#2544, @xuxianzhang)
- Fixed issue that prevented restoring the startTime and pod template when evicting a batch/v1 Job, if any API errors happened in the process (#2567, @mbobrovskyi)
- MultiKueue: Do not reject a JobSet if the corresponding cluster queue doesn't exist (#2425, @vladikkuzn)
- MultiKueue: Skip garbage collection for disconnected clients which could occasionally result in panic. (#2369, @trasc)
- Show weightedShare in ClusterQueue status.fairSharing even if the value is zero (#2521, @alculquicondor)
- Skip duplicate Tolerations when an admission check introduces a toleration that the job also set. (#2498, @trasc)

### Other (Cleanup or Flake)

- Importer: corrects the field name `observedFirstIn` in logs. (#2500, @alexandear)
- Use Patch instead of Update on jobframework multikueue adapters to prevent the risk of dropping fields. (#2590, @mbobrovskyi)
- Use Patch instead of Update on jobframework to prevent the risk of dropping fields. (#2553, @mbobrovskyi)
