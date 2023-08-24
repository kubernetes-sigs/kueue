package config

import (
	"fmt"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
)

var (
	errInvalidMaxValue              = fmt.Errorf("maximum value for QueueVisibility.ClusterQueues.MaxCount must be %d", queueVisibilityClusterQueuesMaxValue)
	errInvalidUpdateIntervalSeconds = fmt.Errorf("minimum value for QueueVisibility.UpdateIntervalSeconds must be %d", queueVisibilityClusterQueuesUpdateIntervalSeconds)
)

const (
	queueVisibilityClusterQueuesMaxValue              = 4000
	queueVisibilityClusterQueuesUpdateIntervalSeconds = 1
)

func validate(cfg *configapi.Configuration) error {
	if cfg.QueueVisibility != nil {
		if cfg.QueueVisibility.ClusterQueues != nil {
			if cfg.QueueVisibility.ClusterQueues.MaxCount > queueVisibilityClusterQueuesMaxValue {
				return errInvalidMaxValue
			}
		}
		if cfg.QueueVisibility.UpdateIntervalSeconds < queueVisibilityClusterQueuesUpdateIntervalSeconds {
			return errInvalidUpdateIntervalSeconds
		}
	}
	return nil
}
