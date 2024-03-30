/*
Copyright 2023 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package config

import (
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation/field"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
)

const (
	queueVisibilityClusterQueuesMaxValue              = 4000
	queueVisibilityClusterQueuesUpdateIntervalSeconds = 1
)

var (
	integrationsPath           = field.NewPath("integrations")
	integrationsFrameworksPath = integrationsPath.Child("frameworks")
	podOptionsPath             = integrationsPath.Child("podOptions")
	namespaceSelectorPath      = podOptionsPath.Child("namespaceSelector")
	waitForPodsReadyPath       = field.NewPath("waitForPodsReady")
	requeuingStrategyPath      = waitForPodsReadyPath.Child("requeuingStrategy")
)

func validate(c *configapi.Configuration) field.ErrorList {
	var allErrs field.ErrorList

	allErrs = append(allErrs, validateWaitForPodsReady(c)...)

	allErrs = append(allErrs, validateQueueVisibility(c)...)

	// Validate PodNamespaceSelector for the pod framework
	allErrs = append(allErrs, validateIntegrations(c)...)

	return allErrs
}

func validateWaitForPodsReady(c *configapi.Configuration) field.ErrorList {
	var allErrs field.ErrorList
	if !WaitForPodsReadyIsEnabled(c) {
		return allErrs
	}
	if strategy := c.WaitForPodsReady.RequeuingStrategy; strategy != nil {
		if strategy.Timestamp != nil &&
			*strategy.Timestamp != configapi.CreationTimestamp && *strategy.Timestamp != configapi.EvictionTimestamp {
			allErrs = append(allErrs, field.NotSupported(requeuingStrategyPath.Child("timestamp"),
				strategy.Timestamp, []configapi.RequeuingTimestamp{configapi.CreationTimestamp, configapi.EvictionTimestamp}))
		}
		if strategy.BackoffLimitCount != nil && *strategy.BackoffLimitCount < 0 {
			allErrs = append(allErrs, field.Invalid(requeuingStrategyPath.Child("backoffLimitCount"),
				*strategy.BackoffLimitCount, constants.IsNegativeErrorMsg))
		}
	}
	return allErrs
}

func validateQueueVisibility(cfg *configapi.Configuration) field.ErrorList {
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

func validateIntegrations(c *configapi.Configuration) field.ErrorList {
	var allErrs field.ErrorList

	if c.Integrations == nil {
		return field.ErrorList{field.Required(integrationsPath, "cannot be empty")}
	}

	if c.Integrations.Frameworks == nil {
		return field.ErrorList{field.Required(integrationsFrameworksPath, "cannot be empty")}
	}

	allErrs = append(allErrs, validatePodIntegrationOptions(c)...)

	return allErrs
}

func validatePodIntegrationOptions(c *configapi.Configuration) field.ErrorList {
	var allErrs field.ErrorList

	if !slices.Contains(c.Integrations.Frameworks, "pod") {
		return allErrs
	}

	if c.Integrations.PodOptions == nil {
		return field.ErrorList{field.Required(podOptionsPath, "cannot be empty when pod integration is enabled")}
	}
	if c.Integrations.PodOptions.NamespaceSelector == nil {
		return field.ErrorList{field.Required(namespaceSelectorPath, "a namespace selector is required")}
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
			allErrs = append(allErrs, field.Invalid(namespaceSelectorPath, c.Integrations.PodOptions.NamespaceSelector,
				fmt.Sprintf("should not match the %q namespace", pn[corev1.LabelMetadataName])))
		}
	}

	return allErrs
}
