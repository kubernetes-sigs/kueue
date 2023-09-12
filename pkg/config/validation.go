package config

import (
	"fmt"

	"k8s.io/apimachinery/pkg/util/validation/field"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
)

const (
	queueVisibilityClusterQueuesMaxValue              = 4000
	queueVisibilityClusterQueuesUpdateIntervalSeconds = 1
)

func validate(cfg *configapi.Configuration) field.ErrorList {
	var allErrs field.ErrorList
	if cfg.QueueVisibility != nil {
		queueVisibilityPath := field.NewPath("queueVisibility")
		if cfg.QueueVisibility.ClusterQueues != nil {
			clusterQueues := queueVisibilityPath.Child("clusterQueues")
			if cfg.QueueVisibility.ClusterQueues.MaxCount > queueVisibilityClusterQueuesMaxValue {
				allErrs = append(allErrs, field.Invalid(clusterQueues.Child("maxCount"), cfg.QueueVisibility.ClusterQueues.MaxCount, fmt.Sprintf("must be less than %d", queueVisibilityClusterQueuesMaxValue)))
			}
		}
		if cfg.QueueVisibility.UpdateIntervalSeconds < queueVisibilityClusterQueuesUpdateIntervalSeconds {
			allErrs = append(allErrs, field.Invalid(queueVisibilityPath.Child("updateIntervalSeconds"), cfg.QueueVisibility.UpdateIntervalSeconds, fmt.Sprintf("greater than or equal to %d", queueVisibilityClusterQueuesUpdateIntervalSeconds)))
		}
	}
	return allErrs
}
