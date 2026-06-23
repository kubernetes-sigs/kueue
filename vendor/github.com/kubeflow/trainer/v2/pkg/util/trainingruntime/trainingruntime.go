package trainingruntime

import "github.com/kubeflow/trainer/v2/pkg/constants"

// IsSupportDeprecated returns true if TrainingRuntime labels indicate support=deprecated.
func IsSupportDeprecated(labels map[string]string) bool {
	if labels == nil {
		return false
	}
	val, ok := labels[constants.LabelSupport]
	return ok && val == constants.SupportDeprecated
}
