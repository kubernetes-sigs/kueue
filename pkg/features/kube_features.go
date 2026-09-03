/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package features

import (
	"testing"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/version"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/component-base/featuregate"
)

const (
	// owner: @trasc
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/420-partial-admission
	//
	// Enables partial admission.
	PartialAdmission featuregate.Feature = "PartialAdmission"

	// owner: @yaroslava-serdiuk
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/12100-partial-scale-up

	// Enables partial scale up for elastic jobs.
	ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp featuregate.Feature = "ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp"

	// owner: @KunWuLuan
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/582-preempt-based-on-flavor-order
	//
	// Enables flavor fungibility.
	FlavorFungibility featuregate.Feature = "FlavorFungibility"

	// owner: @pbundyra
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/168-2-pending-workloads-visibility
	//
	// Enables Kueue visibility on demand
	VisibilityOnDemand featuregate.Feature = "VisibilityOnDemand"

	// Allows disabling WaitForPodsReady during the migration period.
	//
	// Deprecated: planned to be removed in 0.21. Temporary option while WaitForPodsReady is enabled by default.
	DisableWaitForPodsReady featuregate.Feature = "DisableWaitForPodsReady"
	// owner: @yaroslava-serdiuk
	// kep: https://github.com/kubernetes-sigs/kueue/issues/1283
	//
	// Enable priority sorting within the cohort.
	PrioritySortingWithinCohort featuregate.Feature = "PrioritySortingWithinCohort"

	// owner: @mukund-wayve
	// kep: https://github.com/kubernetes-sigs/kueue/issues/9406
	//
	// In fair sharing preemption, allow preemption when the preemptor CQ
	// is within nominal quota for contested resources, bypassing DRS gates.
	FairSharingPreemptWithinNominal featuregate.Feature = "FairSharingPreemptWithinNominal"

	// owner: @mukund-wayve
	// kep: https://github.com/kubernetes-sigs/kueue/issues/9406
	//
	// In fair sharing admission ordering, prefer workloads whose subtree
	// is not borrowing on the workload's requested flavors, checked at
	// every level of the cohort hierarchy.
	FairSharingPrioritizeNonBorrowing featuregate.Feature = "FairSharingPrioritizeNonBorrowing"

	// owner: @trasc
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/693-multikueue
	//
	// Enables MultiKueue support.
	MultiKueue featuregate.Feature = "MultiKueue"

	// owner: @kevin85421
	// issue: https://github.com/kubernetes-sigs/kueue/issues/14779
	//
	// Enables clearing ttlSecondsAfterFinished when creating remote batch Jobs.
	MultiKueueBatchJobClearingTTLSecondsAfterFinishedOnWorkerCluster featuregate.Feature = "MultiKueueBatchJobClearingTTLSecondsAfterFinishedOnWorkerCluster"

	// owner: @mimowo
	//
	// Enable Topology Aware Scheduling allowing to optimize placement of Pods
	// to put them on closely located nodes (e.g. within the same rack or block).
	TopologyAwareScheduling featuregate.Feature = "TopologyAwareScheduling"

	// owner: @kpostoffice
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/1833-metrics-for-local-queue
	//
	// Enabled gathering of LocalQueue metrics
	LocalQueueMetrics featuregate.Feature = "LocalQueueMetrics"

	// owner: @pbundyra
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/2724-topology-aware-scheduling
	//
	// Enable to set use Mixed algorithm (BestFit or LeastFreeCapacity) for TAS which switch the algorithm based on TAS requirements level.
	TASProfileMixed featuregate.Feature = "TASProfileMixed"

	// owner: @pbundyra
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/4136-admission-fair-sharing
	//
	// Enable admission fair sharing
	AdmissionFairSharing featuregate.Feature = "AdmissionFairSharing"

	// owner: @pajakd
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/2724-topology-aware-scheduling
	//
	// Enable replacement of failed node in TAS.
	TASFailedNodeReplacement featuregate.Feature = "TASFailedNodeReplacement"

	// owner: @ichekrygin
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/77-dynamically-sized-jobs
	//
	// ElasticJobsViaWorkloadSlices enables workload-slices support.
	ElasticJobsViaWorkloadSlices featuregate.Feature = "ElasticJobsViaWorkloadSlices"

	// owner: @sohankunkerkar
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/77-dynamically-sized-jobs
	//
	// ElasticJobsViaWorkloadSlicesWithTAS enables TAS integration with elastic workload slices.
	// Requires both ElasticJobsViaWorkloadSlices and TopologyAwareScheduling to be enabled.
	ElasticJobsViaWorkloadSlicesWithTAS featuregate.Feature = "ElasticJobsViaWorkloadSlicesWithTAS"

	// owner: @pbundyra
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/2724-topology-aware-scheduling
	//
	// Evict Workload if Kueue couldn't find replacement for a failed node in TAS in the first attempt.
	TASFailedNodeReplacementFailFast featuregate.Feature = "TASFailedNodeReplacementFailFast"

	// owner: @pajakd
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/2724-topology-aware-scheduling
	//
	// In TAS, treat node as failed if the node is not ready and the pods assigned to this node terminate.
	TASReplaceNodeOnPodTermination featuregate.Feature = "TASReplaceNodeOnPodTermination"

	// owner: @yakticus
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/2724-topology-aware-scheduling
	//
	// Skip TAS assignment recomputation (failed-node replacement and
	// post-eviction requeue) for Workloads owned by a single Pod; the pod
	// cannot relocate and the Workload cannot outlive it, so a recomputed
	// topologyAssignment only diverges from the node the pod runs on.
	SkipReassignmentForPodOwnedWorkloads featuregate.Feature = "SkipReassignmentForPodOwnedWorkloads"

	// owner: @yakticus
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/2724-topology-aware-scheduling
	//
	// Mark a TAS node as failed once it has been NotReady for NodeFailureDelay,
	// regardless of pod state. When disabled, node replacement is driven solely
	// by pod termination. Sunset gate: deprecated and off by default from 0.19.
	TASReplaceNodeDueToNotReadyOverFixedTime featuregate.Feature = "TASReplaceNodeDueToNotReadyOverFixedTime"

	// owner: @PannagaRao
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/3589-manage-jobs-selectively
	//
	// Enforces that even Jobs with a queue-name label are only reconciled if their namespace
	// matches managedJobsNamespaceSelector.
	ManagedJobsNamespaceSelectorAlwaysRespected featuregate.Feature = "ManagedJobsNamespaceSelectorAlwaysRespected"

	// owner: @pajakd
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/2724-topology-aware-scheduling
	//
	// Use balanced placement algorithm in TAS. This feature gate is going to be replaced by an API
	// before graduation or deprecation.
	TASBalancedPlacement featuregate.Feature = "TASBalancedPlacement"

	// owner: @ShaanveerS
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/2724-topology-aware-scheduling
	//
	// Enable grouping v1beta2 TopologyAssignments by reusable hostname prefixes.
	TASAssignmentsEncodingByHostnamePrefix featuregate.Feature = "TASAssignmentsEncodingByHostnamePrefix"

	// owner: @alaypatel07
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/2941-DRA
	//
	// Enable quota accounting for Dynamic Resource Allocation (DRA) devices in workloads.
	KueueDRAIntegration featuregate.Feature = "KueueDRAIntegration"

	// owner: @sohankunkerkar
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/2941-DRA
	//
	// Enable extended resources support for DRA. Allows workloads to request DRA devices
	// via standard resources.requests using DeviceClass extendedResourceName.
	KueueDRAIntegrationExtendedResource featuregate.Feature = "KueueDRAIntegrationExtendedResource"

	// owner: @thc1006
	// issue: https://github.com/kubernetes-sigs/kueue/issues/14763
	//
	// Refuse `pods` in a resource transformation, and as a deviceClassMappings name
	// once KueueDRAIntegration builds the mapper. With it off such a configuration
	// loads, and flavor assignment then overwrites the key with the PodSet count.
	ReservedResourceNameValidation featuregate.Feature = "ReservedResourceNameValidation"

	// owner: @MaysaMacedo
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/7513-quota-check-strategy
	//
	// Enable QuotaCheckStrategy for quota admission.
	QuotaCheckStrategy featuregate.Feature = "QuotaCheckStrategy"

	// owner: @kannon92
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/2941-DRA
	//
	// Reject workloads that use DRA resources when the KueueDRAIntegration feature gate is disabled.
	KueueDRARejectWorkloadsWhenDRADisabled featuregate.Feature = "KueueDRARejectWorkloadsWhenDRADisabled"

	// owner: @PannagaRao
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/2941-DRA
	//
	// Enable counter-based quota for partitionable DRA devices (e.g., MIG). Tracks
	// counter consumption from ResourceSlice devices via consumesCounters configuration.
	KueueDRAIntegrationPartitionableDevices featuregate.Feature = "KueueDRAIntegrationPartitionableDevices"

	// owner: @sohankunkerkar
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/2941-DRA
	//
	// Enable capacity-based quota for DRA devices that allow multiple allocations
	// (consumable capacity, KEP-5075). Tracks consumed capacity dimensions from the
	// device's Capacity field and the workload's capacity.requests.
	KueueDRAIntegrationConsumableCapacity featuregate.Feature = "KueueDRAIntegrationConsumableCapacity"

	// owner: @khrm
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/2349-multikueue-external-custom-job-support
	//
	// Enable MultiKueue support for external custom Jobs via configurable adapters.
	MultiKueueAdaptersForCustomJobs featuregate.Feature = "MultiKueueAdaptersForCustomJobs"

	// owner: @mszadkow
	//
	// Enable all updates to Workload objects to use Patch Merge instead of Patch Apply.
	WorkloadRequestUseMergePatch featuregate.Feature = "WorkloadRequestUseMergePatch"

	// owner: @kannon92
	// issue: https://github.com/kubernetes-sigs/kueue/issues/12144
	//
	// Enforce safe path validation for locationType=Path MultiKueueCluster
	// kubeconfig references. When enabled (the default), only paths under
	// /etc/multikueue/kubeconfigs/ are accepted. When disabled, the
	// controller falls back to the legacy behavior of allowing any path.
	MultiKueueKubeConfigPathValidation featuregate.Feature = "MultiKueueKubeConfigPathValidation"

	// owner: @pbundyra
	//
	// Enables reclaimable pods counting towards quota.
	ReclaimablePods featuregate.Feature = "ReclaimablePods"

	// owner: @hdp617
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/693-multikueue
	//
	// Enables ClusterProfile integration for MultiKueue.
	MultiKueueClusterProfile featuregate.Feature = "MultiKueueClusterProfile"

	// owner: @kshalot
	//
	// issue: https://github.com/kubernetes-sigs/kueue/issues/6757
	// Enabled failure recovery of pods stuck in terminating state.
	FailureRecoveryPolicy featuregate.Feature = "FailureRecoveryPolicy"

	// owner: @kannon92
	//
	// issue: https://github.com/kubernetes-sigs/kueue/issues/8190
	// Enables TLSOptions for TLS MinVersion, CipherSuites, and CurvePreferences for kueue servers
	TLSOptions featuregate.Feature = "TLSOptions"

	// owner: @mykysha
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/3899-remove-finalizers-with-strict-patch
	//
	// Finalizers are removed using a strict patch not to cause race conditions.
	RemoveFinalizersWithStrictPatch featuregate.Feature = "RemoveFinalizersWithStrictPatch"

	// owner: @j-skiba
	//
	// issue: https://github.com/kubernetes-sigs/kueue/issues/8828
	// Enable workload eviction when node is tainted and pods are not able to run.
	TASReplaceNodeOnNodeTaints featuregate.Feature = "TASReplaceNodeOnNodeTaints"

	// owner: @dkaluza
	//
	// issue: https://github.com/kubernetes-sigs/kueue/issues/9156
	// Enables pod labeling with corresponding cluster and local queue names
	AssignQueueLabelsForPods featuregate.Feature = "AssignQueueLabelsForPods"

	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/2724-topology-aware-scheduling
	//
	// Enable multi-layer topology constraints for TAS, allowing up to 3 slice
	// layers (in addition to the podset-level constraint) for fine-grained
	// placement across deep topology hierarchies.
	TASMultiLayerTopology featuregate.Feature = "TASMultiLayerTopology"

	// owner: @sohankunkerkar
	//
	// issue: https://github.com/kubernetes-sigs/kueue/issues/9694
	// Skip equivalent inadmissible workloads in BestEffortFIFO scheduling.
	SchedulingEquivalenceHashing featuregate.Feature = "SchedulingEquivalenceHashing"

	// owner: @venuchitta
	//
	// issue: https://github.com/kubernetes-sigs/kueue/issues/14534
	// Exclude the PodSet name from the scheduling equivalence hash. Flavor assignment
	// does not use the name, so including it splits otherwise equivalent Workloads.
	SchedulingEquivalenceHashingIgnorePodSetName featuregate.Feature = "SchedulingEquivalenceHashingIgnorePodSetName"

	// owner: @IrvingMg
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/7066-custom-metric-labels
	//
	// Enable custom metadata labels on Kueue metrics
	CustomMetricLabels featuregate.Feature = "CustomMetricLabels"

	// owner: @everpeace
	//
	// pr: https://github.com/kubernetes-sigs/kueue/pull/7268#issuecomment-3890609376
	// Enables the Kubeflow's SparkApplication integration
	SparkApplicationIntegration featuregate.Feature = "SparkApplicationIntegration"

	// owner: @kshalot
	//
	// issue: https://github.com/kubernetes-sigs/kueue/issues/8303
	// Enables preemption orchestration in MultiKueue worker clusters, preventing
	// concurrent preemptions causing disruptions to other workloads.
	MultiKueueOrchestratedPreemption featuregate.Feature = "MultiKueueOrchestratedPreemption"

	// owner: @vladikkuzn
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/7990-priority-boost
	//
	// Enable priority boost via the kueue.x-k8s.io/priority-boost annotation,
	// allowing external controllers to adjust a workload's effective priority.
	PriorityBoost featuregate.Feature = "PriorityBoost"

	// owner: @VassilisVassiliadis
	//
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/6915-scheduling-gated-by-annotation
	//
	// Enables gating the admission of workloads based on annotations.
	AdmissionGatedBy featuregate.Feature = "AdmissionGatedBy"

	// owner: @mbobrovskyi
	//
	// issue: https://github.com/kubernetes-sigs/kueue/issues/9872
	//
	// ShortWorkloadNames ensures that generated Workload names do not exceed
	// 63 characters, making them compatible with Kubernetes label value limits.
	ShortWorkloadNames featuregate.Feature = "ShortWorkloadNames"

	// owner: @tkillian
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/6143-quota-release-strategy
	//
	// When enabled, pods with a DeletionTimestamp are treated as inactive in the
	// Pod integration's IsActive() check, allowing quota to be released immediately
	// when preempted pods begin terminating rather than waiting for the grace period.
	FastQuotaReleaseInPodIntegration featuregate.Feature = "FastQuotaReleaseInPodIntegration"

	// owner: @ShaanveerS
	//
	// issue: https://github.com/kubernetes-sigs/kueue/issues/7259
	// Enables rejecting updates to ClusterQueues with invalid
	// AdmissionCheckStrategy.OnFlavors references.
	RejectUpdatesToCQWithInvalidOnFlavors featuregate.Feature = "RejectUpdatesToCQWithInvalidOnFlavors"

	// owner: @sebest
	//
	// issue: https://github.com/kubernetes-sigs/kueue/issues/1789
	// Finish workloads whose controller owner no longer exists, preventing
	// stale workload accumulation (e.g., after PodsReady timeout eviction
	// deletes a Deployment-owned pod).
	FinishOrphanedWorkloads featuregate.Feature = "FinishOrphanedWorkloads"

	// owner: @Mostafahassen1
	//
	// kep: https://github.com/kubernetes-sigs/kueue/pull/10877#issuecomment-4412688735
	// Enables configurable stepSize for the MultiKueue Incremental Dispatcher.
	MultiKueueIncrementalDispatcherConfig featuregate.Feature = "MultiKueueIncrementalDispatcherConfig"

	// owner: @andrewseif
	//
	// kep: https://github.com/kubernetes-sigs/kueue/issues/12854
	// Enables the MultiKueue Incremental Dispatcher to nominate worker clusters in the
	// order defined in MultiKueueConfig.spec.clusters. When disabled, clusters are
	// nominated in alphabetical order.
	MultiKueueIncrementalDispatcherRespectConfigOrder featuregate.Feature = "MultiKueueIncrementalDispatcherRespectConfigOrder"

	// owner: @pbundyra
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/8691-concurrent-admission
	//
	// Enables Concurrent Admission feature which allows pursuing multiple ResourceFlavors in parallel.
	ConcurrentAdmission featuregate.Feature = "ConcurrentAdmission"

	// Enable recording of WorkloadCreationLatency metric.
	MetricForWorkloadCreationLatency featuregate.Feature = "MetricForWorkloadCreationLatency"

	// owner: @j-skiba
	// issue: https://github.com/kubernetes-sigs/kueue/issues/10902
	// Enable evaluation of preferredDuringSchedulingIgnoredDuringExecution node affinities during scheduling in TAS.
	// The preferred affinity will take precedence over the placement policy (eg. BestFit , LeastFreeCapacity).
	TASRespectNodeAffinityPreferred featuregate.Feature = "TASRespectNodeAffinityPreferred"

	// owner: @olekzabl
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/9988-multikueue-manager-quota-automation
	//
	// Enables MultiKueue manager quota automation support.
	MultiKueueManagerQuotaAutomation featuregate.Feature = "MultiKueueManagerQuotaAutomation"

	// owner: @ivnovakov
	// issue: https://github.com/kubernetes-sigs/kueue/issues/10032
	//
	// WorkloadIdentifierAnnotations makes Kueue read and write the workload identifiers
	// (pod-group-name and prebuilt-workload-name) via annotations. When the gate is
	// disabled, the label-based identifiers are used instead.
	WorkloadIdentifierAnnotations featuregate.Feature = "WorkloadIdentifierAnnotations"

	// owner: @atosatto
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/10765-workload-priority-class-defaulting
	//
	// issue: https://github.com/kubernetes-sigs/kueue/issues/10765
	// Enable defaulting of WorkloadPriorityClass to a WorkloadPriorityClass
	// named "default" when no explicit priority class label is set.
	WorkloadPriorityClassDefaulting featuregate.Feature = "WorkloadPriorityClassDefaulting"

	// owner: @mszadkow
	//
	// issue: https://github.com/kubernetes-sigs/kueue/issues/7539
	// Enable reporting of Cohort related metrics (also including ClusterQueueInfo metric).
	MetricsForCohorts featuregate.Feature = "MetricsForCohorts"

	// owner: @MatteoFari
	//
	// issue: https://github.com/kubernetes-sigs/kueue/issues/11677
	// Enables cleaning up ProvisioningRequests owned by an evicted Workload.
	CleanupProvisioningRequestsOnEviction featuregate.Feature = "CleanupProvisioningRequestsOnEviction"

	// owner: @tenzen-y
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/2724-topology-aware-scheduling
	// issue: https://github.com/kubernetes-sigs/kueue/issues/10659
	// Enable accurately topology aware scheduling when multiple flavors cover the same Node.
	TASHandleOverlappingFlavors featuregate.Feature = "TASHandleOverlappingFlavors"

	// owner: @j-skiba
	// kep: https://github.com/kubernetes-sigs/kueue/issues/10852
	//
	// UnadmittedWorkloadsObservability enables granular Prometheus metrics and
	// updates status reasons for QuotaReserved/Admitted conditions to use tiered reasons.
	UnadmittedWorkloadsObservability featuregate.Feature = "UnadmittedWorkloadsObservability"

	// owner: @mimowo
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/2724-topology-aware-scheduling
	//
	// Enable re-computing the assignment within the same scheduling cycle when a TAS workload doesn't fit.
	// Requires TopologyAwareScheduling to be enabled.
	TASRecomputeAssignmentWithinSchedulingCycle featuregate.Feature = "TASRecomputeAssignmentWithinSchedulingCycle"

	// owner: @j-skiba
	// kep: https://github.com/kubernetes-sigs/kueue/issues/10852
	//
	// UnadmittedWorkloadsExplicitStatus gates the immediate, proactive
	// initialization of both QuotaReserved and Admitted status conditions
	// to False during a workload's first reconciliation.
	// This feature gate requires UnadmittedWorkloadsObservability to be enabled to take effect.
	UnadmittedWorkloadsExplicitStatus featuregate.Feature = "UnadmittedWorkloadsExplicitStatus"

	// owner: @kevin85421
	//
	// issue: https://github.com/kubernetes-sigs/kueue/issues/12820
	// Defers finalizing a deleting GCS fault-tolerant RayService's Workload until
	// KubeRay's Redis cleanup Job completes, so the cleanup Job is not gated forever
	// and can purge the RayCluster's Redis metadata namespace.
	//
	// TODO(#12820): temporary workaround in the RayService integration. Remove once
	// the generic FinishOrphanedWorkloads owner-deletion check lands.
	DeferRayServiceFinalizationForRedisCleanup featuregate.Feature = "DeferRayServiceFinalizationForRedisCleanup"

	// owner: @j-skiba
	//
	// Enable caching node matching results (NodeSelector, Tolerations, Affinity) per workload/PodSet
	// to be reused within a single scheduling cycle by TAS evaluations corresponding to different
	// sets of preemption candidates.
	TASCacheNodeMatchResults featuregate.Feature = "TASCacheNodeMatchResults"

	// owner: @j-skiba
	//
	// Enables caching of remaining capacity and clone reduction in TAS Flavor Snapshot.
	TASCachingRemainingResources featuregate.Feature = "TASCachingRemainingResources"

	// owner: @tenzen-y
	//
	// issue: https://github.com/kubernetes-sigs/kueue/issues/12580
	// Enables caching the immutable TAS topology tree on the TASFlavorCache and reusing it
	// across scheduling cycles while the nodesCache generation is unchanged. When disabled,
	// every snapshot builds a tree of its own and none is ever shared between snapshots,
	// which is the behavior before the tree cache was introduced. Kept as an off-switch in
	// case a stale or shared tree is suspected in a production TAS deployment.
	TASCacheTopologyTree featuregate.Feature = "TASCacheTopologyTree"

	// owner: @kshalot
	//
	// issue: https://github.com/kubernetes-sigs/kueue/issues/8871
	// Enable integration of the https://github.com/kubernetes-sigs/scheduler-library.
	SchedulerLibraryIntegration featuregate.Feature = "SchedulerLibraryIntegration"

	// owner: @j-skiba
	//
	// VectorizedResourceRequests enables slice-based indexing for resource requests in TAS snapshots,
	// replacing map lookups for higher performance during scheduling and preemption.
	VectorizedResourceRequests featuregate.Feature = "VectorizedResourceRequests"

	// owner: @vladikkuzn
	//
	// Rejects Workloads with negative container or pod-level resource requests/limits.
	WorkloadValidateResourcesAreNonNegative featuregate.Feature = "WorkloadValidateResourcesAreNonNegative"

	// owner: mszadkow
	//
	// Rejects Workloads with non-positive TAS slice sizes in PodSet topology requests.
	TASValidateWorkloadSliceSize featuregate.Feature = "TASValidateWorkloadSliceSize"

	// owner: @Dasmat13
	// issue: https://github.com/kubernetes-sigs/kueue/issues/12775
	//
	// WorkloadValidationForPodSetMetadata enables validation of labels and annotations
	// in PodSet template metadata during Workload creation and update.
	WorkloadValidationForPodSetMetadata featuregate.Feature = "WorkloadValidationForPodSetMetadata"

	// owner: @Nilsachy
	// issue: https://github.com/kubernetes-sigs/kueue/issues/13662
	//
	// PrioritizePreemptorWorkloads enables prioritization of workloads waiting for preemption
	// to complete to prevent quota stealing and thrashing during desynchronized evictions.
	PrioritizePreemptorWorkloads featuregate.Feature = "PrioritizePreemptorWorkloads"

	// owner: @ivnovakov
	//
	// pr: https://github.com/kubernetes-sigs/kueue/pull/13279#discussion_r3655384989
	// Keeps spec.leaderWorkerTemplate.size immutable while a LeaderWorkerSet is managed by
	// Kueue. Workload updates never recompute PodSets, so growing size would ungate extra
	// pods per group without accounting for quota. Recreate the LeaderWorkerSet to change
	// the size, or disable this gate to accept the previous behavior.
	LWSImmutableGroupSize featuregate.Feature = "LWSImmutableGroupSize"

	// owner: @varunsyal
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/582-preempt-based-on-flavor-order
	//
	// Keeps the flavor scan progress recorded in LastTriedFlavorIdx usable in the next
	// scheduling cycle. Without this the progress is discarded whenever the ClusterQueue's
	// AllocatableResourceGeneration advances, which happens on every admission or eviction
	// in the Cohort, and whenever a Workload is skipped because another Workload processed
	// earlier in the cycle took the capacity or the preemption targets it had been assigned.
	// Under sustained load either can happen every cycle, leaving the scan to restart from
	// the first flavor indefinitely.
	FlavorFungibilityPreserveScanProgress featuregate.Feature = "FlavorFungibilityPreserveScanProgress"

	// owner: @iaalm
	//
	// pr: https://github.com/kubernetes-sigs/kueue/pull/10009
	// Tolerates a missing owner while walking the ownership chain of an object that is
	// already being deleted (GC teardown with mixed foreground/background deletion), so
	// Kueue webhooks do not block finalizer removal. Disable to restore the previous
	// behavior of failing the admission request when an owner cannot be found.
	//
	// Note: the tolerance cannot apply at creation time — the API server never creates
	// an object with a deletionTimestamp itself (one could technically be injected by a
	// mutating webhook, which is deliberately not covered) — so the strict missing-owner
	// handling on CREATE (which
	// protects against acting on a stale informer view of a just-created owner) is
	// unchanged. The earliest the tolerance can apply is an update after deletion has
	// begun, and suspend defaulting only ever adds suspend, so no path exists to
	// unsuspend an object or bypass quota via create-then-delete.
	SkipAncestorCheckForDeletedWorkloads featuregate.Feature = "SkipAncestorCheckForDeletedWorkloads"

	// owner: @kevin85421
	//
	// Enables MultiKueue to forward manager-side spec changes (currently a RayService
	// serveConfigV2 edit) onto the worker copy after admission, and to watch the manager
	// job so such a change is forwarded promptly instead of on the next periodic requeue.
	MultiKueueRemoteSpecSync featuregate.Feature = "MultiKueueRemoteSpecSync"

	// owner: @pajakd
	// issue: https://github.com/kubernetes-sigs/kueue/issues/13320
	//
	// Enable recomputing preemption targets if they overlap with another workload's targets
	// within the same scheduling cycle. This recomputation is done assuming that the earlier
	// preemptions issued in this cycle are already completed. If the outcome of the new assuignment
	// is Preempt, we issue preemptions for the new set of targets. If the outcome is Fit,
	// it means that the preemptions issued by other workloads in this cycle are sufficient also
	// for the considered workload to get it. We don't immediately admit this workload as we have
	// to wait for these preemptions to complete.
	RecomputeAssignmentUponPreemptionTargetsOverlap featuregate.Feature = "RecomputeAssignmentUponPreemptionTargetsOverlap"

	// owner: @vladikkuzn
	//
	// issue: https://github.com/kubernetes-sigs/kueue/pull/13014
	// Refuse to adopt an existing Workload by pod-group name when it was not created
	// by the pod-group framework (missing is-group-workload annotation).
	PodIntegrationValidateGroupOwner featuregate.Feature = "PodIntegrationValidateGroupOwner"

	// owner: @ivnovakov
	//
	// Validates an update in the RayCluster, RayJob, RayService and SparkApplication
	// webhooks when the object carried a queue-name label before the update, so removing
	// that label cannot leave a job running unmanaged. Jobs that Kueue manages neither
	// before nor after the update are never validated. When disabled, those webhooks only
	// validate an update if manageJobsWithoutQueueName is set or the new object carries a
	// queue-name label.
	ValidateRayAndSparkJobUpdates featuregate.Feature = "ValidateRayAndSparkJobUpdates"

	// owner: @lightZebra
	//
	// pr: https://github.com/kubernetes-sigs/kueue/pull/14128
	// Runs the first strategy again for fair preemption algorithm if DRS was decreased
	// during processing. This allows to preempt more workloads if first run missed some
	// candidates because of previously high DRS, preemptions are still fair.
	//
	// Note: This feature can be promoted to "beta" once https://github.com/kubernetes-sigs/kueue/issues/14543
	// is fixed because it might boost chances for issue #14543 to appear.
	FairSharingReevaluatePreemptionCandidates featuregate.Feature = "FairSharingReevaluatePreemptionCandidates"

	// owner: @j-skiba
	//
	// Enables Dynamic Quota Orchestration and respecting Effective Quota in ClusterQueue/Cohort status.
	DynamicQuotaOrchestration featuregate.Feature = "DynamicQuotaOrchestration"
)

