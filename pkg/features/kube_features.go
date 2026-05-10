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

	// owners: @B1F030, @kerthcet
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/1224-lending-limit
	//
	// Enables lending limit.
	LendingLimit featuregate.Feature = "LendingLimit"

	// owner: @trasc
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/693-multikueue
	//
	// Enable the usage of batch.Job spec.managedBy field its MultiKueue integration.
	MultiKueueBatchJobWithManagedBy featuregate.Feature = "MultiKueueBatchJobWithManagedBy"

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

	// owner: @yaroslava-serdiuk
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/2936-local-queue-defaulting
	//
	// Enable to set default LocalQueue.
	LocalQueueDefaulting featuregate.Feature = "LocalQueueDefaulting"

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

	// owner: @alaypatel07
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/2941-DRA
	//
	// Enable quota accounting for Dynamic Resource Allocation (DRA) devices in workloads
	DynamicResourceAllocation featuregate.Feature = "DynamicResourceAllocation"

	// owner: @sohankunkerkar
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/2941-DRA
	//
	// Enable extended resources support for DRA. Allows workloads to request DRA devices
	// via standard resources.requests using DeviceClass extendedResourceName.
	DRAExtendedResources featuregate.Feature = "DRAExtendedResources"

	// owner: @MaysaMacedo
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/7513-quota-check-strategy
	//
	// Enable QuotaCheckStrategy for quota admission.
	QuotaCheckStrategy featuregate.Feature = "QuotaCheckStrategy"

	// owner: @khrm
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/2349-multikueue-external-custom-job-support
	//
	// Enable MultiKueue support for external custom Jobs via configurable adapters.
	MultiKueueAdaptersForCustomJobs featuregate.Feature = "MultiKueueAdaptersForCustomJobs"

	// owner: @mszadkow
	//
	// Enable all updates to Workload objects to use Patch Merge instead of Patch Apply.
	WorkloadRequestUseMergePatch featuregate.Feature = "WorkloadRequestUseMergePatch"

	// owner: @mbobrovskyi
	//
	// SanitizePodSets enables automatic sanitization of PodSets when creating the Workload object.
	// The main use case it deduplication of environment variables
	// in PodSet templates within Workload objects. When enabled, duplicate env var entries
	// are resolved by keeping only the last occurrence, allowing workload creation to succeed
	// even when duplicates are present in the spec.
	SanitizePodSets featuregate.Feature = "SanitizePodSets"

	// owner: @mszadkow
	//
	// Allow insecure kubeconfigs in MultiKueue setup.
	// Requires careful consideration as it may lead to security issues.
	//
	// Deprecated: planned to be removed in 0.17
	MultiKueueAllowInsecureKubeconfigs featuregate.Feature = "MultiKueueAllowInsecureKubeconfigs"

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
	// Enables TLSOptions for TLS MinVersion and CipherSuites for kueue servers
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

	// owner: @pbundyra
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/8691-concurrent-admission
	//
	// Enables Concurrent Admission feature which allows pursuing multiple ResourceFlavors in parallel.
	ConcurrentAdmission featuregate.Feature = "ConcurrentAdmission"

	// Enable recording of WorkloadCreationLatency metric.
	MetricForWorkloadCreationLatency featuregate.Feature = "MetricForWorkloadCreationLatency"
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
	LendingLimit: {
		{Version: version.MustParse("0.6"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.9"), Default: true, PreRelease: featuregate.Beta},
		{Version: version.MustParse("0.17"), Default: true, PreRelease: featuregate.GA, LockToDefault: true}, // remove in 0.19
	},
	MultiKueueBatchJobWithManagedBy: {
		{Version: version.MustParse("0.8"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.15"), Default: true, PreRelease: featuregate.Beta},
		{Version: version.MustParse("0.17"), Default: true, PreRelease: featuregate.GA, LockToDefault: true}, // remove in 0.19
	},
	TopologyAwareScheduling: {
		{Version: version.MustParse("0.9"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.14"), Default: true, PreRelease: featuregate.Beta},
	},
	LocalQueueMetrics: {
		{Version: version.MustParse("0.10"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.17"), Default: true, PreRelease: featuregate.Beta},
	},
	LocalQueueDefaulting: {
		{Version: version.MustParse("0.10"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.12"), Default: true, PreRelease: featuregate.Beta},
		{Version: version.MustParse("0.17"), Default: true, PreRelease: featuregate.GA, LockToDefault: true}, // remove in 0.19
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
	ManagedJobsNamespaceSelectorAlwaysRespected: {
		{Version: version.MustParse("0.13"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.15"), Default: true, PreRelease: featuregate.Beta},
	},
	TASBalancedPlacement: {
		{Version: version.MustParse("0.15"), Default: false, PreRelease: featuregate.Alpha},
	},
	DynamicResourceAllocation: {
		{Version: version.MustParse("0.14"), Default: false, PreRelease: featuregate.Alpha},
	},
	DRAExtendedResources: {
		{Version: version.MustParse("0.17"), Default: false, PreRelease: featuregate.Alpha},
	},
	MultiKueueAdaptersForCustomJobs: {
		{Version: version.MustParse("0.14"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.15"), Default: true, PreRelease: featuregate.Beta},
	},
	WorkloadRequestUseMergePatch: {
		{Version: version.MustParse("0.14"), Default: false, PreRelease: featuregate.Alpha},
	},
	SanitizePodSets: {
		{Version: version.MustParse("0.13"), Default: true, PreRelease: featuregate.Beta},
		{Version: version.MustParse("0.17"), Default: true, PreRelease: featuregate.GA, LockToDefault: true}, // remove in 0.19
	},
	MultiKueueAllowInsecureKubeconfigs: {
		{Version: version.MustParse("0.15"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.17"), Default: false, PreRelease: featuregate.Deprecated}, // remove in 0.19
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
		{Version: version.MustParse("0.16"), Default: true, PreRelease: featuregate.Beta}, // GA in 0.20 (https://github.com/kubernetes-sigs/kueue/issues/10704)
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
	},
	SchedulingEquivalenceHashing: {
		{Version: version.MustParse("0.17"), Default: false, PreRelease: featuregate.Beta},
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
		{Version: version.MustParse("0.18"), Default: false, PreRelease: featuregate.Alpha},
	},
	ConcurrentAdmission: {
		{Version: version.MustParse("0.18"), Default: false, PreRelease: featuregate.Alpha},
	},
	QuotaCheckStrategy: {
		{Version: version.MustParse("0.18"), Default: false, PreRelease: featuregate.Alpha},
	},
	MetricForWorkloadCreationLatency: {
		{Version: version.MustParse("0.18"), Default: true, PreRelease: featuregate.Beta}, // GA in 0.21
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
