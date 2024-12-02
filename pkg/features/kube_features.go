/*
Copyright 2023 The Kubernetes Authors.

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
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/component-base/featuregate"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
)

const (
	// owner: @trasc
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/420-partial-admission
	// alpha: v0.4
	// beta: v0.5
	//
	// Enables partial admission.
	PartialAdmission featuregate.Feature = "PartialAdmission"

	// owner: @stuton
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/168-pending-workloads-visibility
	// alpha: v0.5
	// Deprecated: v0.9
	//
	// Enables queue visibility.
	QueueVisibility featuregate.Feature = "QueueVisibility"

	// owner: @KunWuLuan
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/582-preempt-based-on-flavor-order
	// beta: v0.5
	//
	// Enables flavor fungibility.
	FlavorFungibility featuregate.Feature = "FlavorFungibility"

	// owner: @trasc
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/1136-provisioning-request-support
	// alpha: v0.5
	//
	// Enables Provisioning Admission Check Controller.
	ProvisioningACC featuregate.Feature = "ProvisioningACC"

	// owner: @pbundyra
	// kep: https://github.com/kubernetes-sigs/kueue/pull/1300
	// alpha: v0.6
	// beta: v0.9
	//
	// Enables Kueue visibility on demand
	VisibilityOnDemand featuregate.Feature = "VisibilityOnDemand"

	// owner: @yaroslava-serdiuk
	// kep: https://github.com/kubernetes-sigs/kueue/issues/1283
	// beta: v0.6
	//
	// Enable priority sorting within the cohort.
	PrioritySortingWithinCohort featuregate.Feature = "PrioritySortingWithinCohort"

	// owner: @trasc
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/693-multikueue
	// alpha: v0.6
	// beta: v0.9
	//
	// Enables MultiKueue support.
	MultiKueue featuregate.Feature = "MultiKueue"

	// owners: @B1F030, @kerthcet
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/1224-lending-limit
	// alpha: v0.6
	// beta: v0.9
	//
	// Enables lending limit.
	LendingLimit featuregate.Feature = "LendingLimit"

	// owner: @trasc
	// alpha: v0.8
	//
	// Enable the usage of batch.Job spec.managedBy field its MultiKueue integration.
	MultiKueueBatchJobWithManagedBy featuregate.Feature = "MultiKueueBatchJobWithManagedBy"

	// owner: @gabesaba
	// alpha: v0.8
	// beta: v0.9
	// stable: v0.10
	//
	// remove in v0.12
	//
	// Enable more than one workload sharing flavors to preempt within a Cohort,
	// as long as the preemption targets don't overlap.
	MultiplePreemptions featuregate.Feature = "MultiplePreemptions"

	// owner: @mimowo
	// alpha: v0.9
	//
	// Enable Topology Aware Scheduling allowing to optimize placement of Pods
	// to put them on closely located nodes (e.g. within the same rack or block).
	TopologyAwareScheduling featuregate.Feature = "TopologyAwareScheduling"

	// owner: @dgrove-oss
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/2937-resource-transformer
	// alpha: v0.9
	// beta: v0.10
	//
	// Enable applying configurable resource transformations when computing
	// the resource requests of a Workload
	ConfigurableResourceTransformations featuregate.Feature = "ConfigurableResourceTransformations"

	// owner: @dgrove-oss
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/2937-resource-transformer
	// alpha: v0.9
	// beta: v0.10
	//
	// Summarize the resource requests of non-admitted Workloads in Workload.Status.resourceRequest
	// to improve observability
	WorkloadResourceRequestsSummary featuregate.Feature = "WorkloadResourceRequestsSummary"

	// owner: @mbobrovskyi
	// beta: v0.9
	//
	// Enable the Flavors status field in the LocalQueue, allowing users to view
	// all currently available ResourceFlavors for the LocalQueue.
	ExposeFlavorsInLocalQueue featuregate.Feature = "ExposeFlavorsInLocalQueue"

	// owner: @mszadkow
	// alpha: v0.9
	// Deprecated: v0.9
	//
	// Enable additional AdmissionCheck validation rules that will appear in status conditions.
	AdmissionCheckValidationRules featuregate.Feature = "AdmissionCheckValidationRules"

	// owner: @pbundyra
	// alpha: v0.9
	// Deprecated: v0.9
	//
	// Workloads keeps allocated quota and preserves QuotaReserved=True when ProvisioningRequest fails
	KeepQuotaForProvReqRetry featuregate.Feature = "KeepQuotaForProvReqRetry"

	// owner: @dgrove-oss
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/3589-manage-jobs-selectively
	// beta: v0.10
	//
	// Enable namespace-based control of manageJobsWithoutQueueNames for all Job integrations
	ManagedJobsNamespaceSelector featuregate.Feature = "ManagedJobsNamespaceSelector"

	// owner: @kpostoffice
	// alpha: v0.10
	//
	// Enabled gathering of LocalQueue metrics
	LocalQueueMetrics featuregate.Feature = "LocalQueueMetrics"
)

func init() {
	runtime.Must(utilfeature.DefaultMutableFeatureGate.Add(defaultFeatureGates))
}

// defaultFeatureGates consists of all known Kueue-specific feature keys.
// To add a new feature, define a key for it above and add it here. The features will be
// available throughout Kueue binaries.
//
// Entries are separated from each other with blank lines to avoid sweeping gofmt changes
// when adding or removing one entry.
var defaultFeatureGates = map[featuregate.Feature]featuregate.FeatureSpec{
	PartialAdmission:                    {Default: true, PreRelease: featuregate.Beta},
	QueueVisibility:                     {Default: false, PreRelease: featuregate.Deprecated},
	FlavorFungibility:                   {Default: true, PreRelease: featuregate.Beta},
	ProvisioningACC:                     {Default: true, PreRelease: featuregate.Beta},
	VisibilityOnDemand:                  {Default: true, PreRelease: featuregate.Beta},
	PrioritySortingWithinCohort:         {Default: true, PreRelease: featuregate.Beta},
	MultiKueue:                          {Default: true, PreRelease: featuregate.Beta},
	LendingLimit:                        {Default: true, PreRelease: featuregate.Beta},
	MultiKueueBatchJobWithManagedBy:     {Default: false, PreRelease: featuregate.Alpha},
	MultiplePreemptions:                 {Default: true, PreRelease: featuregate.GA},
	TopologyAwareScheduling:             {Default: false, PreRelease: featuregate.Alpha},
	ConfigurableResourceTransformations: {Default: true, PreRelease: featuregate.Beta},
	WorkloadResourceRequestsSummary:     {Default: true, PreRelease: featuregate.Beta},
	ExposeFlavorsInLocalQueue:           {Default: true, PreRelease: featuregate.Beta},
	AdmissionCheckValidationRules:       {Default: false, PreRelease: featuregate.Deprecated},
	KeepQuotaForProvReqRetry:            {Default: false, PreRelease: featuregate.Deprecated},
	ManagedJobsNamespaceSelector:        {Default: true, PreRelease: featuregate.Beta},
	LocalQueueMetrics:                   {Default: false, PreRelease: featuregate.Alpha},
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
	features := make(map[featuregate.Feature]bool, len(defaultFeatureGates))
	for f := range utilfeature.DefaultMutableFeatureGate.GetAll() {
		if _, ok := defaultFeatureGates[f]; ok {
			features[f] = Enabled(f)
		}
	}
	log.V(2).Info("Loaded feature gates", "featureGates", features)
}
