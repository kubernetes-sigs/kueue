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
	featuregatetesting "k8s.io/component-base/featuregate/testing"
)

const (
	// owner: @trasc
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/420-partial-admission
	//
	// Enables partial admission.
	PartialAdmission featuregate.Feature = "PartialAdmission"

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

	// owner: @mwielgus
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/79-hierarchical-cohorts
	//
	// Enable hierarchical cohorts
	HierarchicalCohorts featuregate.Feature = "HierarchicalCohorts"

	// owner: @pbundyra
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/4136-admission-fair-sharing
	//
	// Enable admission fair sharing
	AdmissionFairSharing featuregate.Feature = "AdmissionFairSharing"

	// owner: @mwysokin @mykysha @mbobrovskyi
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/1618-optional-gc-of-workloads
	//
	// Enable object retentions
	ObjectRetentionPolicies featuregate.Feature = "ObjectRetentionPolicies"

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

	// owner: @mszadkow
	//
	// Allow insecure kubeconfigs in MultiKueue setup.
	// Requires careful consideration as it may lead to security issues.
	//
	// Deprecated: locked to its default value (false) in 0.19; planned to be removed in 0.20.
	MultiKueueAllowInsecureKubeconfigs featuregate.Feature = "MultiKueueAllowInsecureKubeconfigs"

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

	// owner: @yaroslva-serdiuk
	//
	// issue: https://github.com/kubernetes-sigs/kueue/issues/7597
	// Do not remove job-name label from Workload PodTemplate object.
	PropagateBatchJobLabelsToWorkload featuregate.Feature = "PropagateBatchJobLabelsToWorkload"

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

	// owner: @mbobrovskyi
	//
	// issue: https://github.com/kubernetes-sigs/kueue/issues/5298
	// Enabled skip adding finalizers for serving workloads.
	SkipFinalizersForPodsSuspendedByParent featuregate.Feature = "SkipFinalizersForPodsSuspendedByParent"

	// owner: @IrvingMg
	//
	// issue: https://github.com/kubernetes-sigs/kueue/issues/8585
	// Enable waiting for WorkloadAdmitted before cleaning up non-selected worker workloads.
	MultiKueueWaitForWorkloadAdmitted featuregate.Feature = "MultiKueueWaitForWorkloadAdmitted"

	// owner: @mszadkow
	//
	// issue: https://github.com/kubernetes-sigs/kueue/issues/8302
	// Redo admission on eviction in worker cluster.
	MultiKueueRedoAdmissionOnEvictionInWorker featuregate.Feature = "MultiKueueRedoAdmissionOnEvictionInWorker"

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

	// owner: @mbobrovskyi
	//
	// issue: https://github.com/kubernetes-sigs/kueue/issues/9799
	// Use 10s interval for scheduler requeuing.
	SchedulerLongRequeueInterval featuregate.Feature = "SchedulerLongRequeueInterval"

	// owner: @mbobrovskyi
	//
	// issue: https://github.com/kubernetes-sigs/kueue/issues/9799
	// Use a 5min buffer so that workloads with scheduling timestamps within this
	// buffer do not preempt each other based on LowerOrNewerEqualPriority.
	SchedulerTimestampPreemptionBuffer featuregate.Feature = "SchedulerTimestampPreemptionBuffer"

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
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/7990-preemption-cost
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
)

