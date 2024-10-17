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
	"strings"
	"unsafe"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	apimachineryvalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podworkload "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
)

const (
	queueVisibilityClusterQueuesMaxValue              = 4000
	queueVisibilityClusterQueuesUpdateIntervalSeconds = 1
)

var (
	integrationsPath                  = field.NewPath("integrations")
	integrationsFrameworksPath        = integrationsPath.Child("frameworks")
	integrationsExternalFrameworkPath = integrationsPath.Child("externalFrameworks")
	podOptionsPath                    = integrationsPath.Child("podOptions")
	namespaceSelectorPath             = podOptionsPath.Child("namespaceSelector")
	waitForPodsReadyPath              = field.NewPath("waitForPodsReady")
	requeuingStrategyPath             = waitForPodsReadyPath.Child("requeuingStrategy")
	multiKueuePath                    = field.NewPath("multiKueue")
	fsPreemptionStrategiesPath        = field.NewPath("fairSharing", "preemptionStrategies")
	internalCertManagementPath        = field.NewPath("internalCertManagement")
	queueVisibilityPath               = field.NewPath("queueVisibility")
	objectRetentionPoliciesPath       = field.NewPath("objectRetentionPolicies")
)

func validate(c *configapi.Configuration, scheme *runtime.Scheme) field.ErrorList {
	var allErrs field.ErrorList
	allErrs = append(allErrs, validateWaitForPodsReady(c)...)
	allErrs = append(allErrs, validateQueueVisibility(c)...)
	allErrs = append(allErrs, validateIntegrations(c, scheme)...)
	allErrs = append(allErrs, validateMultiKueue(c)...)
	allErrs = append(allErrs, validateFairSharing(c)...)
	allErrs = append(allErrs, validateInternalCertManagement(c)...)
	allErrs = append(allErrs, validateObjectRetentionPolicies(c)...)
	return allErrs
}

func validateInternalCertManagement(c *configapi.Configuration) field.ErrorList {
	var allErrs field.ErrorList
	if c.InternalCertManagement == nil || !ptr.Deref(c.InternalCertManagement.Enable, false) {
		return allErrs
	}
	if svcName := c.InternalCertManagement.WebhookServiceName; svcName != nil {
		if errs := apimachineryvalidation.IsDNS1035Label(*svcName); len(errs) != 0 {
			allErrs = append(allErrs, field.Invalid(internalCertManagementPath.Child("webhookServiceName"), svcName, strings.Join(errs, ",")))
		}
	}
	if secName := c.InternalCertManagement.WebhookSecretName; secName != nil {
		if errs := apimachineryvalidation.IsDNS1123Subdomain(*secName); len(errs) != 0 {
			allErrs = append(allErrs, field.Invalid(internalCertManagementPath.Child("webhookSecretName"), secName, strings.Join(errs, ",")))
		}
	}
	return allErrs
}

func validateMultiKueue(c *configapi.Configuration) field.ErrorList {
	var allErrs field.ErrorList
	if c.MultiKueue != nil {
		if c.MultiKueue.GCInterval != nil && c.MultiKueue.GCInterval.Duration < 0 {
			allErrs = append(allErrs, field.Invalid(multiKueuePath.Child("gcInterval"),
				c.MultiKueue.GCInterval.Duration, constants.IsNegativeErrorMsg))
		}
		if c.MultiKueue.WorkerLostTimeout != nil && c.MultiKueue.WorkerLostTimeout.Duration < 0 {
			allErrs = append(allErrs, field.Invalid(multiKueuePath.Child("workerLostTimeout"),
				c.MultiKueue.WorkerLostTimeout.Duration, constants.IsNegativeErrorMsg))
		}
		if c.MultiKueue.Origin != nil {
			if errs := apimachineryvalidation.IsValidLabelValue(*c.MultiKueue.Origin); len(errs) != 0 {
				allErrs = append(allErrs, field.Invalid(multiKueuePath.Child("origin"), *c.MultiKueue.Origin, strings.Join(errs, ",")))
			}
		}
	}
	return allErrs
}

func validateWaitForPodsReady(c *configapi.Configuration) field.ErrorList {
	var allErrs field.ErrorList
	if !WaitForPodsReadyIsEnabled(c) {
		return allErrs
	}
	if c.WaitForPodsReady.Timeout != nil && c.WaitForPodsReady.Timeout.Duration < 0 {
		allErrs = append(allErrs, field.Invalid(waitForPodsReadyPath.Child("timeout"),
			c.WaitForPodsReady.Timeout, constants.IsNegativeErrorMsg))
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
		if strategy.BackoffBaseSeconds != nil && *strategy.BackoffBaseSeconds < 0 {
			allErrs = append(allErrs, field.Invalid(requeuingStrategyPath.Child("backoffBaseSeconds"),
				*strategy.BackoffBaseSeconds, constants.IsNegativeErrorMsg))
		}
		if ptr.Deref(strategy.BackoffMaxSeconds, 0) < 0 {
			allErrs = append(allErrs, field.Invalid(requeuingStrategyPath.Child("backoffMaxSeconds"),
				*strategy.BackoffMaxSeconds, constants.IsNegativeErrorMsg))
		}
	}
	return allErrs
}

