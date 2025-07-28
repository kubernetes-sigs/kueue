/*
Copyright The Kubernetes Authors.

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
	"errors"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apimachineryvalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	apimachineryutilvalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podworkload "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/features"
	stringsutils "sigs.k8s.io/kueue/pkg/util/strings"
)

const (
	queueVisibilityClusterQueuesMaxValue              = 4000
	queueVisibilityClusterQueuesUpdateIntervalSeconds = 1
)

var (
	integrationsPath                     = field.NewPath("integrations")
	integrationsFrameworksPath           = integrationsPath.Child("frameworks")
	integrationsExternalFrameworkPath    = integrationsPath.Child("externalFrameworks")
	podOptionsPath                       = integrationsPath.Child("podOptions")
	podOptionsNamespaceSelectorPath      = podOptionsPath.Child("namespaceSelector")
	managedJobsNamespaceSelectorPath     = field.NewPath("managedJobsNamespaceSelector")
	waitForPodsReadyPath                 = field.NewPath("waitForPodsReady")
	requeuingStrategyPath                = waitForPodsReadyPath.Child("requeuingStrategy")
	multiKueuePath                       = field.NewPath("multiKueue")
	fsPreemptionStrategiesPath           = field.NewPath("fairSharing", "preemptionStrategies")
	afsResourceWeightsPath               = field.NewPath("admissionFairSharing", "resourceWeights")
	afsPath                              = field.NewPath("admissionFairSharing")
	internalCertManagementPath           = field.NewPath("internalCertManagement")
	queueVisibilityPath                  = field.NewPath("queueVisibility")
	resourceTransformationPath           = field.NewPath("resources", "transformations")
	objectRetentionPoliciesPath          = field.NewPath("objectRetentionPolicies")
	objectRetentionPoliciesWorkloadsPath = objectRetentionPoliciesPath.Child("workloads")
)

func validate(c *configapi.Configuration, scheme *runtime.Scheme) field.ErrorList {
	var allErrs field.ErrorList
	allErrs = append(allErrs, validateWaitForPodsReady(c)...)
	allErrs = append(allErrs, validateQueueVisibility(c)...)
	allErrs = append(allErrs, validateIntegrations(c, scheme)...)
	allErrs = append(allErrs, validateMultiKueue(c)...)
	allErrs = append(allErrs, validateFairSharing(c)...)
	allErrs = append(allErrs, validateAdmissionFairSharing(c)...)
	allErrs = append(allErrs, validateInternalCertManagement(c)...)
	allErrs = append(allErrs, validateResourceTransformations(c)...)
	allErrs = append(allErrs, validateManagedJobsNamespaceSelector(c)...)
	allErrs = append(allErrs, validateObjectRetentionPolicies(c)...)
	return allErrs
}

func validateInternalCertManagement(c *configapi.Configuration) field.ErrorList {
	var allErrs field.ErrorList
	if c.InternalCertManagement == nil || !ptr.Deref(c.InternalCertManagement.Enable, false) {
		return allErrs
	}
	if svcName := c.InternalCertManagement.WebhookServiceName; svcName != nil {
		if errs := apimachineryutilvalidation.IsDNS1035Label(*svcName); len(errs) != 0 {
			allErrs = append(allErrs, field.Invalid(internalCertManagementPath.Child("webhookServiceName"), svcName, strings.Join(errs, ",")))
		}
	}
	if secName := c.InternalCertManagement.WebhookSecretName; secName != nil {
		if errs := apimachineryutilvalidation.IsDNS1123Subdomain(*secName); len(errs) != 0 {
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
				c.MultiKueue.GCInterval.Duration, apimachineryvalidation.IsNegativeErrorMsg))
		}
		if c.MultiKueue.WorkerLostTimeout != nil && c.MultiKueue.WorkerLostTimeout.Duration < 0 {
			allErrs = append(allErrs, field.Invalid(multiKueuePath.Child("workerLostTimeout"),
				c.MultiKueue.WorkerLostTimeout.Duration, apimachineryvalidation.IsNegativeErrorMsg))
		}
		if c.MultiKueue.Origin != nil {
			if errs := apimachineryutilvalidation.IsValidLabelValue(*c.MultiKueue.Origin); len(errs) != 0 {
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
			c.WaitForPodsReady.Timeout, apimachineryvalidation.IsNegativeErrorMsg))
	}
	if c.WaitForPodsReady.RecoveryTimeout != nil && c.WaitForPodsReady.RecoveryTimeout.Duration < 0 {
		allErrs = append(allErrs, field.Invalid(waitForPodsReadyPath.Child("recoveryTimeout"),
			c.WaitForPodsReady.RecoveryTimeout, apimachineryvalidation.IsNegativeErrorMsg))
	}
	if strategy := c.WaitForPodsReady.RequeuingStrategy; strategy != nil {
		if strategy.Timestamp != nil &&
			*strategy.Timestamp != configapi.CreationTimestamp && *strategy.Timestamp != configapi.EvictionTimestamp {
			allErrs = append(allErrs, field.NotSupported(requeuingStrategyPath.Child("timestamp"),
				strategy.Timestamp, []configapi.RequeuingTimestamp{configapi.CreationTimestamp, configapi.EvictionTimestamp}))
		}
		if strategy.BackoffLimitCount != nil && *strategy.BackoffLimitCount < 0 {
			allErrs = append(allErrs, field.Invalid(requeuingStrategyPath.Child("backoffLimitCount"),
				*strategy.BackoffLimitCount, apimachineryvalidation.IsNegativeErrorMsg))
		}
		if strategy.BackoffBaseSeconds != nil && *strategy.BackoffBaseSeconds < 0 {
			allErrs = append(allErrs, field.Invalid(requeuingStrategyPath.Child("backoffBaseSeconds"),
				*strategy.BackoffBaseSeconds, apimachineryvalidation.IsNegativeErrorMsg))
		}
		if ptr.Deref(strategy.BackoffMaxSeconds, 0) < 0 {
			allErrs = append(allErrs, field.Invalid(requeuingStrategyPath.Child("backoffMaxSeconds"),
				*strategy.BackoffMaxSeconds, apimachineryvalidation.IsNegativeErrorMsg))
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
				allErrs = append(allErrs, field.Invalid(maxCountPath, cfg.QueueVisibility.ClusterQueues.MaxCount, apimachineryvalidation.IsNegativeErrorMsg))
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

func validateNamespaceSelectorForPodIntegration(c *configapi.Configuration, namespaceSelector *metav1.LabelSelector, namespaceSelectorPath *field.Path, allErrs field.ErrorList) field.ErrorList {
	allErrs = append(allErrs, validation.ValidateLabelSelector(namespaceSelector, validation.LabelSelectorValidationOptions{}, namespaceSelectorPath)...)
	selector, err := metav1.LabelSelectorAsSelector(namespaceSelector)
	if err != nil {
		return allErrs
	}
	prohibitedNamespaces := []labels.Set{{corev1.LabelMetadataName: metav1.NamespaceSystem}}
	if c.Namespace != nil && *c.Namespace != "" {
		prohibitedNamespaces = append(prohibitedNamespaces, labels.Set{corev1.LabelMetadataName: *c.Namespace})
	}
	for _, pn := range prohibitedNamespaces {
		if selector.Matches(pn) {
			allErrs = append(allErrs, field.Invalid(namespaceSelectorPath, namespaceSelector,
				fmt.Sprintf("should not match the %q namespace", pn[corev1.LabelMetadataName])))
		}
	}
	return allErrs
}

func validatePodIntegrationOptions(c *configapi.Configuration) field.ErrorList {
	var allErrs field.ErrorList

	if !slices.Contains(c.Integrations.Frameworks, podworkload.FrameworkName) {
		return allErrs
	}

	// At least one namespace selector must be non-nil and enabled.
	// It is ok for both to be non-nil; pods will only be managed if all non-nil selectors match
	hasNamespaceSelector := false
	if c.ManagedJobsNamespaceSelector != nil {
		allErrs = validateNamespaceSelectorForPodIntegration(c, c.ManagedJobsNamespaceSelector, managedJobsNamespaceSelectorPath, allErrs)
		hasNamespaceSelector = true
	}
	if c.Integrations.PodOptions != nil && c.Integrations.PodOptions.NamespaceSelector != nil {
		allErrs = validateNamespaceSelectorForPodIntegration(c, c.Integrations.PodOptions.NamespaceSelector, podOptionsNamespaceSelectorPath, allErrs)
		hasNamespaceSelector = true
	}

	if !hasNamespaceSelector {
		allErrs = append(allErrs, field.Required(managedJobsNamespaceSelectorPath, "cannot be empty when pod integration is enabled"))
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
			ss = append(ss, stringsutils.Join(s, ","))
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

func validateAdmissionFairSharing(c *configapi.Configuration) field.ErrorList {
	afs := c.AdmissionFairSharing
	if afs == nil {
		return nil
	}
	var allErrs field.ErrorList

	if afs.UsageHalfLifeTime.Duration < 0 {
		allErrs = append(allErrs, field.Invalid(afsPath.Child("usageHalfLifeTime"),
			afs.UsageHalfLifeTime, apimachineryvalidation.IsNegativeErrorMsg))
	}
	if afs.UsageSamplingInterval.Duration <= 0 {
		allErrs = append(allErrs, field.Invalid(afsPath.Child("usageSamplingInterval"),
			afs.UsageHalfLifeTime, "must be greater than 0"))
	}
	for resName, weight := range afs.ResourceWeights {
		if weight < 0 {
			allErrs = append(allErrs, field.Invalid(afsResourceWeightsPath.Key(string(resName)),
				afs.ResourceWeights, apimachineryvalidation.IsNegativeErrorMsg))
		}
	}
	return allErrs
}

func validateResourceTransformations(c *configapi.Configuration) field.ErrorList {
	res := c.Resources
	if res == nil {
		return nil
	}
	var allErrs field.ErrorList
	seenKeys := make(sets.Set[corev1.ResourceName])
	for idx, transform := range res.Transformations {
		strategy := ptr.Deref(transform.Strategy, "")
		if strategy != configapi.Retain && strategy != configapi.Replace {
			allErrs = append(allErrs, field.NotSupported(resourceTransformationPath.Index(idx).Child("strategy"),
				transform.Strategy, []configapi.ResourceTransformationStrategy{configapi.Retain, configapi.Replace}))
		}
		if seenKeys.Has(transform.Input) {
			allErrs = append(allErrs, field.Duplicate(resourceTransformationPath.Index(idx).Child("input"), transform.Input))
		} else {
			seenKeys.Insert(transform.Input)
		}
	}
	return allErrs
}

func validateManagedJobsNamespaceSelector(c *configapi.Configuration) field.ErrorList {
	var allErrs field.ErrorList

	// The namespace selector must exempt every prohibitedNamespace
	prohibitedNamespaces := []labels.Set{{corev1.LabelMetadataName: metav1.NamespaceSystem}}
	if c.Namespace != nil && *c.Namespace != "" {
		prohibitedNamespaces = append(prohibitedNamespaces, labels.Set{corev1.LabelMetadataName: *c.Namespace})
	}

	allErrs = append(allErrs, validation.ValidateLabelSelector(c.ManagedJobsNamespaceSelector, validation.LabelSelectorValidationOptions{}, managedJobsNamespaceSelectorPath)...)
	selector, err := metav1.LabelSelectorAsSelector(c.ManagedJobsNamespaceSelector)
	if err != nil {
		return allErrs
	}

	for _, pn := range prohibitedNamespaces {
		if selector.Matches(pn) {
			allErrs = append(allErrs, field.Invalid(managedJobsNamespaceSelectorPath, c.ManagedJobsNamespaceSelector,
				fmt.Sprintf("should not match the %q namespace", pn[corev1.LabelMetadataName])))
		}
	}

	return allErrs
}

func ValidateFeatureGates(featureGateCLI string, featureGateMap map[string]bool) error {
	if featureGateCLI != "" && featureGateMap != nil {
		return errors.New("feature gates for CLI and configuration cannot both specified")
	}
	TASProfilesEnabled := []bool{features.Enabled(features.TASProfileMixed),
		features.Enabled(features.TASProfileLeastFreeCapacity),
	}
	enabledProfilesCount := 0
	for _, enabled := range TASProfilesEnabled {
		if enabled {
			enabledProfilesCount++
		}
	}
	if enabledProfilesCount > 1 {
		return errors.New("cannot use more than one TAS profiles")
	}
	if !features.Enabled(features.TopologyAwareScheduling) && enabledProfilesCount > 0 {
		return errors.New("cannot use a TAS profile with TAS disabled")
	}

	return nil
}

func validateObjectRetentionPolicies(c *configapi.Configuration) field.ErrorList {
	var allErrs field.ErrorList
	rr := c.ObjectRetentionPolicies
	if rr == nil || rr.Workloads == nil {
		return allErrs
	}
	if rr.Workloads.AfterFinished != nil && rr.Workloads.AfterFinished.Duration < 0 {
		allErrs = append(allErrs, field.Invalid(objectRetentionPoliciesWorkloadsPath.Child("afterFinished"),
			c.ObjectRetentionPolicies.Workloads.AfterFinished, apimachineryvalidation.IsNegativeErrorMsg))
	}
	if rr.Workloads.AfterDeactivatedByKueue != nil && rr.Workloads.AfterDeactivatedByKueue.Duration < 0 {
		allErrs = append(allErrs, field.Invalid(objectRetentionPoliciesWorkloadsPath.Child("afterDeactivatedByKueue"),
			c.ObjectRetentionPolicies.Workloads.AfterDeactivatedByKueue.Duration.String(), apimachineryvalidation.IsNegativeErrorMsg))
	}
	return allErrs
}
