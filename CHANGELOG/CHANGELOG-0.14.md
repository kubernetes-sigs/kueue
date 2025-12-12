## v0.14.6

Changes since `v0.14.5`:

## Changes by Kind

### Feature

- TAS: extend the information in condition messages and events about nodes excluded from calculating the
  assignment due to various recognized reasons like: taints, node affinity, node resource constraints. (#8169, @sohankunkerkar)

### Bug or Regression

- Fix `TrainJob` controller not correctly setting the `PodSet` count value based on `numNodes` for the expected number of training nodes. (#8146, @kaisoz)
- Fix a performance bug as some "read-only" functions would be taking unnecessary "write" lock. (#8182, @ErikJiang)
- Fix the race condition bug where the kueue_pending_workloads metric may not be updated to 0 after the last 
  workload is admitted and there are no new workloads incoming. (#8048, @Singularity23x0)
- Fixed the following bugs for the StatefulSet integration by ensuring the Workload object
  has the ownerReference to the StatefulSet:
  1. Kueue doesn't keep the StatefulSet as deactivated
  2. Kueue marks the Workload as Finished if all StatefulSet's Pods are deleted
  3. changing the "queue-name" label could occasionally result in the StatefulSet getting stuck (#8104, @mbobrovskyi)
- TAS: Fix handling of admission for workloads using the LeastFreeCapacity algorithm when the  "unconstrained"
  mode is used. In that case scheduling would fail if there is at least one node in the cluster which does not have
  enough capacity to accommodate at least one Pod. (#8171, @PBundyra)
- TAS: fix bug that when TopologyAwareScheduling is disabled, but there is a ResourceFlavor configured with topologyName, then preemptions fail with "workload requires Topology, but there is no TAS cache information". (#8196, @zhifei92)

### Other (Cleanup or Flake)

- Add safe-guard to protect against re-evaluating Finished workloads by scheduler which caused a bug. (#8199, @mimowo)

## v0.14.5

Changes since `v0.14.4`:

## Urgent Upgrade Notes 

### (No, really, you MUST read this before you upgrade)

- TAS: It supports the Kubeflow TrainJob.
  
  You should update Kubeflow Trainer to v2.1.0 at least when using Trainer v2. (#7755, @IrvingMg)
 
## Changes by Kind

### Bug or Regression

- AdmissionFairSharing: Fix the bug that occasionally a workload may get admitted from a busy LocalQueue,
  bypassing the entry penalties. (#7914, @IrvingMg)
- Fix a bug that an error during workload preemption could leave the scheduler stuck without retrying. (#7818, @olekzabl)
- Fix a bug that the cohort client-go lib is for a Namespaced resource, even though the cohort is a Cluster-scoped resource. (#7802, @tenzen-y)
- Fix integration of `manageJobWithoutQueueName` and `managedJobsNamespaceSelector` with JobSet by ensuring that jobSets without a queue are  not managed by Kueue if are not selected by the  `managedJobsNamespaceSelector`. (#7762, @MaysaMacedo)
- Fix issue #6711 where an inactive workload could transiently get admitted into a queue. (#7939, @olekzabl)
- Fix the bug that a workload which was deactivated by setting the `spec.active=false` would not have the 
  `wl.Status.RequeueState` cleared. (#7768, @sohankunkerkar)
- Fix the bug that the kubernetes.io/job-name label was not propagated from the k8s Job to the PodTemplate in
  the Workload object, and later to the pod template in the ProvisioningRequest. 
  
  As a consequence the ClusterAutoscaler could not properly resolve pod affinities referring to that label,
  via podAffinity.requiredDuringSchedulingIgnoredDuringExecution.labelSelector. For example, 
  such pod affinities can be used to request ClusterAutoscaler to provision a single node which is large enough
  to accommodate all Pods on a single Node.
  
  We also introduce the PropagateBatchJobLabelsToWorkload feature gate to disable the new behavior in case of 
  complications. (#7613, @yaroslava-serdiuk)
- Fix the race condition which could result that the Kueue scheduler occasionally does not record the reason
  for admission failure of a workload if the workload was modified in the meanwhile by another controller. (#7884, @mbobrovskyi)
- TAS: Fix the `requiredDuringSchedulingIgnoredDuringExecution` node affinity setting being ignored in topology-aware scheduling. (#7937, @kshalot)

## v0.14.4

Changes since `v0.14.3`:

## Changes by Kind

### Feature

- `ReclaimablePods` feature gate is introduced to enable users switching on and off the reclaimable Pods feature (#7537, @PBundyra)

### Bug or Regression

- Fix eviction of jobs with memory requests in decimal format (#7556, @brejman)
- Fix the bug for the StatefulSet integration that the scale up could get stuck if
  triggered immediately after scale down to zero. (#7500, @IrvingMg)
- MultiKueue: Remove remoteClient from clusterReconciler when kubeconfig is detected as invalid or insecure, preventing workloads from being admitted to misconfigured clusters. (#7517, @mszadkow)

## v0.14.3

Changes since `v0.14.2`:

## Urgent Upgrade Notes 

### (No, really, you MUST read this before you upgrade)

- MultiKueue: validate remote client kubeconfigs and reject insecure kubeconfigs by default; add feature gate MultiKueueAllowInsecureKubeconfigs to temporarily allow insecure kubeconfigs until v0.17.0.
  
  if you are using MultiKueue kubeconfigs which are not passing the new validation please
  enable the `MultiKueueAllowInsecureKubeconfigs` feature gate and let us know so that we can re-consider
  the deprecation plans for the feature gate. (#7452, @mszadkow)
 
## Changes by Kind

### Bug or Regression

- Fix a bug where a workload would not get requeued after eviction due to failed hotswap. (#7379, @pajakd)
- Fix the kueue-controller-manager startup failures.
  
  This fixed the Kueue CrashLoopBackOff due to the log message: "Unable to setup indexes","error":"could not setup multikueue indexer: setting index on workloads admission checks: indexer conflict. (#7440, @IrvingMg)
- Fixed the bug that prevented managing workloads with duplicated environment variable names in containers. This issue manifested when creating the Workload via the API. (#7443, @mbobrovskyi)
- Increase the number of Topology levels limitations for localqueue and workloads to 16 (#7427, @kannon92)
- Services: fix the setting of the `app.kubernetes.io/component` label to discriminate between different service components within Kueue as follows:
  - controller-manager-metrics-service for kueue-controller-manager-metrics-service 
  - visibility-service for kueue-visibility-server
  - webhook-service for kueue-webhook-service (#7450, @rphillips)

## v0.14.2

Changes since `v0.14.1`:

## Changes by Kind

### Feature

- JobFramework: Introduce an optional interface for custom Jobs, called JobWithCustomWorkloadActivation, which can be used to deactivate or active a custom CRD workload. (#7286, @tg123)

### Bug or Regression

- Fix existing workloads not being re-evaluated when new clusters are added to MultiKueueConfig. Previously, only newly created workloads would see updated cluster lists. (#7349, @mimowo)
- Fix handling of RayJobs which specify the spec.clusterSelector and the "queue-name" label for Kueue. These jobs should be ignored by kueue as they are being submitted to a RayCluster which is where the resources are being used and was likely already admitted by kueue. No need to double admit.
  Fix on a panic on kueue managed jobs if spec.rayClusterSpec wasn't specified. (#7258, @laurafitzgerald)
- Fixed a bug that Kueue would keep sending empty updates to a Workload, along with sending the "UpdatedWorkload" event, even if the Workload didn't change. This would happen for Workloads using any other mechanism for setting
  the priority than the WorkloadPriorityClass, eg. for Workloads for PodGroups. (#7305, @mbobrovskyi)
- MultiKueue x ElasticJobs: fix webhook validation bug which prevented scale up operation when any other
  than the default "AllAtOnce" MultiKueue dispatcher was used. (#7332, @mszadkow)
- TAS: Introduce missing validation against using incompatible `PodSet` grouping configuration in `JobSet, `MPIJob`, `LeaderWorkerSet`, `RayJob` and `RayCluster`. 
  
  Now, only groups of two `PodSet`s can be defined and one of the grouped `PodSet`s has to have only a single `Pod`.
  The `PodSet`s within a group must specify the same topology request via one of the `kueue.x-k8s.io/podset-required-topology` and `kueue.x-k8s.io/podset-preferred-topology` annotations. (#7263, @kshalot)
- Visibility API: Fix a bug that the Config clientConnection is not respected in the visibility server. (#7225, @tenzen-y)
- WorkloadRequestUseMergePatch: use "strict" mode for admission patches during scheduling which
  sends the ResourceVersion of the workload being admitted for comparing by kube-apiserver. 
  This fixes the race-condition issue that Workload conditions added concurrently by other controllers
  could be removed during scheduling. (#7279, @mszadkow)

### Other (Cleanup or Flake)

- Improve the messages presented to the user in scheduling events, by clarifying the reason for "insufficient quota"
  in case of workloads with multiple PodSets. 
  
  Example:
  - before: "insufficient quota for resource-type in flavor example-flavor, request > maximum capacity (24 > 16)"
  - after: "insufficient quota for resource-type in flavor example-flavor, previously considered podsets requests (16) + current podset request (8) > maximum capacity (16)" (#7293, @iomarsayed)

## v0.14.1

Changes since `v0.14.0`:

## Changes by Kind

### Bug or Regression

- Add rbac for train job for kueue-batch-admin and kueue-batch-user (#7198, @kannon92)
- Fix invalid annotations path being reported in `JobSet` topology validations. (#7191, @kshalot)
- Fix malformed annotations paths being reported for `RayJob` and `RayCluster` head group specs. (#7185, @kshalot)
- With BestEffortFIFO enabled, we will keep attempting to schedule a workload as long as
  it is waiting for preemption targets to complete. This fixes a bugs where an inadmissible
  workload went back to head of queue, in front of the preempting workload, allowing
  preempted workloads to reschedule (#7197, @gabesaba)

## v0.14.0

Changes since `v0.13.0`:

## Urgent Upgrade Notes 

### (No, really, you MUST read this before you upgrade)

- ProvisioningRequest: Remove setting deprecated ProvisioningRequest annotations on Kueue-managed Pods:
  - cluster-autoscaler.kubernetes.io/consume-provisioning-request
  - cluster-autoscaler.kubernetes.io/provisioning-class-name
  
  If you are implementing a ProvisioningRequest reconciler used by Kueue you should
  make sure the new annotations are supported:
  - autoscaling.x-k8s.io/consume-provisioning-request
  - autoscaling.x-k8s.io/provisioning-class-name (#6381, @kannon92)
 - Rename kueue-metrics-certs to kueue-metrics-cert cert-manager.io/v1 Certificate name in cert-manager manifests when installing Kueue using the Kustomize configuration.
  
  If you're using cert-manager and have deployed Kueue using the Kustomize configuration, you must delete the existing kueue-metrics-certs cert-manager.io/v1 Certificate before applying the new changes to avoid conflicts. (#6345, @mbobrovskyi)
 - Replace "DeactivatedXYZ" "reason" label values with "Deactivated" and introduce "underlying_cause" label to the following metrics:
  - "pods_ready_to_evicted_time_seconds"
  - "evicted_workloads_total"
  - "local_queue_evicted_workloads_total"
  - "evicted_workloads_once_total"
  
  If you rely on the "DeactivatedXYZ" "reason" label values, you can migrate to the "Deactivated" "reason" label value and the following "underlying_cause" label values:
  - ""
  - "WaitForStart"
  - "WaitForRecovery"
  - "AdmissionCheck"
  - "MaximumExecutionTimeExceeded"
  - "RequeuingLimitExceeded" (#6590, @mykysha)
 - TAS: Enforce a stricter value of the `kueue.x-k8s.io/podset-group-name` annotation in the creation webhook.
  
  Make sure the values of the `kueue.x-k8s.io/podset-group-name` annotation are not numbers.` (#6708, @kshalot)
 
## Upgrading steps

### 1. Back Up Topology Resources (skip if you are not using Topology API):

kubectl get topologies.kueue.x-k8s.io -o yaml > topologies.yaml

### 2. Update apiVersion in Backup File (skip if not using Topology API):
Replace `v1alpha1` with `v1beta1` in topologies.yaml for all resources:

sed -i -e 's/v1alpha1/v1beta1/g' topologies.yaml

### 3. Delete Old CRDs:

kubectl delete crd topologies.kueue.x-k8s.io

### 4. Remove Finalizers from Topologies (skip if you are not using Topology API):

kubectl get topology.kueue.x-k8s.io -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | while read -r name; do
  kubectl patch topology.kueue.x-k8s.io "$name" -p '{"metadata":{"finalizers":[]}}' --type='merge'
done

### 5. Install Kueue v0.14.0:
Follow the instructions [here](https://kueue.sigs.k8s.io/docs/installation/#install-a-released-version) to install.

### 6. Restore Topology Resources (skip if not using Topology API):

kubectl apply -f topologies.yaml

## Changes by Kind

### Deprecation

- Stop serving the QueueVisibility feature, but keep APIs (`.status.pendingWorkloadsStatus`) to avoid breaking changes.
  
  If you rely on the QueueVisibility feature (`.status.pendingWorkloadsStatus` in the ClusterQueue), you must migrate to VisibilityOndDemand 
  (https://kueue.sigs.k8s.io/docs/tasks/manage/monitor_pending_workloads/pending_workloads_on_demand). (#6631, @vladikkuzn)

### API Change

- TAS: Graduated TopologyAwareScheduling to Beta. (#6830, @mbobrovskyi)
- TAS: Support multiple nodes for failure handling by ".status.unhealthyNodes" in Workload. The "alpha.kueue.x-k8s.io/node-to-replace" annotation is no longer used (#6648, @pajakd)

### Feature

- Add an alpha integration for Kubeflow Trainer to Kueue. (#6597, @kaisoz)
- Add an exponential backoff for the TAS scheduler second pass. (#6753, @mykysha)
- Added priority_class label for kueue_local_queue_admitted_workloads_total metric. (#6845, @vladikkuzn)
- Added priority_class label for kueue_local_queue_evicted_workloads_total metric (#6898, @vladikkuzn)
- Added priority_class label for kueue_local_queue_quota_reserved_workloads_total metric. (#6897, @vladikkuzn)
- Added priority_class label for the following metrics:
  - kueue_admitted_workloads_total
  - kueue_evicted_workloads_total
  - kueue_evicted_workloads_once_total
  - kueue_quota_reserved_workloads_total
  - kueue_admission_wait_time_seconds
  - kueue_quota_reserved_wait_time_seconds
  - kueue_admission_checks_wait_time_seconds (#6951, @mbobrovskyi)
- Added priority_class to kueue_local_queue_admission_checks_wait_time_seconds (#6902, @vladikkuzn)
- Added priority_class to kueue_local_queue_admission_wait_time_seconds (#6899, @vladikkuzn)
- Added priority_class to kueue_local_queue_quota_reserved_wait_time_seconds (#6900, @vladikkuzn)
- Added workload_priority_class label for optional metrics (if waitForPodsReady is enabled):
  
  - kueue_ready_wait_time_seconds (Histogram)
  - kueue_admitted_until_ready_wait_time_seconds (Histogram)
  - kueue_local_queue_ready_wait_time_seconds (Histogram)
  - kueue_local_queue_admitted_until_ready_wait_time_seconds (Histogram) (#6944, @IrvingMg)
- DRA: Alpha support for Dynamic Resource Allocation in Kueue. (#5873, @alaypatel07)
- ElasticJobs: Support in-tree RayAutoscaler for RayCluster (#6662, @VassilisVassiliadis)
- KueueViz: Enhancing the following endpoint customizations and optimizations:
  - The frontend and backend ingress no longer have hardcoded NGINX annotations. You can now set your own annotations in Helm’s values.yaml using kueueViz.backend.ingress.annotations and kueueViz.frontend.ingress.annotations
  - The Ingress resources for KueueViz frontend and backend no longer require hardcoded TLS. You can now choose to use HTTP only by not providing kueueViz.backend.ingress.tlsSecretName and kueueViz.frontend.ingress.tlsSecretName
  - You can set environment variables like KUEUEVIZ_ALLOWED_ORIGINS directly from values.yaml using kueueViz.backend.env (#6682, @Smuger)
- MultiKueue: Support external frameworks.
  Introduced a generic MultiKueue adapter to support external, custom Job-like workloads. This allows users to integrate custom Job-like CRDs (e.g., Tekton PipelineRuns) with MultiKueue for resource management across multiple clusters. This feature is guarded by the `MultiKueueGenericJobAdapter` feature gate. (#6760, @khrm)
- Multikueue × ElasticJobs: The elastic `batchv1/Job` supports MultiKueue. (#6445, @ichekrygin)
- ProvisioningRequest: Graduate ProvisioningACC feature to GA (#6382, @kannon92)
- TAS: Graduated to Beta the following feature gates responsible for enabling and default configuration of the Node Hot Swap mechanism: 
  TASFailedNodeReplacement, TASFailedNodeReplacementFailFast, TASReplaceNodeOnPodTermination. (#6890, @mbobrovskyi)
- TAS: Implicit mode schedules consecutive indexes as close as possible (rank-ordering). (#6615, @PBundyra)
- TAS: introduce validation against using PodSet grouping and PodSet slicing for the same PodSet, 
  which is currently not supported. More precisely the `kueue.x-k8s.io/podset-group-name` annotation
  cannot be set along with any of: `kueue.x-k8s.io/podset-slice-size`, `kueue.x-k8s.io/podset-slice-required-topology`. (#7051, @kshalot)
- The following limits for ClusterQueue quota specification have been relaxed:
  - the number of Flavors per ResourceGroup is increased from 16 to 64
  - the number of Resources per Flavor, within a ResourceGroup, is increased from 16 to 64
  
  We also provide the following additional limits:
  - the total number of Flavors across all ResourceGroups is <= 256
  - the total number of Resources across all ResourceGroups is <= 256
  - the total number of (Flavor, Resource) pairs within a ResourceGroup is <= 512 (#6906, @LarsSven)
- Visibility API: Adds support for Securing APIService. (#6798, @MaysaMacedo)
- WorkloadRequestUseMergePatch: allows switching the Status Patch type from Apply to Merge for admission-related patches. (#6765, @mszadkow)

### Bug or Regression

- AFS: Fixed kueue-controller-manager crash when enabled AdmissionFairSharing feature gate without AdmissionFairSharing config. (#6670, @mbobrovskyi)
- ElasticJobs: Fix the bug for the ElasticJobsViaWorkloadSlices feature where in case of Job resize followed by eviction
  of the "old" workload, the newly created workload could get admitted along with the "old" workload.
  The two workloads would overcommit the quota. (#6221, @ichekrygin)
- ElasticJobs: Fix the bug that scheduling of the Pending workloads was not triggered on scale-down of the running 
  elastic Job which could result in admitting one or more of the queued workloads. (#6395, @ichekrygin)
- ElasticJobs: workloads correctly trigger workload preemption in response to a scale-up event. (#6973, @ichekrygin)
- FS: Fix the algorithm bug for identifying preemption candidates, as it could return a different
  set of preemption target workloads (pseudo random) in consecutive attempts in tie-break scenarios,
  resulting in excessive preemptions. (#6764, @PBundyra)
- FS: Fix the following FairSharing bugs:
  - Incorrect DominantResourceShare caused by rounding (large quotas or high FairSharing weight)
  - Preemption loop caused by zero FairSharing weight (#6925, @gabesaba)
- FS: Fixing a bug where a preemptor ClusterQueue was unable to reclaim its nominal quota when the preemptee ClusterQueue can borrow a large number of resources from the parent ClusterQueue / Cohort (#6617, @pajakd)
- FS: Validate FairSharing.Weight against small values which lose precision (0 < value <= 10^-9) (#6986, @gabesaba)
- Fix accounting for the `evicted_workloads_once_total` metric:
  - the metric wasn't incremented for workloads evicted due to stopped LocalQueue (LocalQueueStopped reason)
  - the reason used for the metric was "Deactivated" for workloads deactivated by users and Kueue, now the reason label can have the following values: Deactivated, DeactivatedDueToAdmissionCheck, DeactivatedDueToMaximumExecutionTimeExceeded, DeactivatedDueToRequeuingLimitExceeded. This approach aligns the metric with `evicted_workloads_total`.
  - the metric was incremented during preemption before the preemption request was issued. Thus, it could be incorrectly over-counted in case of the preemption request failure.
  - the metric was not incremented for workload evicted due to NodeFailures (TAS)
  
  The existing and introduced DeactivatedDueToXYZ reason label values will be replaced by the single "Deactivated" reason label value and underlying_cause in the future release. (#6332, @mimowo)
- Fix bug in workload usage removal simulation that results in inaccurate flavor assignment (#7077, @gabesaba)
- Fix support for PodGroup integration used by external controllers, which determine the 
  the target LocalQueue and the group size only later. In that case the hash would not be 
  computed resulting in downstream issues for ProvisioningRequest.
  
  Now such an external controller can indicate the control over the PodGroup by adding
  the `kueue.x-k8s.io/pod-suspending-parent` annotation, and later patch the Pods by setting
  other metadata, like the kueue.x-k8s.io/queue-name label to initiate scheduling of the PodGroup. (#6286, @pawloch00)
- Fix the bug for the StatefulSet integration which would occasionally cause a StatefulSet
  to be stuck without workload after renaming the "queue-name" label. (#7028, @IrvingMg)
- Fix the bug that a workload going repeatedly via the preemption and re-admission cycle would accumulate the
  "Previously" prefix in the condition message, eg: "Previously: Previously: Previously: Preempted to accommodate a workload ...". (#6819, @amy)
- Fix the bug which could occasionally cause workloads evicted by the built-in AdmissionChecks
  (ProvisioningRequest and MultiKueue) to get stuck in the evicted state which didn't allow re-scheduling.
  This could happen when the AdmissionCheck controller would trigger eviction by setting the
  Admission check state to "Retry". (#6283, @mimowo)
- Fix the validation messages when attempting to remove the queue-name label from a Deployment or StatefulSet. (#6715, @Panlq)
- Fixed a bug that prevented adding the kueue- prefix to the secretName field in cert-manager manifests when installing Kueue using the Kustomize configuration. (#6318, @mbobrovskyi)
- HC: When multiple borrowing flavors are available, prefer the flavor which
  results in borrowing more locally (closer to the ClusterQueue, further from the root Cohort).
  
  This fixes the scenario where a flavor would be selected which required borrowing
  from the root Cohort in one flavor, while in a second flavor, quota was
  available from the nearest parent Cohort. (#7024, @gabesaba)
- Helm: Fix a bug where the internal cert manager assumed that the helm installation name is 'kueue'. (#6869, @cmtly)
- Helm: Fixed a bug preventing Kueue from starting after installing via Helm with a release name other than "kueue" (#6799, @mbobrovskyi)
- Helm: Fixed bug where webhook configurations assumed a helm install name as "kueue". (#6918, @cmtly)
- KueueViz: Fix CORS configuration for development environments (#6603, @yankay)
- KueueViz: Fix a bug that only localhost is an executable domain. (#7011, @kincl)
- Pod-integration now correctly handles pods stuck in the Terminating state within pod groups, preventing them from being counted as active and avoiding blocked quota release. (#6872, @ichekrygin)
- ProvisioningRequest: Fix a bug that Kueue didn't recreate the next ProvisioningRequest instance after the
  second (and consecutive) failed attempt. (#6322, @PBundyra)
- Support disabling client-side ratelimiting in Config API clientConnection.qps with a negative value (e.g., -1) (#6300, @tenzen-y)
- TAS: Fix a bug that the node failure controller tries to re-schedule Pods on the failure node even after the Node is recovered and reappears (#6325, @pajakd)
- TAS: Fix a bug where new Workloads starve, caused by inadmissible workloads frequently requeueing due to unrelated Node LastHeartbeatTime update events. (#6570, @utam0k)
- TAS: Fix the scenario when Node Hot Swap cannot find a replacement. In particular, if slices are used
  they could result in generating invalid assignment, resulting in panic from TopologyUngater.
  Now, such a workload is evicted. (#6914, @PBundyra)
- TAS: Node Hot Swap allows replacing a node for workloads using PodSet slices, 
  ie. when the `kueue.x-k8s.io/podset-slice-size` annotation is used. (#6942, @pajakd)
- TAS: fix the bug that Kueue is crashing when PodSet has size 0, eg. no workers in LeaderWorkerSet instance. (#6501, @mimowo)

### Other (Cleanup or Flake)

- Promote ConfigurableResourceTransformations feature gate to stable. (#6599, @mbobrovskyi)
- Support for Kubernetes 1.34 (#6689, @mbobrovskyi)
- TAS: stop setting the "kueue.x-k8s.io/tas" label on Pods. 
  
  In case the implicit TAS mode is used, then the `kueue.x-k8s.io/podset-unconstrained-topology=true` annotation
  is set on Pods. (#6895, @mimowo)