func init() {
	runtime.Must(utilfeature.DefaultMutableFeatureGate.AddVersioned(defaultVersionedFeatureGates))
	runtime.Must(utilfeature.DefaultMutableFeatureGate.AddDependencies(defaultFeatureGateDependencies))
}

var defaultFeatureGateDependencies = map[featuregate.Feature][]featuregate.Feature{
	TASFailedNodeReplacement:                     {TopologyAwareScheduling},
	TASFailedNodeReplacementFailFast:             {TopologyAwareScheduling, TASFailedNodeReplacement},
	TASReplaceNodeOnPodTermination:               {TopologyAwareScheduling, TASFailedNodeReplacement},
	TASReplaceNodeDueToNotReadyOverFixedTime:     {TopologyAwareScheduling, TASFailedNodeReplacement},
	TASBalancedPlacement:                         {TopologyAwareScheduling},
	TASReplaceNodeOnNodeTaints:                   {TopologyAwareScheduling},
	TASMultiLayerTopology:                        {TopologyAwareScheduling},
	TASRespectNodeAffinityPreferred:              {TopologyAwareScheduling},
	UnadmittedWorkloadsExplicitStatus:            {UnadmittedWorkloadsObservability},
	TASHandleOverlappingFlavors:                  {TopologyAwareScheduling},
	TASProfileMixed:                              {TopologyAwareScheduling},
	TASRecomputeAssignmentWithinSchedulingCycle:  {TopologyAwareScheduling},
	ElasticJobsViaWorkloadSlicesWithTAS:          {ElasticJobsViaWorkloadSlices, TopologyAwareScheduling},
	KueueDRAIntegrationExtendedResource:          {KueueDRAIntegration},
	KueueDRAIntegrationPartitionableDevices:      {KueueDRAIntegration},
	KueueDRAIntegrationConsumableCapacity:        {KueueDRAIntegration},
	FlavorFungibilityPreserveScanProgress:        {FlavorFungibility},
	SchedulingEquivalenceHashingIgnorePodSetName: {SchedulingEquivalenceHashing},
}

