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
	//
	// Enables MultiKueue support.
	MultiKueue featuregate.Feature = "MultiKueue"

	// owners: @B1F030, @kerthcet
	// kep: https://github.com/kubernetes-sigs/kueue/tree/main/keps/1224-lending-limit
	// alpha: v0.6
	//
	// Enables lending limit.
	LendingLimit featuregate.Feature = "LendingLimit"
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
	PartialAdmission:            {Default: true, PreRelease: featuregate.Beta},
	QueueVisibility:             {Default: false, PreRelease: featuregate.Alpha},
	FlavorFungibility:           {Default: true, PreRelease: featuregate.Beta},
	ProvisioningACC:             {Default: false, PreRelease: featuregate.Alpha},
	VisibilityOnDemand:          {Default: false, PreRelease: featuregate.Alpha},
	PrioritySortingWithinCohort: {Default: true, PreRelease: featuregate.Beta},
	MultiKueue:                  {Default: false, PreRelease: featuregate.Alpha},
	LendingLimit:                {Default: false, PreRelease: featuregate.Alpha},
}

func SetFeatureGateDuringTest(tb testing.TB, f featuregate.Feature, value bool) func() {
	return featuregatetesting.SetFeatureGateDuringTest(tb, utilfeature.DefaultFeatureGate, f, value)
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
