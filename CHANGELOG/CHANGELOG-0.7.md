## v0.7.1

Changes since `v0.7.0`:

### Feature

- Improved logging for scheduling and preemption in levels 4 and 5 (#2510, @gabesaba, @alculquicondor)
- MultiKueue: Remove remote objects synchronously when the worker cluster is reachable. (#2360, @trasc)

### Bug or Regression

- Fix check that prevents preemptions when a workload requests 0 for a resource that is at nominal or over it. (#2524, @mbobrovskyi, @alculquicondor)
- Fix for the scenario when a workload doesn't match some resource flavors due to affinity or taints
  could cause the workload to be continuously retried. (#2440, @KunWuLuan)
- Fix missing fairSharingStatus in ClusterQueue (#2432, @mbobrovskyi)
- Fix missing metric cluster_queue_status. (#2475, @mbobrovskyi)
- Fix panic that could occur when a ClusterQueue is deleted while Kueue was updating the ClusterQueue status. (#2464, @mbobrovskyi)
- Fix panic when there is not enough quota to assign flavors to a Workload in the cohort, when FairSharing is enabled. (#2449, @mbobrovskyi)
- Fix performance issue in logging when processing LocalQueues. (#2492, @alexandear)
- Fix race condition on delete workload from queue manager. (#2465, @mbobrovskyi)
- MultiKueue: Do not reject a JobSet if the corresponding cluster queue doesn't exist (#2442, @vladikkuzn)
- MultiKueue: Skip garbage collection for disconnected clients which could occasionally result in panic. (#2370, @trasc)
- Show weightedShare in ClusterQueue status.fairSharing even if the value is zero (#2522, @alculquicondor)
- Skip duplicate Tolerations when an admission check introduces a toleration that the job also set. (#2499, @trasc)

## v0.7.0

Changes since `v0.6.0`:

### Urgent Upgrade Notes 

#### (No, really, you MUST read this before you upgrade)

- Added CRD validation rules to AdmissionCheck.
  
  Requires Kubernetes 1.25 or newer (#1975, @IrvingMg)
- Added CRD validation rules to ClusterQueue.
  
  Requires Kubernetes 1.25 or newer (#1972, @IrvingMg)
- Added CRD validation rules to LocalQueue.
  
  Requires Kubernetes 1.25 or newer (#1938, @IrvingMg)
- Added CRD validation rules to ResourceFlavor.
  
  Requires Kubernetes 1.25 or newer (#1958, @IrvingMg)
- Added CRD validation rules to Workload.
  
  Requires Kubernetes 1.25 or newer (#2008, @IrvingMg)
- Increased the default value in the `.waitForPodsReady.requeuingStrategy.backoffBaseSeconds` to 60
  
  You can configure `.waitForPodsReady.requeuingStrategy.backoffBaseSeconds` as needed. (#2251, @mbobrovskyi)
- Upgrade RayJob API to v1
  
  If you use KubeRay older than v1.0.0, you'll have to upgrade your existing installation
  to KubeRay v1.0.0, or any more recent version, that supports KubeRay v1 APIs, for it to
  remain compatible with Kueue. (#1802, @astefanutti)
- When using admission checks, and they are not satisfied yet, the reason for the Admission condition with status=False is now
  `UnsatisfiedChecks`
  
  If you were watching for the reason `NoChecks` in the Admitted condition, use `UnsatisfiedChecks` instead. (#2150, @trasc)
 
## Changes by Kind

### API Change

- Make ClusterQueue queueingStrategy field mutable. The field can be mutated while there are pending workloads. (#1934, @mimowo)
- User can now pass parameters to ProvisioningRequest using job's annotations (#1869, @PBundyra)

### Feature

- A new condition with type Preempted allows to distinguish different reasons for the preemption to happen (#1942, @mimowo)
- Add configuration to register Kinds as being managed by an external Kueue-compatible controller (#2059, @dgrove-oss)
- Add fair sharing when borrowing unused resources from other ClusterQueues in a cohort.
  
  Fair sharing is based on DRF for usage above nominal quotas.
  When fair sharing is enabled, Kueue prefers to admit workloads from ClusterQueues with the lowest share first.
  Administrators can enable and configure fair sharing preemption using a combination of two policies: `LessThanOrEqualtoFinalShare`, `LessThanInitialShare`.
  
  You can define a fair sharing `weight` for ClusterQueues. The weight determines how much of the unused resources each ClusterQueue can take in comparison to others. (#2070, @alculquicondor)
- Add metric `evicted_workloads`: the number of evicted workloads per 'cluster_queue' (#1955, @lowang-bh)
- Add recommended Kubernetes labels to uniquely identify Pods and other resources installed with Kueue.
  The Deployment selector remains unchanged to allow for a seamless upgrade. (#1695, @astefanutti)
- Added label copying from Pod/Job into the Kueue Workload. (#1959, @pajakd)
- Added non-negative validations for the ".queueVisibility.clusterQueues.maxCount" in the Configuration. (#2309, @tenzen-y)
- Added validations for the ".internalCertManagement" in the Configuration. (#2169, @tenzen-y)
- Added validations for the "multiKueue.origin", ".multiKueue.gcInterval" and the "multiKueue.workerLostTimeout" in the Configuration. (#2129, @tenzen-y)
- Added validations for the "waitForPodsReady.timeout" in the Configuration. (#2214, @tenzen-y)
- Adds ObservedGeneration in conditions (#1939, @vladikkuzn)
- Adds the `BackoffMaxSeconds` property to limit the retry period length for re-queing workloads. (#2264, @IrvingMg)
- Allow for `workload.spec.podSet.[*].count` to be 0 (#2268, @mszadkow)
- CLI: Add command to list ClusterQueues (#2156, @vladikkuzn)
- CLI: Add commands to stop and Resume a ClusterQueue (#2200, @vladikkuzn)
- CLI: Add kubectl kueue plugin that allows to create LocalQueues without writing yamls. (#2027, @mbobrovskyi)
- CLI: Add list LocalQueue command (#2157, @mbobrovskyi)
- CLI: Add stop/resume workload commands (#2134, @mbobrovskyi)
- CLI: Add validation for ClusterQueue on creating LocalQueue (#2122, @mbobrovskyi)
- CLI: Added list workloads command. (#2195, @mbobrovskyi)
- CLI: Added pass-through commands support in `kubectl-kueue` for `get`, `describe`, `edit`, `patch` and `delete`. (#2181, @trasc)
- CLI: kubectl-kueue is part of the release artifacts (#2306, @mbobrovskyi)
- Helm: Allow configuration of `ipFamilyPolicy` for ipDualStack kubernetes cluster (#1933, @dongjiang1989)
- Helm: Allow configuration of custom annotations on Service and Deployment's Pod (#2030, @tozastation)
- Improve metrics related to workload's quota reservation and admission:
  - fix admission_wait_time_seconds - to measure the time to "Admitted" condition since creation time or last requeue (as opposed to the "QuotaReserved" condition as before)
  - add quota_reserved_wait_time_seconds - measures time to "QuotaReserved" condition since creation time, or last eviction time
  - add quota_reserved_workloads_total - counts the number of workloads that got admitted
  - admission_checks_wait_time_seconds - measures the time to admit a workload with admission checks since quota reservation
  - use longer buckets (up to 10240s) for histogram metrics: admission_wait_time_seconds, quota_reserved_wait_time_seconds, admission_checks_wait_time_seconds (#1977, @mbobrovskyi)
- Improve the kubectl output for workloads using admission checks. (#1991, @vladikkuzn)
- Make the PodsReady base delay for requeuing configurable (#2040, @mimowo)
- MuliKueue: Manage worker cluster unavailability (#1681, @trasc)
- MultiKueue: Add support for  JobSet  `spec.managedBy` field (#1870, @trasc)
- MultiKueue: Add the `managedBy` field to JobSets assigned to a ClusterQueue configured for MultiKueue (#2048, @vladikkuzn)
- MultiKueue: Add worker connection monitoring and reconnect (#1806, @trasc)
- Pod Integration: Add condition WaitingForReplacementPods to Workloads of pod groups with incomplete number of pods (#2234, @mbobrovskyi)
- Pod Integration: Improve performance (#1952, @gabesaba)
- Pod Integration: The reason for stopping a pod is now specified in the pod `TerminationTarget` condition (#2160, @pajakd)
- Pods created by Kueue have now the ProvisioningRequest's classname annotation (#2052, @PBundyra)
- ProvisioningRequest: Graduated to Beta and enabled by default (#1968, @pajakd)
- ProvisioningRequest: Propagate the message for a ProvisioningRequest being provisioned (which might include an ETA, depending on the implementation) to the Workload status (#2007, @pajakd)
- Show fair share of a CQ in status and a metric (#2276, @mbobrovskyi)
- Updates in admission check messages are recorded as events for jobs/pods. (#2147, @pajakd)
- Workload finished reason replaced with succeeded and failed reasons (#2026, @vladikkuzn)
- You can configure Kueue to ignore container resources that match specified prefixes. (#2267, @pajakd)
- You can define AdmissionChecks per ResourceFlavor in the ClusterQueue API, using `admissionChecksStrategy` (#1960, @PBundyra)

### Bug or Regression

- Avoid unnecessary preemptions when there are multiple candidates for preemption with the same admission timestamp (#1875, @alculquicondor)
- Change the default pprof port to 8083 to fix a bug that causes conflicting listening ports between pprof and the visibility server. (#2228, @amy)
- Check the containers limits for used resources in provisioning admission check controller and include them in the ProvisioningRequest as requests (#2286, @trasc)
- Do not default to suspending a job whose parent is already managed by Kueue (#1846, @astefanutti)
- Fix handling of eviction in StrictFIFO to ensure the evicted workload is in the head.
  Previously, in case of priority-based preemption, it was possible that the lower-priority
  workload might get admitted while the higher priority workload is being evicted. (#2061, @mimowo)
- Fix incorrect quota management when lendingLimit enabled in preemption (#1770, @kerthcet)
- Fix preemption algorithm to reduce the number of preemptions within a ClusterQueue when reclamation is not possible, and when using .preemption.borrowWithinCohort (#2110, @alculquicondor)
- Fix preemption algorithm to reduce the number of preemptions within a ClusterQueue when reclamation is not possible. (#1979, @mimowo)
- Fix preemption to reclaim quota that is blocked by an earlier pending Workload from another ClusterQueue in the same cohort. (#1866, @alculquicondor)
- Fix support for MPIJobs when using a ProvisioningRequest engine that applies updates only to worker templates. (#2265, @trasc)
- Fix the counter of pending workloads in cluster queue status. 
  
  The counter would not count the head workload for StrictFIFO queues, if the workload cannot get admitted.
  
  This change also includes the blocked workload in the metrics and the visibility API for the list of pending workloads. (#1936, @mimowo)
- Fix the resource requests computation taking into account sidecar containers. (#2099, @IrvingMg)
- Helm: Fix a bug that prevented Kueue to work with the cert-manager. (#2087, @EladDolev)
- Helm: Fix a bug where the configuration for `integrations.podOptions.namespaceSelector` didn't have an effect  due to indentation issues. (#2086, @EladDolev)
- Helm: Fix chart values configuration for the number of reconcilers for the Pod integration. (#2046, @alculquicondor)
- Kueue visibility API is no longer installed by default. Users can install it via helm or applying the visibility-api.yaml artifact. (#1746, @trasc)
- Make the defaults for PodsReadyTimeout backoff more practical, as for the original values
  the couple of first requeues made the impression as immediate on users (below 10s, which 
  is negligible to the wait time spent waiting for PodsReady). 
  
  The defaults values for the formula to determine the exponential back are changed as follows:
  - base `1s -> 10s`
  - exponent: `1.41284738 -> 2`
  So, now the consecutive times to requeue a workload are: 10s, 20s, 40s, ... (#2025, @mimowo)
- MultiKueue: Do not default the managedBy field for the mirror copy of the Job on the worker cluster. (#2316, @mimowo)
- MultiKueue: Fix a bug that could delay the joining clusters when it's MultiKueueCluster is created. (#2165, @trasc)
- Pod Integration: Consider deleted pods without `spec.nodeName` inactive and subject for pod replacement. (#2212, @trasc)
- Pod Integration: Exclude Pod labels, preemptionPolicy and container images when determining whether pods in a pod group have the same shape. (#1758, @alculquicondor)
- Pod Integration: Finalize failed pods in a pod-group when replacement pods are created (#1766, @trasc)
- Pod Integration: Fix Pods in Pod groups stuck with finalizers when deleted immediately after Succeeded (#1905, @alculquicondor)
- Pod Integration: Fix the configuration for the number of reconcilers for the Pod integration, defaulting to 5 workers. Previously, it was only reconciling one group at a time. (#1835, @alculquicondor)
- Pod Integration: Prevent Pod from being deleted when admitted via ProvisioningRequest that has pod updates on tolerations (#2239, @vladikkuzn)
- Pod Integration: Use PATCH updates for pods. This fixes support for Pods when using the latest features in Kubernetes v1.29 (#2074, @mbobrovskyi)
- Reduce number of Workload reconciliations due to wrong equality check. (#1897, @gabesaba)
- WaitForPodsReady: Fix a bug that causes the reactivated Workload to be immediately deactivated even though it doesn't exceed the backoffLimit. (#2219, @tenzen-y)
- WaitForPodsReady: Fix a bug that the requeueState isn't reset. (#1838, @tenzen-y)
- WaitForPodsReady: clear RequeueAt when the workload backoff time is completed. (#2143, @mbobrovskyi)

### Other (Cleanup or Flake)

- Added scalability test for scheduling performance (#1931, @trasc)
- Avoid API calls for admission attempts when Workload already has condition Admitted=false (#1820, @alculquicondor)
- Correctly log workload status for workloads with quota reserved, but awaiting for admission checks. (#2062, @mimowo)
- Dropped the usage of `kueue.x-k8s.io/parent-workload` annotation  in favor of an object ownership based approach. (#1747, @trasc)
- Skip requeueing of Workloads when there is a status update for a ClusterQueue, saving on API calls for Workloads that were already attempted for admission. (#1822, @alculquicondor)
- The hash suffix of the workload's name are now influenced by the job's object UID. Recreated jobs with the same name and kind will use different workload names. (#1732, @trasc)
- Upgrade to Kubernetes v1.30 APIs (#2005, @trasc)