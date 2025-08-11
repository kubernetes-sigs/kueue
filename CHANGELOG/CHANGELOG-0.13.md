## V0.13.2

Changes since `v0.13.1`:

## Changes by Kind

### Bug or Regression

- ElasticJobs: Fix the bug that scheduling of the Pending workloads was not triggered on scale-down of the running
  elastic Job which could result in admitting one or more of the queued workloads. (#6407, @ichekrygin)
- Fix support for PodGroup integration used by external controllers, which determine the
  the target LocalQueue and the group size only later. In that case the hash would not be
  computed resulting in downstream issues for ProvisioningRequest.

  Now such an external controller can indicate the control over the PodGroup by adding
  the `kueue.x-k8s.io/pod-suspending-parent` annotation, and later patch the Pods by setting
  other metadata, like the kueue.x-k8s.io/queue-name label to initiate scheduling of the PodGroup. (#6461, @pawloch00)
- TAS: fix the bug that Kueue is crashing when PodSet has size 0, eg. no workers in LeaderWorkerSet instance. (#6522, @mimowo)

## v0.13.1

Changes since `v0.13.0`:

## Urgent Upgrade Notes

### (No, really, you MUST read this before you upgrade)

- Rename kueue-metrics-certs to kueue-metrics-cert cert-manager.io/v1 Certificate name in cert-manager manifests when installing Kueue using the Kustomize configuration.

  If you're using cert-manager and have deployed Kueue using the Kustomize configuration, you must delete the existing kueue-metrics-certs cert-manager.io/v1 Certificate before applying the new changes to avoid conflicts. (#6362, @mbobrovskyi)

## Changes by Kind

### Bug or Regression

- Fix accounting for the `evicted_workloads_once_total` metric:
  - the metric wasn't incremented for workloads evicted due to stopped LocalQueue (LocalQueueStopped reason)
  - the reason used for the metric was "Deactivated" for workloads deactivated by users and Kueue, now the reason label can have the following values: Deactivated, DeactivatedDueToAdmissionCheck, DeactivatedDueToMaximumExecutionTimeExceeded, DeactivatedDueToRequeuingLimitExceeded. This approach aligns the metric with `evicted_workloads_total`.
  - the metric was incremented during preemption before the preemption request was issued. Thus, it could be incorrectly over-counted in case of the preemption request failure.
  - the metric was not incremented for workload evicted due to NodeFailures (TAS)

  The existing and introduced DeactivatedDueToXYZ reason label values will be replaced by the single "Deactivated" reason label value and underlying_cause in the future release. (#6360, @mimowo)
- Fix the bug for the ElasticJobsViaWorkloadSlices feature where in case of Job resize followed by eviction
  of the "old" workload, the newly created workload could get admitted along with the "old" workload.
  The two workloads would overcommit the quota. (#6257, @ichekrygin)
- Fix the bug which could occasionally cause workloads evicted by the built-in AdmissionChecks
  (ProvisioningRequest and MultiKueue) to get stuck in the evicted state which didn't allow re-scheduling.
  This could happen when the AdmissionCheck controller would trigger eviction by setting the
  Admission check state to "Retry". (#6299, @mimowo)
- Fixed a bug that prevented adding the kueue- prefix to the secretName field in cert-manager manifests when installing Kueue using the Kustomize configuration. (#6343, @mbobrovskyi)
- ProvisioningRequest: Fix a bug that Kueue didn't recreate the next ProvisioningRequest instance after the
  second (and consecutive) failed attempt. (#6329, @PBundyra)
- Support disabling client-side ratelimiting in Config API clientConnection.qps with a negative value (e.g., -1) (#6305, @tenzen-y)
- TAS: Fix a bug that the node failure controller tries to re-schedule Pods on the failure node even after the Node is recovered and reappears (#6347, @pajakd)

## v0.13.0

Changes since `v0.12.0`:

## Urgent Upgrade Notes

### (No, really, you MUST read this before you upgrade)

- Helm:

    - Fixed KueueViz installation when enableKueueViz=true is used with default values for the image specifying parameters.
    - Split the image specifying parameters into separate repository and tag, both for KueueViz backend and frontend.

  If you are using Helm charts and installing KueueViz using custom images,
  then you need to specify them by kueueViz.backend.image.repository, kueueViz.backend.image.tag,
  kueueViz.fontend.image.repository and kueueViz.frontend.image.tag parameters. (#5400, @mbobrovskyi)
- ProvisioningRequest: Kueue now supports and manages ProvisioningRequests in v1 rather than v1beta1.

if you are using ProvisioningRequests with ClusterAutoscaler
ensure that your ClusterAutoscaler supports the v1 API (1.31.1+). (#4444, @kannon92)
- TAS: Drop support for MostFreeCapacity mode

The `TASProfileMostFreeCapacity` feature gate is no longer available.
If you specify that, you must remove it from the `.featureGates` in your Kueue Config or kueue-controller-manager command-line flag, `--feature-gates`. (#5536, @lchrzaszcz)
- The API Priority and Fairness configuration for the visibility endpoint is installed by default.

If your cluster is using k8s 1.28 or older, you will need to either update your version of k8s (to 1.29+) or remove the  FlowSchema and PriorityLevelConfiguration from the installation manifests of Kueue. (#5043, @mbobrovskyi)

## Upgrading steps

### 1. Backup Cohort Resources (skip if you are not using Cohorts API):

kubectl get cohorts.kueue.x-k8s.io -o yaml > cohorts.yaml


### 2. Update apiVersion in Backup File (skip if you are not using Cohort API):
Replace `v1alpha1` with `v1beta1` in `cohorts.yaml` for all resources:

sed -i -e 's/v1alpha1/v1beta1/g' cohorts.yaml
sed -i -e 's/^    parent: \(\S*\)$/    parentName: \1/' cohorts.yaml

### 3. Delete old CRDs:

kubectl delete crd cohorts.kueue.x-k8s.io


### 4. Install Kueue v0.13.x:
Follow the instruction [here](https://kueue.sigs.k8s.io/docs/installation/#install-a-released-version) to install.

### 5. Restore Cohorts Resources (skip if you are not using Cohorts API):

kubectl apply -f cohorts.yaml


## Changes by Kind

### Deprecation

- Promote Cohort CRD version to v1beta1

  The Cohort CRD `v1alpha1` is no longer supported.
  The `.spec.parent` in Cohort `v1alpha1` was replaced with `.spec.parentName` in Cohort `v1beta1`. (#5595, @tenzen-y)

### Feature

- AFS: Introduce the "entry penalty" for newly admitted workloads in a LQ.
  This mechanism is designed to prevent exploiting a flaw in the previous design which allowed
  to submit and get admitted multiple workloads from a single LQ before their usage would be
  accounted by the admission fair sharing mechanism. (#5933, @IrvingMg)
- AFS: preemption candidates are now ordered within ClusterQueue with respect to LQ's usage.
  The ordering of candidates coming from other ClusterQueues is unchanged. (#5632, @PBundyra)
- Adds the `pods_ready_to_evicted_time_seconds` metric that measures the time between workload's start,
  based on the PodsReady condition, and its eviction. (#5923, @amy)
- Flavor Fungibility: Introduces a new mode which allows to prefer preemption over borrowing when choosing a flavor.
  In this mode the preference is decided based on FavorFungibilityStrategy. This behavior is behind the
  FlavorFungibilityImplicitPreferenceDefault Alpha feature gate (disabled by default). (#6132, @pajakd)
- Graduate ManagedJobNamespaceSelector to GA (#5987, @kannon92)
- Helm: Allow setting the controller-manager's Pod `PriorityClassName` (#5631, @kaisoz)
- Helm: introduce new parameters to configure KueueViz installation:
    - kueueViz.backend.ingress and kueueViz.frontend.ingress to configure ingress
    - kueueViz.imagePullSecrets and kueueViz.priorityClassName (#5815, @btwseeu78)
- Helm: support for specifying nodeSelector and tolerations for all Kueue components (#5820, @zmalik)
- Introduce the ManagedJobsNamespaceSelectorAlwaysRespected feature, which allows you to manage Jobs in the managed namespaces. Even if the Jobs have queue name label, this feature ignore those Jobs when the deployed namespaces are not managed by Kueue (#5638, @PannagaRao)
- KueueViz: Add View YAML (#5992, @samzong)
- Kueue_controller_version prometheus metric, that specifies the Git commit ID used to compile Kueue controller (#5846, @rsevilla87)
- MultiKueue: Introduce the Dispatcher API which allows to provide an external dispatcher for nominating
  a subset of worker clusters for workload admission, instead of all clusters.

  The name of the dispatcher, either internal or external, is specified in the global config map under the
  `multikueue.dispatcherName` field. The following internal dispatchers are supported:
    - kueue.x-k8s.io/multikueue-dispatcher-all-at-once - nominates all clusters at once (default, used if the name is not specified)
    - kueue.x-k8s.io/multikueue-dispatcher-incremental - nominates clusters incrementally in constant time intervals

  **Important**: the current implementation requires implementations of external dispatchers to use
  `kueue-admission` as the field manager when patching the status.nominatedClusterNames field. (#5782, @mszadkow)
- Promoted ObjectRetentionPolicies to Beta. (#6209, @mykysha)
- Support for Elastic (Dynamically Sized Jobs) in Alpha as designed in [KEP-77](https://github.com/kubernetes-sigs/kueue/tree/main/keps/77-dynamically-sized-jobs).
  The implementation supports resizing (scale up and down) of batch/v1.Job and is behind the Alpha
  `ElasticJobsViaWorkloadSlices` feature gate. Jobs which are subject to resizing need to have the
  `kueue.x-k8s.io/elastic-job` annotation added at creation time. (#5510, @ichekrygin)
- Support for Kubernetes 1.33 (#5123, @mbobrovskyi)
- TAS: Add FailFast on Node's failure handling mode (#5861, @PBundyra)
- TAS: Co-locate leader and workers in a single replica in LeaderWorkerSet (#5845, @lchrzaszcz)
- TAS: Increase the maximal number of Topology Levels (`.spec.levels`) from 8 to 16. (#5635, @sohankunkerkar)
- TAS: Introduce a mode for triggering node replacement as soon as the workload's Pods are terminating
  on the node which is not ready. This behavior is behind the ReplaceNodeOnPodTermination Alpha feature gate
  (disabled by default). (#5931, @pajakd)
- TAS: Introduce two-level scheduling (#5353, @lchrzaszcz)

### Bug or Regression

- Emit the Workload event indicating eviction when LocalQueue is stopped (#5984, @amy)
- Fix a bug that would allow a user to bypass localQueueDefaulting. (#5451, @dgrove-oss)
- Fix a bug where the GroupKindConcurrency in Kueue Config is not propagated to the controllers (#5818, @tenzen-y)
- Fix incorrect workload admission after CQ is deleted in a cohort reducing the amount of available quota. The culprit of the issue was that the cached amount of quota was not updated on CQ deletion. (#5985, @amy)
- Fix the bug that Kueue, upon startup, would incorrectly admit and then immediately deactivate
  already deactivated Workloads.

  This bug also prevented the ObjectRetentionPolicies feature from deleting Workloads
  that were deactivated by Kueue before the feature was enabled. (#5625, @mbobrovskyi)
- Fix the bug that the webhook certificate setting under `controllerManager.webhook.certDir` was ignored by the internal cert manager, effectively always defaulting to /tmp/k8s-webhook-server/serving-certs. (#5432, @ichekrygin)
- Fixed bug that doesn't allow Kueue to admit Workload after queue-name label set. (#5047, @mbobrovskyi)
- HC: Add Cohort Go client library (#5597, @tenzen-y)
- Helm: Fix a templating bug when configuring managedJobsNamespaceSelector. (#5393, @mtparet)
- MultiKueue: Fix a bug that batch/v1 Job final state is not synced from Workload cluster to Management cluster when disabling the `MultiKueueBatchJobWithManagedBy` feature gate. (#5615, @ichekrygin)
- MultiKueue: Fix the bug that Job deleted on the manager cluster didn't trigger deletion of pods on the worker cluster. (#5484, @ichekrygin)
- RBAC permissions for the Cohort API to update & read by admins are now created out of the box. (#5431, @vladikkuzn)
- TAS: Fix a bug for the incompatible NodeFailureController name with Prometheus (#5819, @tenzen-y)
- TAS: Fix a bug that Kueue unintentionally gives up a workload scheduling in LeastFreeCapacity if there is at least one unmatched domain. (#5803, @PBundyra)
- TAS: Fix a bug that LeastFreeCapacity Algorithm does not respect level ordering (#5464, @tenzen-y)
- TAS: Fix a bug that the tas-node-failure-controller unexpectedly is started under the HA mode even though the replica is not the leader. (#5848, @tenzen-y)
- TAS: Fix bug which prevented admitting any workloads if the first resource flavor is reservation, and the fallback is using ProvisioningRequest. (#5426, @mimowo)
- TAS: Fix the bug when Kueue crashes if the preemption target, due to quota, is using a node which is already deleted. (#5833, @mimowo)
- TAS: fix the bug which would trigger unnecessary second pass scheduling for nodeToReplace
  in the following scenarios:
    1. Finished workload
    2. Evicted workload
    3. node to replace is not present in the workload's TopologyAssignment domains (#5585, @mimowo)
- TAS: fix the scenario when deleted workload still lives in the cache. (#5587, @mimowo)
- Use simulation of preemption for more accurate flavor assignment.
  In particular, in certain scenarios when preemption while borrowing is enabled,
  the previous heuristic would wrongly state that preemption was possible. (#5529, @pajakd)
- Use simulation of preemption for more accurate flavor assignment.
  In particular, the previous heuristic would wrongly state that preemption
  in a flavor was possible even if no preemption candidates could be found.

  Additionally, in scenarios when preemption while borrowing is enabled,
  the flavor in which reclaim is possible is preferred over flavor where
  priority-based preemption is required. This is consistent with prioritizing
  flavors when preemption without borrowing is used. (#5698, @gabesaba)

### Other (Cleanup or Flake)

- KueueViz: reduce the image size from 1.14 GB to 267MB, resulting in faster pull and shorter startup time. (#5860, @mbobrovskyi)
