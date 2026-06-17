package util

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"k8s.io/apiserver/pkg/util/feature"
	schedulerMetrics "k8s.io/kubernetes/pkg/scheduler/metrics"
	"sigs.k8s.io/kueue/pkg/features"
)

func SchedulerLibraryTASWrapper(tests func()) func() {
	return func() {
		for _, enableSchedulerLibraryIntegration := range []bool{false, true} {
			ginkgo.Context(fmt.Sprintf("with SchedulerLibraryIntegration=%v", enableSchedulerLibraryIntegration), func() {
				ginkgo.BeforeAll(func() {
					err := feature.DefaultMutableFeatureGate.Set(fmt.Sprintf("%s=%v", features.SchedulerLibraryIntegration, enableSchedulerLibraryIntegration))
					if err != nil {
						panic(fmt.Sprintf("Failed to set feature gate: %v", err))
					}
					if enableSchedulerLibraryIntegration {
						// Make sure that the scheduler metrics are registered, as the `scheduler-library` only registers them when
						// calling `NewClusterState`.
						// TODO: Remove once the code uses `NewClusterState` or metrics management is revamped in `scheduler-library`.
						schedulerMetrics.Register()
					}
				})

				tests()
			})
		}
	}
}
