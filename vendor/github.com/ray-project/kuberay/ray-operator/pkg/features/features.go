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
	// beta: v1.6
	// Enables new deletion policy API in RayJob
	RayJobDeletionPolicy featuregate.Feature = "RayJobDeletionPolicy"

	// owner: @aaronliang @ryanaoleary
	// rep: N/A
	// alpha: v1.5
	// beta: v1.6
	//
	// Enables multi-host worker indexing
	RayMultiHostIndexing featuregate.Feature = "RayMultiHostIndexing"

	// owner: @ryanaoleary
	// rep: https://github.com/ray-project/enhancements/pull/58
	// alpha: v1.5
	// beta: v1.7
	//
	// Enabled NewClusterWithIncrementalUpgrade type for RayService zero-downtime upgrades.
	RayServiceIncrementalUpgrade featuregate.Feature = "RayServiceIncrementalUpgrade"

	// owner: @machichima
	// rep: N/A
	// alpha: v1.6
	//
	// Enables RayCronJob controller for scheduled RayJob execution.
	RayCronJob featuregate.Feature = "RayCronJob"

	// owner: @justinyeh1995
	// rep: N/A
	// alpha: v1.7
	//
	// Enables per-container restart policy for SidecarMode submitter to handle transient failures.
	// Requires Kubernetes v1.35+ since it supports ContainerRestartPolicy by default starting in v1.35 and Ray v2.54.0+.
	SidecarSubmitterRestart featuregate.Feature = "SidecarSubmitterRestart"

	// owner: @chipspeak @kryanbeane
	// rep: N/A
	// alpha: v1.7
	//
	// Enables NetworkPolicy-based network isolation for RayClusters (spec.networkPolicy).
	RayClusterNetworkPolicy featuregate.Feature = "RayClusterNetworkPolicy"

	// owner: @jhasm
	// rep: https://github.com/ray-project/enhancements/pull/65
	// alpha: v1.7
	//
	// Enables the embedded RocksDB storage backend for GCS fault tolerance
	// (GcsFaultToleranceOptions.Backend: rocksdb). Mirrors the alpha status of the
	// corresponding Ray Core feature (ray-project/ray#63657).
	GCSFaultToleranceEmbeddedStorage featuregate.Feature = "GCSFaultToleranceEmbeddedStorage"

	// owner: @chipspeak @kryanbeane
	// rep: N/A
	// alpha: v1.7
	//
	// Enables mTLS (spec.tlsOptions) for RayClusters via cert-manager.
	RayClusterMTLS featuregate.Feature = "RayClusterMTLS"

	// owner: @chiayi @Future-Outlier
	// rep: N/A
	// alpha: v1.7
	//
	// Enables RayCluster history server collector sidecar injection (spec.historyServerOptions).
	RayClusterHistoryServer featuregate.Feature = "RayClusterHistoryServer"

	// owner: @marosset
	// rep: N/A
	// alpha: v1.7
	//
	// Enables the Kubernetes Workload-Aware Scheduling (WAS) batch scheduler, which gang schedules
	// RayClusters through the in-tree scheduling.k8s.io Workload and PodGroup APIs. Enabling this gate
	// selects the scheduler; it is mutually exclusive with --batch-scheduler and --enable-batch-scheduler.
	// Requires a Kubernetes cluster that serves the scheduling.k8s.io API with GenericWorkload enabled.
	KubernetesWAS featuregate.Feature = "KubernetesWAS"
)

func init() {
	runtime.Must(utilfeature.DefaultMutableFeatureGate.Add(defaultFeatureGates))
}

var defaultFeatureGates = map[featuregate.Feature]featuregate.FeatureSpec{
	RayClusterStatusConditions:       {Default: true, PreRelease: featuregate.Beta},
	RayJobDeletionPolicy:             {Default: true, PreRelease: featuregate.Beta},
	RayMultiHostIndexing:             {Default: true, PreRelease: featuregate.Beta},
	RayServiceIncrementalUpgrade:     {Default: true, PreRelease: featuregate.Beta},
	RayCronJob:                       {Default: false, PreRelease: featuregate.Alpha},
	SidecarSubmitterRestart:          {Default: false, PreRelease: featuregate.Alpha},
	RayClusterNetworkPolicy:          {Default: false, PreRelease: featuregate.Alpha},
	GCSFaultToleranceEmbeddedStorage: {Default: false, PreRelease: featuregate.Alpha},
	RayClusterMTLS:                   {Default: false, PreRelease: featuregate.Alpha},
	RayClusterHistoryServer:          {Default: false, PreRelease: featuregate.Alpha},
	KubernetesWAS:                    {Default: false, PreRelease: featuregate.Alpha},
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
