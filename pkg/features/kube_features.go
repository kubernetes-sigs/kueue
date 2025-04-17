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

	// owner: @gabesaba
	// kep: https://github.com/kubernetes-sigs/kueue/issues/2596
	//
	// Enable more than one workload sharing flavors to preempt within a Cohort,
	// as long as the preemption targets don't overlap.
	MultiplePreemptions featuregate.Feature = "MultiplePreemptions"

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

	// owner: @dgrove-oss
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/2937-resource-transformer
	//
	// Summarize the resource requests of non-admitted Workloads in Workload.Status.resourceRequest
	// to improve observability
	WorkloadResourceRequestsSummary featuregate.Feature = "WorkloadResourceRequestsSummary"

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
	// Enable to set use LeastAlloactedFit algorithm for TAS
	TASProfileMostFreeCapacity featuregate.Feature = "TASProfileMostFreeCapacity"

	// owner: @pbundyra
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/2724-topology-aware-scheduling
	//
	// Enable to set use LeastAlloactedFit algorithm for TAS
	TASProfileLeastFreeCapacity featuregate.Feature = "TASProfileLeastFreeCapacity"

	// owner: @pbundyra
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/2724-topology-aware-scheduling
	//
	// Enable to set use LeastAlloactedFit algorithm for TAS
	TASProfileMixed featuregate.Feature = "TASProfileMixed"

	// owner: @mwielgus
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/79-hierarchical-cohorts
	//
	// Enable hierarchical cohorts
	HierarchicalCohorts featuregate.Feature = "HierarchicalCohorts"
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
	MultiplePreemptions: {
		{Version: version.MustParse("0.8"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.9"), Default: true, PreRelease: featuregate.Beta},
		{Version: version.MustParse("0.10"), Default: true, PreRelease: featuregate.GA, LockToDefault: true}, // remove in 0.12
	},
	TopologyAwareScheduling: {
		{Version: version.MustParse("0.9"), Default: false, PreRelease: featuregate.Alpha},
	},
	ConfigurableResourceTransformations: {
		{Version: version.MustParse("0.9"), Default: false, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.10"), Default: true, PreRelease: featuregate.Beta},
	},
	WorkloadResourceRequestsSummary: {
		{Version: version.MustParse("0.9"), Default: true, PreRelease: featuregate.Alpha},
		{Version: version.MustParse("0.10"), Default: true, PreRelease: featuregate.Beta},
		{Version: version.MustParse("0.11"), Default: true, PreRelease: featuregate.GA, LockToDefault: true}, // remove in 0.13
	},
	ExposeFlavorsInLocalQueue: {
		{Version: version.MustParse("0.9"), Default: true, PreRelease: featuregate.Beta},
	},
	ManagedJobsNamespaceSelector: {
		{Version: version.MustParse("0.10"), Default: true, PreRelease: featuregate.Beta},
	},
	LocalQueueMetrics: {
		{Version: version.MustParse("0.10"), Default: false, PreRelease: featuregate.Alpha},
	},
	LocalQueueDefaulting: {
		{Version: version.MustParse("0.10"), Default: false, PreRelease: featuregate.Alpha},
	},
	TASProfileMostFreeCapacity: {
		{Version: version.MustParse("0.11"), Default: false, PreRelease: featuregate.Deprecated},
	},
	TASProfileLeastFreeCapacity: {
		{Version: version.MustParse("0.11"), Default: false, PreRelease: featuregate.Deprecated},
	},
	TASProfileMixed: {
		{Version: version.MustParse("0.11"), Default: false, PreRelease: featuregate.Deprecated},
	},
	HierarchicalCohorts: {
		{Version: version.MustParse("0.11"), Default: true, PreRelease: featuregate.Beta},
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