func validateQueueVisibility(cfg *configapi.Configuration) field.ErrorList {
	var allErrs field.ErrorList
	if cfg.QueueVisibility != nil {
		if cfg.QueueVisibility.ClusterQueues != nil {
			maxCountPath := queueVisibilityPath.Child("clusterQueues").Child("maxCount")
			if cfg.QueueVisibility.ClusterQueues.MaxCount < 0 {
				allErrs = append(allErrs, field.Invalid(maxCountPath, cfg.QueueVisibility.ClusterQueues.MaxCount, constants.IsNegativeErrorMsg))
			}
			if cfg.QueueVisibility.ClusterQueues.MaxCount > queueVisibilityClusterQueuesMaxValue {
				allErrs = append(allErrs, field.Invalid(maxCountPath, cfg.QueueVisibility.ClusterQueues.MaxCount, fmt.Sprintf("must be less than %d", queueVisibilityClusterQueuesMaxValue)))
			}
		}
		if cfg.QueueVisibility.UpdateIntervalSeconds < queueVisibilityClusterQueuesUpdateIntervalSeconds {
			allErrs = append(allErrs, field.Invalid(queueVisibilityPath.Child("updateIntervalSeconds"), cfg.QueueVisibility.UpdateIntervalSeconds, fmt.Sprintf("greater than or equal to %d", queueVisibilityClusterQueuesUpdateIntervalSeconds)))
		}
	}
	return allErrs
}

func validateIntegrations(c *configapi.Configuration, scheme *runtime.Scheme) field.ErrorList {
	var allErrs field.ErrorList
	if c.Integrations == nil {
		return field.ErrorList{field.Required(integrationsPath, "cannot be empty")}
	}
	if c.Integrations.Frameworks == nil {
		return field.ErrorList{field.Required(integrationsFrameworksPath, "cannot be empty")}
	}

	managedFrameworks := sets.New[string]()
	availableBuiltInFrameworks := jobframework.GetIntegrationsList()
	for idx, framework := range c.Integrations.Frameworks {
		if cb, found := jobframework.GetIntegration(framework); !found {
			allErrs = append(allErrs, field.NotSupported(integrationsFrameworksPath.Index(idx), framework, availableBuiltInFrameworks))
		} else if gvk, err := apiutil.GVKForObject(cb.JobType, scheme); err == nil {
			if managedFrameworks.Has(gvk.String()) {
				allErrs = append(allErrs, field.Duplicate(integrationsFrameworksPath.Index(idx), framework))
			} else {
				managedFrameworks = managedFrameworks.Insert(gvk.String())
			}
		}
	}
	for idx, framework := range c.Integrations.ExternalFrameworks {
		gvk, _ := schema.ParseKindArg(framework)
		switch {
		case gvk == nil:
			allErrs = append(allErrs, field.Invalid(integrationsExternalFrameworkPath.Index(idx), framework, "must be format, 'Kind.version.group.com'"))
		case managedFrameworks.Has(gvk.String()):
			allErrs = append(allErrs, field.Duplicate(integrationsExternalFrameworkPath.Index(idx), framework))
		default:
			managedFrameworks = managedFrameworks.Insert(gvk.String())
		}
	}

	allErrs = append(allErrs, validatePodIntegrationOptions(c)...)
	return allErrs
}

func validatePodIntegrationOptions(c *configapi.Configuration) field.ErrorList {
	var allErrs field.ErrorList

	if !slices.Contains(c.Integrations.Frameworks, podworkload.FrameworkName) {
		return allErrs
	}

	if c.Integrations.PodOptions == nil {
		return field.ErrorList{field.Required(podOptionsPath, "cannot be empty when pod integration is enabled")}
	}
	if c.Integrations.PodOptions.NamespaceSelector == nil {
		return field.ErrorList{field.Required(namespaceSelectorPath, "a namespace selector is required")}
	}

	prohibitedNamespaces := []labels.Set{{corev1.LabelMetadataName: metav1.NamespaceSystem}}

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

var (
	validStrategySets = [][]configapi.PreemptionStrategy{
		{
			configapi.LessThanOrEqualToFinalShare,
		},
		{
			configapi.LessThanInitialShare,
		},
		{
			configapi.LessThanOrEqualToFinalShare,
			configapi.LessThanInitialShare,
		},
	}

	validStrategySetsStr = func() []string {
		var ss []string
		for _, s := range validStrategySets {
			// Casting because strings.Join requires a slice of strings
			strategies := *(*[]string)(unsafe.Pointer(&s))
			ss = append(ss, strings.Join(strategies, ","))
		}
		return ss
	}()
)

func validateFairSharing(c *configapi.Configuration) field.ErrorList {
	fs := c.FairSharing
	if fs == nil {
		return nil
	}
	var allErrs field.ErrorList
	if len(fs.PreemptionStrategies) > 0 {
		validStrategy := false
		for _, s := range validStrategySets {
			if slices.Equal(s, fs.PreemptionStrategies) {
				validStrategy = true
				break
			}
		}
		if !validStrategy {
			allErrs = append(allErrs, field.NotSupported(fsPreemptionStrategiesPath, fs.PreemptionStrategies, validStrategySetsStr))
		}
	}
	return allErrs
}

func validateObjectRetentionPolicies(c *configapi.Configuration) field.ErrorList {
	var allErrs field.ErrorList

	rr := c.ObjectRetentionPolicies
	if rr != nil {
		if rr.FinishedWorkloadRetention != nil && rr.FinishedWorkloadRetention.Duration < 0 {
			allErrs = append(allErrs, field.Invalid(objectRetentionPoliciesPath.Child("finishedWorkloadRetention"),
				c.ObjectRetentionPolicies.FinishedWorkloadRetention, constants.IsNegativeErrorMsg))
		}
	}
	return allErrs
}
