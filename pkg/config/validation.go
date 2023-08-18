package config

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation/field"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
)

const (
	queueVisibilityClusterQueuesMaxValue              = 4000
	queueVisibilityClusterQueuesUpdateIntervalSeconds = 1
	errMsgPodOptionsIsNil                             = "value of Config.Integrations.PodOptions is nil"
	errMsgNamespaceSelectorIsNil                      = "value of Config.Integrations.PodOptions.PodNamespaceSelector is nil"
	errMsgProhibitedNamespace                         = "namespaces with this label cannot be used for pod integration"
)

var (
	podOptionsPath        = field.NewPath("integrations", "podOptions")
	namespaceSelectorPath = podOptionsPath.Child("namespaceSelector")
)

func ValidateConfiguration(c configapi.Configuration) field.ErrorList {
	var allErrs field.ErrorList

	allErrs = append(allErrs, validateQueueVisibility(c)...)

	// Validate PodNamespaceSelector for the pod framework
	allErrs = append(allErrs, validateNamespaceSelector(c)...)

	return allErrs
}

func validateQueueVisibility(cfg configapi.Configuration) field.ErrorList {
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

func validateNamespaceSelector(c configapi.Configuration) field.ErrorList {
	var allErrs field.ErrorList

	if c.Integrations.PodOptions == nil {
		return field.ErrorList{field.Required(podOptionsPath, errMsgPodOptionsIsNil)}
	}
	if c.Integrations.PodOptions.NamespaceSelector == nil {
		return field.ErrorList{field.Required(namespaceSelectorPath, errMsgNamespaceSelectorIsNil)}
	}

	prohibitedNamespaces := []labels.Set{{corev1.LabelMetadataName: "kube-system"}}

	if c.Namespace != nil && *c.Namespace != "" {
		prohibitedNamespaces = append(prohibitedNamespaces, labels.Set{corev1.LabelMetadataName: *c.Namespace})
	}

	allErrs = append(allErrs, validation.ValidateLabelSelector(c.Integrations.PodOptions.NamespaceSelector, validation.LabelSelectorValidationOptions{}, namespaceSelectorPath)...)

	selector, err := metav1.LabelSelectorAsSelector(c.Integrations.PodOptions.NamespaceSelector)
	if err != nil {
		return allErrs
	}

	for _, pn := range prohibitedNamespaces {
		if selector.Matches(pn) {
			allErrs = append(allErrs, field.Invalid(namespaceSelectorPath, pn, errMsgProhibitedNamespace))
		}
	}

	return allErrs
}
