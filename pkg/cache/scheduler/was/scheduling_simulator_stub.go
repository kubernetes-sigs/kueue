//go:build exclude_scheduler_library

package was

import (
	"context"
	"fmt"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
)

func NewWASSimulator(ctx context.Context, restConfig *rest.Config) (simulator.SchedulingSimulator, error) {
	return nil, fmt.Errorf("scheduler-library integration is compiled out of this binary. Disable the SchedulerLibraryIntegration feature gate.")
}
