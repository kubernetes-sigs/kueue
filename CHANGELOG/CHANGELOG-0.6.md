## v0.6.1

Changes Since `v0.6.0`:

### Feature

- Added MultiKueue worker connection monitoring and reconnect. (#1809, @trasc)
- The Failed pods in a pod-group are finalized once a replacement pods are created. (#1801, @trasc)

### Bug or Regression

- Exclude Pod labels, preemptionPolicy and container images when determining whether pods in a pod group have the same shape. (#1760, @alculquicondor)
- Fix incorrect quota management when lendingLimit enabled in preemption (#1826, @kerthcet, @B1F030)
- Fix the configuration for the number of reconcilers for the Pod integration. It was only reconciling one group at a time. (#1837, @alculquicondor)
- Kueue visibility API is no longer installed by default. Users can install it via helm or applying the visibility-api.yaml artifact. (#1764, @trasc)
- WaitForPodsReady: Fix a bug that the requeueState isn't reset. (#1843, @tenzen-y)

### Other (Cleanup or Flake)

- Avoid API calls for admission attempts when Workload already has condition Admitted=false (#1845, @alculquicondor)
- Skip requeueing of Workloads when there is a status update for a ClusterQueue, saving on API calls for Workloads that were already attempted for admission. (#1832, @alculquicondor)

## v0.6.0

Changes since `v0.5.0`:

### API Change

- A `stopPolicy` field in the ClusterQueue allows to hold or drain a ClusterQueue (#1299, @trasc)
- Add a lendingLimit field in ClusterQueue's quotas, to allow restricting how much of the unused resources by the ClusterQueue can be borrowed by other ClusterQueues in the cohort.
  In other words, this allows a quota equal to `nominal-lendingLimit` to be exclusively used by the ClusterQueue. (#1385, @B1F030)
- Add validation for clusterQueue: when cohort is empty, borrowingLimit must be nil. (#1525, @B1F030)
- Allow decrease reclaimable pods to 0 for suspended job (#1277, @yaroslava-serdiuk)
- MultiKueue: Add Path location type for cluster KubeConfigs. (#1640, @trasc)
- MultiKueue: Add garbage collection of deleted Workloads. (#1643, @trasc)
- MultiKueue: Multi cluster job dispatching for k8s Job. This doesn't include support for live status updates. (#1313, @trasc)
- Support for a mechanism to suspend a running Job without requeueing (#1252, @vicentefb)
- Support for preemption while borrowing (#1397, @mimowo)
- The leaderElection field in the Configuration API is now defaulted.
  Leader election is now enabled by default. (#1598, @astefanutti)
- Visibility API: Add an endpoint that allows a user to fetch information about pending workloads and their position in LocalQueue. (#1365, @PBundyra)
- Visibility API: Introduce an on-demand API endpoint for fetching pending workloads in a ClusterQueue. (#1251, @PBundyra)
- Visibility API: extend the information returned for the pending workloads in a ClusterQueue, including the workload position in the queue. (#1362, @PBundyra)
- WaitForPodsReady: Add a config field to allow admins to configure the timestamp used when sorting workloads that were evicted due to their Pods not becoming ready on time. (#1542, @nstogner)
- WaitForPodsReady: Support a backoff re-queueing mechanism with configurable limit. (#1709, @tenzen-y)

### Feature

- Add Prebuilt Workload support for JobSets. (#1575, @trasc)
- Add events for transitions of the provisioning AdmissionCheck (#1271, @stuton)
- Add prebuilt workload support for batch/job. (#1358, @trasc)
- Add support for groups of plain Pods. (#1319, @achernevskii)
- Allow configuring featureGates on helm charts. (#1314, @B1F030)
- At log level 6, the usage of ClusterQueues and cohorts is included in logs.
  
  The status of the internal cache and queues is also logged on demand when a SIGUSR2 is sent to kueue, regardless of the log level. (#1528, @alculquicondor)
- Changing tolerations in an inadmissible job triggers an admission retry with the updated tolerations. (#1304, @stuton)
- Increase the default number of reconcilers for Pod and Workload objects to 5, each. (#1589, @alculquicondor)
- Jobs preserve their position in the queue if the number of pods change before being admitted (#1223, @yaroslava-serdiuk)
- Make the image build setting CGO_ENABLED configurable (#1391, @anishasthana)
- MultiKueue: Add live status updates for multikueue JobSets (#1668, @trasc)
- MultiKueue: Support for JobSets. (#1606, @trasc)
- Support RayCluster as a queue-able workload in Kueue (#1520, @vicentefb)
- Support for retry of provisioning request.
  
  When `ProvisioningACC` is enabled, and there are existing ProvisioningRequests, they are going to be recreated.
  This may cause job evictions for some long-running jobs which were using the ProvisioningRequests. (#1351, @mimowo)
- The image gcr.io/k8s-staging-kueue/debug:main, along with the script ./hack/dump_cache.sh can be used to trigger a dump of the internal cache into the logs. (#1541, @alculquicondor)
- The priority sorting within the cohort could be disabled by setting the feature gate PrioritySortingWithinCohort to false (#1406, @yaroslava-serdiuk)
- Visibility API: Add HA support. (#1554, @astefanutti)

### Bug or Regression

- Add Missing RBAC on finalizer sub-resources for job integrations. (#1486, @astefanutti)
- Add Mutating WebhookConfigurations for the AdmissionCheck, RayJob, and JobSet to helm charts (#1567, @B1F030)
- Add Validating/Mutating WebhookConfigurations for the KubeflowJobs like PyTorchJob (#1460, @tenzen-y)
- Added event for QuotaReserved and fixed event for Admitted to trigger when admission checks complete (#1436, @trasc)
- Avoid finished Workloads from blocking quota after a Kueue restart (#1689, @trasc)
- Avoid recreating a Workload for a finished Job and finalize a job when the workload is declared finished. (#1383, @achernevskii)
- Do not (re)create ProvReq if the state of admission check is Ready (#1617, @mimowo)
- Fix Kueue crashing at the log level 6 when re-admitting workloads (#1644, @mimowo)
- Fix a bug in the pod integration that unexpected errors will occur when the pod isn't found (#1512, @achernevskii)
- Fix a bug that plain pods managed by kueue will remain in a terminating state, due to a finalizer (#1342, @tenzen-y)
- Fix client-go libraries bug that can not operate clusterScoped resources like ClusterQueue and ResourceFlavor. (#1294, @tenzen-y)
- Fix fungibility policy `Preempt` where it was not able to utilize the next flavor if preemption was not possible. (#1366, @alculquicondor)
- Fix handling of preemption within a cohort when there is no borrowingLimit. In that case,
  during preemption, the permitted resources to borrow were calculated as if borrowingLimit=0, instead of unlimited.
  
  As a consequence, when using `reclaimWithinCohort`, it was possible that a workload, scheduled to ClusterQueue with no borrowingLimit, would preempt more workloads than needed, even though it could fit by borrowing. (#1561, @mimowo)
- Fix the synchronization of the admission check state based on recreated ProvisioningRequest (#1585, @mimowo)
- Fixed fungibility policy `whenCanPreempt: Preempt`. The admission should happen in the flavor for which preemptions were issued. (#1332, @alculquicondor)
- Kueue replicas are advertised as Ready only once the webhooks are functional.
  
  This allows users to wait with the first requests until the Kueue deployment is available, so that the 
  early requests don't fail. (#1676, @mimowo)
- Pending workload from StrictFIFO ClusterQueue doesn't block borrowing from other ClusterQueues (#1399, @yaroslava-serdiuk)
- Remove deleted pending workloads from the cache (#1679, @astefanutti)
- Remove finalizer from Workloads that are orphaned (have no owners). (#1523, @achernevskii)
- Trigger an eviction for an admitted Job after an admission check changed state to Rejected. (#1562, @trasc)
- Webhooks are served in non-leading replicas (#1509, @astefanutti)

### Other (Cleanup or Flake)

- Expose utilization functions to setup jobframework reconcilers and webhooks (#1630, @tenzen-y)