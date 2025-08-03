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

	// owner: @stuton
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/168-pending-workloads-visibility
	//
	// Enables queue visibility.
	QueueVisibility featuregate.Feature = "QueueVisibility"

	// owner: @KunWuLuan
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/582-preempt-based-on-flavor-order
	//
	// Enables flavor fungibility.
	FlavorFungibility featuregate.Feature = "FlavorFungibility"

	// owner: @trasc
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/1136-provisioning-request-support
	//
	// Enables Provisioning Admission Check Controller.
	ProvisioningACC featuregate.Feature = "ProvisioningACC"

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

	// owner: @dgrove-oss
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/2937-resource-transformer
	//
	// Enable applying configurable resource transformations when computing
	// the resource requests of a Workload
	ConfigurableResourceTransformations featuregate.Feature = "ConfigurableResourceTransformations"

	// owner: @mbobrovskyi
	//
	// Enable the Flavors status field in the LocalQueue, allowing users to view
	// all currently available ResourceFlavors for the LocalQueue.
	ExposeFlavorsInLocalQueue featuregate.Feature = "ExposeFlavorsInLocalQueue"

	// owner: @dgrove-oss
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/3589-manage-jobs-selectively
	//
	// Enable namespace-based control of manageJobsWithoutQueueNames for all Job integrations
	ManagedJobsNamespaceSelector featuregate.Feature = "ManagedJobsNamespaceSelector"

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
	// Enable to set use LeastFreeCapacity algorithm for TAS
	TASProfileLeastFreeCapacity featuregate.Feature = "TASProfileLeastFreeCapacity"

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
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/582-preempt-based-on-flavor-order
	//
	// In flavor fungibility, the preference whether to preempt or borrow is inferred from flavor fungibility policy
	// This feature gate is going to be replaced by an API before graduation or deprecation.
	FlavorFungibilityImplicitPreferenceDefault featuregate.Feature = "FlavorFungibilityImplicitPreferenceDefault"
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
	QueueVisibility: {
		{Version: version.MustParse("0.5"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.9"), Default: false, PreRelease: featuregate.Deprecated},
	},
	FlavorFungibility: {
		{Version: version.MustParse("0.5"), Default: true, PreRelease: featuregate.Beta},
	},
	ProvisioningACC: {
		{Version: version.MustParse("0.5"), Default: true, PreRelease: featuregate.Beta},
		{Version: version.MustParse("0.14"), Default: true, PreRelease: featuregate.GA, LockToDefault: true}, // remove in 0.15
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
	},
	MultiKueueBatchJobWithManagedBy: {
		{Version: version.MustParse("0.8"), Default: false, PreRelease: featuregate.Alpha},
	},
	TopologyAwareScheduling: {
		{Version: version.MustParse("0.9"), Default: false, PreRelease: featuregate.Alpha},
	},
	ConfigurableResourceTransformations: {
		{Version: version.MustParse("0.9"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.10"), Default: true, PreRelease: featuregate.Beta},
	},
	ExposeFlavorsInLocalQueue: {
		{Version: version.MustParse("0.9"), Default: true, PreRelease: featuregate.Beta},
	},
	ManagedJobsNamespaceSelector: {
		{Version: version.MustParse("0.10"), Default: true, PreRelease: featuregate.Beta},
		{Version: version.MustParse("0.13"), Default: true, PreRelease: featuregate.GA, LockToDefault: true}, // remove in 0.15
	},
	LocalQueueMetrics: {
		{Version: version.MustParse("0.10"), Default: false, PreRelease: featuregate.Alpha},
	},
	LocalQueueDefaulting: {
		{Version: version.MustParse("0.10"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.12"), Default: true, PreRelease: featuregate.Beta},
	},
	TASProfileLeastFreeCapacity: {
		{Version: version.MustParse("0.10"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.11"), Default: false, PreRelease: featuregate.Deprecated},
	},
	TASProfileMixed: {
		{Version: version.MustParse("0.10"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.11"), Default: false, PreRelease: featuregate.Deprecated},
	},
	HierarchicalCohorts: {
		{Version: version.MustParse("0.11"), Default: true, PreRelease: featuregate.Beta},
	},
	AdmissionFairSharing: {
		{Version: version.MustParse("0.12"), Default: false, PreRelease: featuregate.Alpha},
	},
	ObjectRetentionPolicies: {
		{Version: version.MustParse("0.12"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.13"), Default: true, PreRelease: featuregate.Beta},
	},
	TASFailedNodeReplacement: {
		{Version: version.MustParse("0.12"), Default: false, PreRelease: featuregate.Alpha},
	},
	ElasticJobsViaWorkloadSlices: {
		{Version: version.MustParse("0.13"), Default: false, PreRelease: featuregate.Alpha},
	},
	TASFailedNodeReplacementFailFast: {
		{Version: version.MustParse("0.13"), Default: false, PreRelease: featuregate.Alpha},
	},
	TASReplaceNodeOnPodTermination: {
		{Version: version.MustParse("0.13"), Default: false, PreRelease: featuregate.Alpha},
	},
	ManagedJobsNamespaceSelectorAlwaysRespected: {
		{Version: version.MustParse("0.13"), Default: false, PreRelease: featuregate.Alpha},
	},
	FlavorFungibilityImplicitPreferenceDefault: {
		{Version: version.MustParse("0.13"), Default: false, PreRelease: featuregate.Alpha},
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
