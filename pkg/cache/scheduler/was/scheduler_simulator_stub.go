//go:build exclude_scheduler_library

package was

import (
	"context"
	"fmt"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
)

type Option func()

func WithSchedulerConfigFile(string) Option { return nil }

func NewWASSimulatorFactory(ctx context.Context, restConfig *rest.Config, _ ...Option) (simulator.Factory, error) {
	return nil, fmt.Errorf("scheduler-library integration is compiled out of this binary. Disable the SchedulerLibraryIntegration feature gate.")
}
