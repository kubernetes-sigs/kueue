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
	"fmt"
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
	// Enable quota accounting for Dynamic Resource Allocation (DRA) devies in workloads
	DynamicResourceAllocation featuregate.Feature = "DynamicResourceAllocation"

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
		{Version: version.MustParse("0.17"), Default: true, PreRelease: featuregate.GA},
	},
	MultiKueueClusterProfile: {
		{Version: version.MustParse("0.15"), Default: false, PreRelease: featuregate.Alpha},
	},
	FailureRecoveryPolicy: {
		{Version: version.MustParse("0.15"), Default: false, PreRelease: featuregate.Alpha},
	},
	SkipFinalizersForPodsSuspendedByParent: {
		{Version: version.MustParse("0.16"), Default: true, PreRelease: featuregate.Beta}, // GA in 0.18
	},
	MultiKueueWaitForWorkloadAdmitted: {
		{Version: version.MustParse("0.16"), Default: true, PreRelease: featuregate.Beta}, // GA in 0.18
	},
	MultiKueueRedoAdmissionOnEvictionInWorker: {
		{Version: version.MustParse("0.16"), Default: true, PreRelease: featuregate.Beta}, // GA in 0.18
	},
	TLSOptions: {
		{Version: version.MustParse("0.16"), Default: true, PreRelease: featuregate.Beta}, // GA in 0.18
	},
	RemoveFinalizersWithStrictPatch: {
		{Version: version.MustParse("0.17"), Default: true, PreRelease: featuregate.Beta},
	},
	TASReplaceNodeOnNodeTaints: {
		{Version: version.MustParse("0.17"), Default: false, PreRelease: featuregate.Alpha},
	},
	AssignQueueLabelsForPods: {
		{Version: version.MustParse("0.17"), Default: true, PreRelease: featuregate.Beta},
	},
}

func SetFeatureGateDuringTest(tb testing.TB, f featuregate.Feature, value bool) {
	featuregatetesting.SetFeatureGateDuringTest(tb, utilfeature.DefaultFeatureGate, f, value)
}

// Enabled is helper for `utilfeature.DefaultFeatureGate.Enabled()`
func Enabled(f featuregate.Feature) bool {
	return utilfeature.DefaultFeatureGate.Enabled(f)
}

// SetEnable helper function that can be used to set the enabled value of a feature gate,
// it should only be used in integration test pending the merge of
// https://github.com/kubernetes/kubernetes/pull/118346
func SetEnable(f featuregate.Feature, v bool) error {
	return utilfeature.DefaultMutableFeatureGate.Set(fmt.Sprintf("%s=%v", f, v))
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