func init() {
	runtime.Must(utilfeature.DefaultMutableFeatureGate.AddVersioned(defaultVersionedFeatureGates))
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
	HierarchicalCohorts: {
		{Version: version.MustParse("0.11"), Default: true, PreRelease: featuregate.Beta},
		{Version: version.MustParse("0.17"), Default: true, PreRelease: featuregate.GA, LockToDefault: true}, // remove in 0.19
	},
	AdmissionFairSharing: {
		{Version: version.MustParse("0.12"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.15"), Default: true, PreRelease: featuregate.Beta},
	},
	ObjectRetentionPolicies: {
		{Version: version.MustParse("0.12"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.13"), Default: true, PreRelease: featuregate.Beta},
		{Version: version.MustParse("0.17"), Default: true, PreRelease: featuregate.GA, LockToDefault: true}, // remove in 0.19
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
	MultiKueueAllowInsecureKubeconfigs: {
		{Version: version.MustParse("0.15"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.17"), Default: false, PreRelease: featuregate.Deprecated},
		{Version: version.MustParse("0.19"), Default: false, PreRelease: featuregate.Deprecated, LockToDefault: true}, // remove in 0.20
	},

	MultiKueueKubeConfigPathValidation: {
		{Version: version.MustParse("0.19"), Default: false, PreRelease: featuregate.Alpha},
	},
	ReclaimablePods: {
		{Version: version.MustParse("0.15"), Default: true, PreRelease: featuregate.Beta},
	},
	// PropagateBatchJobLabelsToWorkload is enabled from 0.13.10 and 0.14.5.
	PropagateBatchJobLabelsToWorkload: {
		{Version: version.MustParse("0.15"), Default: true, PreRelease: featuregate.Beta},
		{Version: version.MustParse("0.17"), Default: true, PreRelease: featuregate.GA, LockToDefault: true}, // remove in 0.19
	},
	MultiKueueClusterProfile: {
		{Version: version.MustParse("0.15"), Default: false, PreRelease: featuregate.Alpha},
	},
	FailureRecoveryPolicy: {
		{Version: version.MustParse("0.15"), Default: false, PreRelease: featuregate.Alpha},
	},
	SkipFinalizersForPodsSuspendedByParent: {
		{Version: version.MustParse("0.16"), Default: true, PreRelease: featuregate.Beta},                    // GA in 0.18
		{Version: version.MustParse("0.18"), Default: true, PreRelease: featuregate.GA, LockToDefault: true}, // remove in 0.20
	},
	MultiKueueWaitForWorkloadAdmitted: {
		{Version: version.MustParse("0.16"), Default: true, PreRelease: featuregate.Beta},                    // GA in 0.18
		{Version: version.MustParse("0.18"), Default: true, PreRelease: featuregate.GA, LockToDefault: true}, // remove in 0.20
	},
	MultiKueueRedoAdmissionOnEvictionInWorker: {
		{Version: version.MustParse("0.16"), Default: true, PreRelease: featuregate.Beta},                    // GA in 0.18
		{Version: version.MustParse("0.18"), Default: true, PreRelease: featuregate.GA, LockToDefault: true}, // remove in 0.20
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
	SchedulerLongRequeueInterval: {
		{Version: version.MustParse("0.17"), Default: false, PreRelease: featuregate.Alpha}, // remove in 0.20
	},
	SchedulerTimestampPreemptionBuffer: {
		{Version: version.MustParse("0.17"), Default: false, PreRelease: featuregate.Alpha}, // remove in 0.20
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
		{Version: version.MustParse("0.19"), Default: false, PreRelease: featuregate.Beta},
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

	SchedulerLibraryIntegration: {
		{Version: version.MustParse("0.19"), Default: false, PreRelease: featuregate.Alpha},
	},

	VectorizedResourceRequests: {
		{Version: version.MustParse("0.19"), Default: true, PreRelease: featuregate.Beta},
	},

	WorkloadValidateResourcesAreNonNegative: {
		{Version: version.MustParse("0.20"), Default: true, PreRelease: featuregate.Beta},
	},
}

func SetFeatureGateDuringTest(tb testing.TB, f featuregate.Feature, value bool) {
	featuregatetesting.SetFeatureGateDuringTest(tb, utilfeature.DefaultFeatureGate, f, value)
}

func SetFeatureGatesDuringTest(tb testing.TB, featureGates map[featuregate.Feature]bool) {
	for fg, enable := range featureGates {
		featuregatetesting.SetFeatureGateDuringTest(tb, utilfeature.DefaultFeatureGate, fg, enable)
	}
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
