//go:build exclude_scheduler_library

package was

import (
	"context"
	"fmt"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
)

// wasSimulator is never constructed in this build; it exists so WASOption keeps
// the same shape as the real one.
type wasSimulator struct{}

// WASOption configures the WAS simulator.
type WASOption func(*wasSimulator)

// WithDRA enables DRA device feasibility checking. It is a no-op here because
// the simulator this build returns is an error.
func WithDRA(_ client.Client) WASOption {
	return func(_ *wasSimulator) {}
}

func NewWASSimulator(ctx context.Context, restConfig *rest.Config, opts ...WASOption) (simulator.SchedulingSimulator, error) {
	return nil, fmt.Errorf("scheduler-library integration is compiled out of this binary. Disable the SchedulerLibraryIntegration feature gate.")
}
