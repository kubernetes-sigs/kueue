package features

import (
	"testing"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/runtime"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/component-base/featuregate"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
)

const (
	// owner: @rueian @kevin85421 @andrewsykim
	// rep: https://github.com/ray-project/enhancements/pull/54
	// alpha: v1.2
	// beta: v1.3
	//
	// Enables new conditions in RayCluster status
	RayClusterStatusConditions featuregate.Feature = "RayClusterStatusConditions"

	// owner: @andrewsykim @seanlaii
	// rep: N/A
	// alpha: v1.3
	//
	// Enables new deletion policy API in RayJob
	RayJobDeletionPolicy featuregate.Feature = "RayJobDeletionPolicy"

	// owner: @aaronliang @ryanaoleary
	// rep: N/A
	// alpha: v1.5
	//
	// Enables multi-host worker indexing
	RayMultiHostIndexing featuregate.Feature = "RayMultiHostIndexing"

	// owner: @ryanaoleary
	// rep: https://github.com/ray-project/enhancements/pull/58
	// alpha: v1.5
	//
	// Enabled NewClusterWithIncrementalUpgrade type for RayService zero-downtime upgrades.
	RayServiceIncrementalUpgrade featuregate.Feature = "RayServiceIncrementalUpgrade"

	// owner: @machichima
	// rep: N/A
	// alpha: v1.6
	//
	// Enables RayCronJob controller for scheduled RayJob execution.
	RayCronJob featuregate.Feature = "RayCronJob"

	// owner: @fscnick
	// rep: N/A
	// alpha: v1.6
	//
	// Enables asynchronous job info querying.
	AsyncJobInfoQuery featuregate.Feature = "AsyncJobInfoQuery"
)

func init() {
	runtime.Must(utilfeature.DefaultMutableFeatureGate.Add(defaultFeatureGates))
}

var defaultFeatureGates = map[featuregate.Feature]featuregate.FeatureSpec{
	RayClusterStatusConditions:   {Default: true, PreRelease: featuregate.Beta},
	RayJobDeletionPolicy:         {Default: false, PreRelease: featuregate.Alpha},
	RayMultiHostIndexing:         {Default: false, PreRelease: featuregate.Alpha},
	RayServiceIncrementalUpgrade: {Default: false, PreRelease: featuregate.Alpha},
	RayCronJob:                   {Default: false, PreRelease: featuregate.Alpha},
	AsyncJobInfoQuery:            {Default: false, PreRelease: featuregate.Alpha},
}

// SetFeatureGateDuringTest is a helper method to override feature gates in tests.
func SetFeatureGateDuringTest(tb testing.TB, f featuregate.Feature, value bool) {
	featuregatetesting.SetFeatureGateDuringTest(tb, utilfeature.DefaultFeatureGate, f, value)
}

// Enabled is helper for `utilfeature.DefaultFeatureGate.Enabled()`
func Enabled(f featuregate.Feature) bool {
	return utilfeature.DefaultFeatureGate.Enabled(f)
}

func LogFeatureGates(log logr.Logger) {
	features := make(map[featuregate.Feature]bool, len(defaultFeatureGates))
	for f := range utilfeature.DefaultMutableFeatureGate.GetAll() {
		if _, ok := defaultFeatureGates[f]; ok {
			features[f] = Enabled(f)
		}
	}
	log.Info("Loaded feature gates", "featureGates", features)
}