// defaultVersionedFeatureGates consists of all known Kueue-specific feature keys.
// To add a new feature, define a key for it above and add it here. The features will be
// available throughout Kueue binaries.
//
// Entries are separated from each other with blank lines to avoid sweeping gofmt changes
// when adding or removing one entry.
var defaultVersionedFeatureGates = map[featuregate.Feature]featuregate.VersionedSpecs{
	PartialAdmission: {
		{Version: version.MustParse("0.4"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.5"), Default: true, PreRelease: featuregate.Beta},
	},
	ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp: {
		{Version: version.MustParse("0.20"), Default: false, PreRelease: featuregate.Alpha},
	},
	FlavorFungibility: {
		{Version: version.MustParse("0.5"), Default: true, PreRelease: featuregate.Beta},
	},
	VisibilityOnDemand: {
		{Version: version.MustParse("0.6"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.9"), Default: true, PreRelease: featuregate.Beta},
	},
	DisableWaitForPodsReady: {
		{Version: version.MustParse("0.19"), Default: false, PreRelease: featuregate.Alpha},
	},
	PrioritySortingWithinCohort: {
		{Version: version.MustParse("0.6"), Default: true, PreRelease: featuregate.Beta},
	},
	FairSharingPreemptWithinNominal: {
		{Version: version.MustParse("0.17"), Default: true, PreRelease: featuregate.Beta},
	},
	FairSharingPrioritizeNonBorrowing: {
		{Version: version.MustParse("0.17"), Default: true, PreRelease: featuregate.Beta},
	},
	MultiKueue: {
		{Version: version.MustParse("0.6"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.9"), Default: true, PreRelease: featuregate.Beta},
	},
	MultiKueueBatchJobClearingTTLSecondsAfterFinishedOnWorkerCluster: {
		{Version: version.MustParse("0.20"), Default: true, PreRelease: featuregate.Beta},
	},
	TopologyAwareScheduling: {
		{Version: version.MustParse("0.9"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.14"), Default: true, PreRelease: featuregate.Beta},
	},
	LocalQueueMetrics: {
		{Version: version.MustParse("0.10"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.17"), Default: true, PreRelease: featuregate.Beta},
	},
	TASProfileMixed: {
		{Version: version.MustParse("0.10"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.15"), Default: true, PreRelease: featuregate.Beta},
	},
	AdmissionFairSharing: {
		{Version: version.MustParse("0.12"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.15"), Default: true, PreRelease: featuregate.Beta},
	},
	TASFailedNodeReplacement: {
		{Version: version.MustParse("0.12"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.14"), Default: true, PreRelease: featuregate.Beta},
	},
	ElasticJobsViaWorkloadSlices: {
		{Version: version.MustParse("0.13"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.18"), Default: true, PreRelease: featuregate.Beta},
	},
	ElasticJobsViaWorkloadSlicesWithTAS: {
		{Version: version.MustParse("0.17"), Default: false, PreRelease: featuregate.Alpha},
	},
	TASFailedNodeReplacementFailFast: {
		{Version: version.MustParse("0.13"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.14"), Default: true, PreRelease: featuregate.Beta},
	},
	TASReplaceNodeOnPodTermination: {
		{Version: version.MustParse("0.13"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.14"), Default: true, PreRelease: featuregate.Beta},
	},
	SkipReassignmentForPodOwnedWorkloads: {
		{Version: version.MustParse("0.19"), Default: true, PreRelease: featuregate.Beta},
	},
	TASReplaceNodeDueToNotReadyOverFixedTime: {
		{Version: version.MustParse("0.17"), Default: true, PreRelease: featuregate.Beta},
		{Version: version.MustParse("0.19"), Default: false, PreRelease: featuregate.Deprecated},
	},
	ManagedJobsNamespaceSelectorAlwaysRespected: {
		{Version: version.MustParse("0.13"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.15"), Default: true, PreRelease: featuregate.Beta},
		{Version: version.MustParse("0.19"), Default: true, PreRelease: featuregate.GA, LockToDefault: true},
	},
	TASBalancedPlacement: {
		{Version: version.MustParse("0.15"), Default: false, PreRelease: featuregate.Alpha},
	},
	TASAssignmentsEncodingByHostnamePrefix: {
		{Version: version.MustParse("0.19"), Default: true, PreRelease: featuregate.Beta},
	},
	KueueDRAIntegration: {
		{Version: version.MustParse("0.18"), Default: true, PreRelease: featuregate.Beta},
	},
	KueueDRAIntegrationExtendedResource: {
		{Version: version.MustParse("0.18"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.19"), Default: true, PreRelease: featuregate.Beta},
	},
	ReservedResourceNameValidation: {
		{Version: version.MustParse("0.20"), Default: true, PreRelease: featuregate.Beta},
	},

	KueueDRARejectWorkloadsWhenDRADisabled: {
		{Version: version.MustParse("0.18"), Default: true, PreRelease: featuregate.Beta},
	},

	KueueDRAIntegrationPartitionableDevices: {
		{Version: version.MustParse("0.18"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.19"), Default: true, PreRelease: featuregate.Beta},
	},

	KueueDRAIntegrationConsumableCapacity: {
		{Version: version.MustParse("0.19"), Default: false, PreRelease: featuregate.Alpha},
	},

	MultiKueueAdaptersForCustomJobs: {
		{Version: version.MustParse("0.14"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.15"), Default: true, PreRelease: featuregate.Beta},
	},
	WorkloadRequestUseMergePatch: {
		{Version: version.MustParse("0.14"), Default: false, PreRelease: featuregate.Alpha},
	},
	MultiKueueKubeConfigPathValidation: {
		{Version: version.MustParse("0.19"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.20"), Default: true, PreRelease: featuregate.Beta},
	},
	ReclaimablePods: {
		{Version: version.MustParse("0.15"), Default: true, PreRelease: featuregate.Beta},
	},
	MultiKueueClusterProfile: {
		{Version: version.MustParse("0.15"), Default: false, PreRelease: featuregate.Alpha},
	},
	FailureRecoveryPolicy: {
		{Version: version.MustParse("0.15"), Default: false, PreRelease: featuregate.Alpha},
	},
	TLSOptions: {
		{Version: version.MustParse("0.16"), Default: true, PreRelease: featuregate.Beta},                    // GA in 0.20
		{Version: version.MustParse("0.20"), Default: true, PreRelease: featuregate.GA, LockToDefault: true}, // remove in 0.22
	},
	RemoveFinalizersWithStrictPatch: {
		{Version: version.MustParse("0.17"), Default: true, PreRelease: featuregate.Beta},
	},
	TASReplaceNodeOnNodeTaints: {
		{Version: version.MustParse("0.17"), Default: true, PreRelease: featuregate.Beta},
	},
	AssignQueueLabelsForPods: {
		{Version: version.MustParse("0.17"), Default: true, PreRelease: featuregate.Beta},
	},
	TASMultiLayerTopology: {
		{Version: version.MustParse("0.17"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.19"), Default: true, PreRelease: featuregate.Beta},
	},
	SchedulingEquivalenceHashing: {
		{Version: version.MustParse("0.17"), Default: false, PreRelease: featuregate.Beta},
		{Version: version.MustParse("0.18"), Default: true, PreRelease: featuregate.Beta},
	},
	SchedulingEquivalenceHashingIgnorePodSetName: {
		{Version: version.MustParse("0.20"), Default: true, PreRelease: featuregate.Beta},
	},
	CustomMetricLabels: {
		{Version: version.MustParse("0.17"), Default: false, PreRelease: featuregate.Alpha},
	},
	SparkApplicationIntegration: {
		{Version: version.MustParse("0.17"), Default: false, PreRelease: featuregate.Alpha},
	},
	MultiKueueOrchestratedPreemption: {
		{Version: version.MustParse("0.17"), Default: false, PreRelease: featuregate.Alpha},
	},
	PriorityBoost: {
		{Version: version.MustParse("0.17"), Default: false, PreRelease: featuregate.Alpha},
	},
	AdmissionGatedBy: {
		{Version: version.MustParse("0.17"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.19"), Default: true, PreRelease: featuregate.Beta},
	},
	ShortWorkloadNames: {
		{Version: version.MustParse("0.17"), Default: false, PreRelease: featuregate.Alpha},
	},
	FastQuotaReleaseInPodIntegration: {
		{Version: version.MustParse("0.17"), Default: false, PreRelease: featuregate.Alpha},
	},
	RejectUpdatesToCQWithInvalidOnFlavors: {
		{Version: version.MustParse("0.18"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.20"), Default: true, PreRelease: featuregate.Beta},
	},
	FinishOrphanedWorkloads: {
		{Version: version.MustParse("0.18"), Default: true, PreRelease: featuregate.Beta},
	},
	MultiKueueIncrementalDispatcherConfig: {
		{Version: version.MustParse("0.19"), Default: true, PreRelease: featuregate.Beta},
	},
	MultiKueueIncrementalDispatcherRespectConfigOrder: {
		{Version: version.MustParse("0.19"), Default: true, PreRelease: featuregate.Beta},
	},
	ConcurrentAdmission: {
		{Version: version.MustParse("0.18"), Default: false, PreRelease: featuregate.Alpha},
	},
	QuotaCheckStrategy: {
		{Version: version.MustParse("0.18"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.19"), Default: true, PreRelease: featuregate.Beta},
	},
	MetricForWorkloadCreationLatency: {
		{Version: version.MustParse("0.18"), Default: true, PreRelease: featuregate.Beta}, // GA in 0.21
	},
	TASRespectNodeAffinityPreferred: {
		{Version: version.MustParse("0.18"), Default: false, PreRelease: featuregate.Alpha},
	},
	MultiKueueManagerQuotaAutomation: {
		{Version: version.MustParse("0.18"), Default: false, PreRelease: featuregate.Alpha},
	},
	WorkloadIdentifierAnnotations: {
		{Version: version.MustParse("0.18"), Default: true, PreRelease: featuregate.Beta},
	},
	WorkloadPriorityClassDefaulting: {
		{Version: version.MustParse("0.18"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.20"), Default: true, PreRelease: featuregate.Beta},
	},
	MetricsForCohorts: {
		{Version: version.MustParse("0.18"), Default: true, PreRelease: featuregate.Beta},
	},
	CleanupProvisioningRequestsOnEviction: {
		{Version: version.MustParse("0.19"), Default: true, PreRelease: featuregate.Beta},
	},
	TASHandleOverlappingFlavors: {
		{Version: version.MustParse("0.18"), Default: true, PreRelease: featuregate.Beta},
	},
	UnadmittedWorkloadsObservability: {
		{Version: version.MustParse("0.19"), Default: true, PreRelease: featuregate.Beta},
	},

	TASRecomputeAssignmentWithinSchedulingCycle: {
		{Version: version.MustParse("0.19"), Default: true, PreRelease: featuregate.Beta},
	},

	UnadmittedWorkloadsExplicitStatus: {
		{Version: version.MustParse("0.19"), Default: false, PreRelease: featuregate.Beta},
	},

	DeferRayServiceFinalizationForRedisCleanup: {
		{Version: version.MustParse("0.19"), Default: true, PreRelease: featuregate.Beta},
	},

	TASCacheNodeMatchResults: {
		{Version: version.MustParse("0.19"), Default: true, PreRelease: featuregate.Beta},
	},

	TASCachingRemainingResources: {
		{Version: version.MustParse("0.19"), Default: true, PreRelease: featuregate.Beta},
	},

	TASCacheTopologyTree: {
		{Version: version.MustParse("0.20"), Default: true, PreRelease: featuregate.Beta},
	},

	SchedulerLibraryIntegration: {
		{Version: version.MustParse("0.19"), Default: false, PreRelease: featuregate.Alpha},
	},

	VectorizedResourceRequests: {
		{Version: version.MustParse("0.19"), Default: true, PreRelease: featuregate.Beta},
	},

	WorkloadValidateResourcesAreNonNegative: {
		{Version: version.MustParse("0.20"), Default: true, PreRelease: featuregate.Beta},
	},

	TASValidateWorkloadSliceSize: {
		{Version: version.MustParse("0.20"), Default: true, PreRelease: featuregate.Beta}, // GA in 0.21
	},

	WorkloadValidationForPodSetMetadata: {
		{Version: version.MustParse("0.20"), Default: true, PreRelease: featuregate.Beta},
	},

	PrioritizePreemptorWorkloads: {
		{Version: version.MustParse("0.20"), Default: false, PreRelease: featuregate.Alpha},
	},

	LWSImmutableGroupSize: {
		{Version: version.MustParse("0.20"), Default: true, PreRelease: featuregate.Beta},
	},

	FlavorFungibilityPreserveScanProgress: {
		{Version: version.MustParse("0.20"), Default: true, PreRelease: featuregate.Beta},
	},

	SkipAncestorCheckForDeletedWorkloads: {
		{Version: version.MustParse("0.20"), Default: true, PreRelease: featuregate.Beta},
	},

	MultiKueueRemoteSpecSync: {
		{Version: version.MustParse("0.20"), Default: true, PreRelease: featuregate.Beta},
	},

	RecomputeAssignmentUponPreemptionTargetsOverlap: {
		{Version: version.MustParse("0.20"), Default: true, PreRelease: featuregate.Beta},
	},

	PodIntegrationValidateGroupOwner: {
		{Version: version.MustParse("0.18"), Default: true, PreRelease: featuregate.Beta},
	},

	ValidateRayAndSparkJobUpdates: {
		{Version: version.MustParse("0.18"), Default: true, PreRelease: featuregate.Beta}, // GA in 0.21
	},

	FairSharingReevaluatePreemptionCandidates: {
		{Version: version.MustParse("0.20"), Default: false, PreRelease: featuregate.Alpha},
	},

	DynamicQuotaOrchestration: {
		{Version: version.MustParse("0.20"), Default: false, PreRelease: featuregate.Alpha},
	},
}

func SetFeatureGateDuringTest(tb testing.TB, f featuregate.Feature, value bool) {
	SetFeatureGatesDuringTest(tb, map[featuregate.Feature]bool{f: value})
}

func SetFeatureGatesDuringTest(tb testing.TB, featureGates map[featuregate.Feature]bool) {
	originalValues := make(map[string]bool)
	for k := range utilfeature.DefaultMutableFeatureGate.GetAll() {
		originalValues[string(k)] = utilfeature.DefaultFeatureGate.Enabled(k)
	}

	stringMap := make(map[string]bool)
	var enableWithDependencies func(fg featuregate.Feature)
	enableWithDependencies = func(fg featuregate.Feature) {
		stringMap[string(fg)] = true
		for _, dep := range defaultFeatureGateDependencies[fg] {
			enableWithDependencies(dep)
		}
	}

	var disableWithDependents func(fg featuregate.Feature)
	disableWithDependents = func(fg featuregate.Feature) {
		stringMap[string(fg)] = false
		for dependent, parents := range defaultFeatureGateDependencies {
			for _, parent := range parents {
				if parent == fg {
					disableWithDependents(dependent)
				}
			}
		}
	}

	// Populate stringMap deterministically
	for fg, enable := range featureGates {
		if !enable {
			disableWithDependents(fg)
		}
	}
	for fg, enable := range featureGates {
		if enable {
			enableWithDependencies(fg)
		}
	}
	// Re-apply explicit caller choices so caller intent is never silently overwritten
	for fg, enable := range featureGates {
		stringMap[string(fg)] = enable
	}

	if err := utilfeature.DefaultMutableFeatureGate.SetFromMap(stringMap); err != nil {
		tb.Fatalf("Failed to set feature gates: %v", err)
	}

	tb.Cleanup(func() {
		if err := utilfeature.DefaultMutableFeatureGate.SetFromMap(originalValues); err != nil {
			tb.Errorf("error restoring features: %v", err)
		}
	})
}

// Enabled is helper for `utilfeature.DefaultFeatureGate.Enabled()`
func Enabled(f featuregate.Feature) bool {
	return utilfeature.DefaultFeatureGate.Enabled(f)
}

func LogFeatureGates(log logr.Logger) {
	features := make(map[featuregate.Feature]bool, len(defaultVersionedFeatureGates))
	for f := range utilfeature.DefaultMutableFeatureGate.GetAll() {
		if _, ok := defaultVersionedFeatureGates[f]; ok {
			features[f] = Enabled(f)
		}
	}
	log.V(2).Info("Loaded feature gates", "featureGates", features)
}
