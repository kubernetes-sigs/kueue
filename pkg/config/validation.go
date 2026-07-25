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
	"fmt"
	"maps"
	"net"
	"regexp"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/validate/content"
	apimachineryvalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	apimachineryutilvalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/component-base/featuregate"
	dracel "k8s.io/dynamic-resource-allocation/cel"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podworkload "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/features"
	stringsutils "sigs.k8s.io/kueue/pkg/util/strings"
	"sigs.k8s.io/kueue/pkg/util/tlsconfig"
	"sigs.k8s.io/kueue/pkg/util/waitforpodsready"
)

var (
	integrationsPath                      = field.NewPath("integrations")
	integrationsFrameworksPath            = integrationsPath.Child("frameworks")
	integrationsExternalFrameworkPath     = integrationsPath.Child("externalFrameworks")
	managedJobsNamespaceSelectorPath      = field.NewPath("managedJobsNamespaceSelector")
	waitForPodsReadyPath                  = field.NewPath("waitForPodsReady")
	requeuingStrategyPath                 = waitForPodsReadyPath.Child("requeuingStrategy")
	multiKueuePath                        = field.NewPath("multiKueue")
	clusterProfileAccessProvidersPath     = multiKueuePath.Child("clusterProfile").Child("accessProviders")
	clusterProfileCredentialProvidersPath = multiKueuePath.Child("clusterProfile").Child("credentialsProviders")
	fsPreemptionStrategiesPath            = field.NewPath("fairSharing", "preemptionStrategies")
	afsResourceWeightsPath                = field.NewPath("admissionFairSharing", "resourceWeights")
	afsPath                               = field.NewPath("admissionFairSharing")
	internalCertManagementPath            = field.NewPath("internalCertManagement")
	resourceTransformationPath            = field.NewPath("resources", "transformations")
	dynamicResourceAllocationPath         = field.NewPath("resources", "deviceClassMappings")
	objectRetentionPoliciesPath           = field.NewPath("objectRetentionPolicies")
	objectRetentionPoliciesWorkloadsPath  = objectRetentionPoliciesPath.Child("workloads")
	tlsPath                               = field.NewPath("tls")
	featureGatesPath                      = field.NewPath("featureGates")
	visibilityServerBindAddressPath       = field.NewPath("visibilityServer", "bindAddress")
	visibilityServerBindPortPath          = field.NewPath("visibilityServer", "bindPort")
	customLabelsPath                      = field.NewPath("metrics", "customLabels")
	resourceQuotaCheckStrategyPath        = field.NewPath("resources", "quotaCheckStrategy")
	maxCustomLabels                       = 20
	maxTrackedCustomLabelValues           = 16
	maxTrackedWlCustomLabelValues         = 12
	maxCustomLabelsPerSourceKind          = map[configapi.SourceKind]int{
		configapi.SourceKindWorkload:     2,
		configapi.SourceKindLocalQueue:   6,
		configapi.SourceKindClusterQueue: 6,
		configapi.SourceKindCohort:       6,
	}
)

// Validate checks the configuration for invalid values.
func Validate(c *configapi.Configuration, scheme *runtime.Scheme) field.ErrorList {
	var allErrs field.ErrorList
	allErrs = append(allErrs, validateWaitForPodsReady(c)...)
	allErrs = append(allErrs, validateIntegrations(c, scheme)...)
	allErrs = append(allErrs, validateMultiKueue(c)...)
	allErrs = append(allErrs, validateFairSharing(c)...)
	allErrs = append(allErrs, validateAdmissionFairSharing(c)...)
	allErrs = append(allErrs, validateInternalCertManagement(c)...)
	allErrs = append(allErrs, validateResourceTransformations(c)...)
	allErrs = append(allErrs, validateDeviceClassMappings(c)...)
	allErrs = append(allErrs, validateManagedJobsNamespaceSelector(c)...)
	allErrs = append(allErrs, validateObjectRetentionPolicies(c)...)
	allErrs = append(allErrs, validateTLS(c)...)
	allErrs = append(allErrs, validateVisibilityServer(c)...)
	allErrs = append(allErrs, validateCustomLabels(c)...)
	allErrs = append(allErrs, validateQuotaCheckStrategy(c)...)
	return allErrs
}

func validateQuotaCheckStrategy(c *configapi.Configuration) field.ErrorList {
	var allErrs field.ErrorList
	if !features.Enabled(features.QuotaCheckStrategy) {
		return allErrs
	}
	if c.Resources != nil && c.Resources.QuotaCheckStrategy != nil {
		strategy := *c.Resources.QuotaCheckStrategy
		if len(c.Resources.ExcludeResourcePrefixes) > 0 && strategy == configapi.QuotaCheckIgnoreUndeclared {
			allErrs = append(allErrs, field.Invalid(
				resourceQuotaCheckStrategyPath,
				strategy,
				"excludeResourcePrefixes is not allowed when quotaCheckStrategy is IgnoreUndeclared",
			))
		}
		if strategy != configapi.QuotaCheckIgnoreUndeclared && strategy != configapi.QuotaCheckBlockUndeclared {
			allErrs = append(allErrs, field.NotSupported(
				resourceQuotaCheckStrategyPath,
				strategy,
				[]configapi.QuotaCheckStrategy{
					configapi.QuotaCheckIgnoreUndeclared,
					configapi.QuotaCheckBlockUndeclared,
				},
			))
		}
	}
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
			if errs := content.IsLabelValue(*c.MultiKueue.Origin); len(errs) != 0 {
				allErrs = append(allErrs, field.Invalid(multiKueuePath.Child("origin"), *c.MultiKueue.Origin, strings.Join(errs, ",")))
			}
		}

		if len(c.MultiKueue.ExternalFrameworks) > 0 {
			path := multiKueuePath.Child("externalFrameworks")
			enabledIntegrations := sets.New[string]()
			if c.Integrations != nil {
				enabledIntegrations = sets.New(c.Integrations.Frameworks...)
			}

			builtInAdapters, err := jobframework.GetMultiKueueAdapters(enabledIntegrations)
			if err != nil {
				allErrs = append(allErrs, field.InternalError(path, err))
			}
			builtInGVKs := sets.New[string]()
			for gvk := range builtInAdapters {
				builtInGVKs.Insert(gvk)
			}

			seenGVKs := sets.New[string]()
			for i, f := range c.MultiKueue.ExternalFrameworks {
				fldPath := path.Index(i).Child("name")
				parsedGVK, _ := schema.ParseKindArg(f.Name)
				if parsedGVK == nil {
					allErrs = append(allErrs, field.Invalid(fldPath, f.Name, "must be in 'kind.version.group' format"))
					continue
				}
				gvk := parsedGVK.String()
				if seenGVKs.Has(gvk) {
					allErrs = append(allErrs, field.Duplicate(fldPath, f.Name))
				} else {
					seenGVKs.Insert(gvk)
				}
				if builtInGVKs.Has(gvk) {
					allErrs = append(allErrs, field.Invalid(fldPath, f.Name, "conflicts with a built-in MultiKueue adapter"))
				}
			}
		}

		if cp := c.MultiKueue.ClusterProfile; cp != nil {
			credentialsProviders := cp.CredentialsProviders //nolint:staticcheck // SA1019: CredentialsProviders is validated for backward compatibility.
			if len(cp.AccessProviders) > 0 && len(credentialsProviders) > 0 {
				allErrs = append(allErrs, field.Forbidden(clusterProfileCredentialProvidersPath, "must not be specified when accessProviders is specified"))
			}
			if len(cp.AccessProviders) > 0 {
				allErrs = append(allErrs, validateClusterProfileAccessProviders(cp.AccessProviders, clusterProfileAccessProvidersPath)...)
			} else {
				allErrs = append(allErrs, validateClusterProfileAccessProviders(credentialsProviders, clusterProfileCredentialProvidersPath)...)
			}
		}

		if idc := c.MultiKueue.IncrementalDispatcherConfig; idc != nil {
			idcPath := multiKueuePath.Child("incrementalDispatcherConfig")
			if ptr.Deref(c.MultiKueue.DispatcherName, "") != configapi.MultiKueueDispatcherModeIncremental {
				allErrs = append(allErrs, field.Invalid(idcPath, idc,
					"incrementalDispatcherConfig is only valid when dispatcherName is set to the incremental dispatcher"))
			}
			if idc.StepSize != nil && *idc.StepSize < 1 {
				allErrs = append(allErrs, field.Invalid(idcPath.Child("stepSize"), *idc.StepSize,
					"must be greater than or equal to 1"))
			}
		}
	}
	return allErrs
}

func validateClusterProfileAccessProviders(providers []configapi.ClusterProfileAccessProvider, providersPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	execConfigPath := providersPath.Child("execConfig")
	for _, provider := range providers {
		if len(provider.Name) == 0 {
			allErrs = append(allErrs, field.Required(providersPath.Child("name"), "must be specified"))
		}

		// The following execConfig validations almost stolen from
		// https://github.com/kubernetes/client-go/blob/45e0decafa9b847c983f55c84b4f6ce5617f8f69/tools/clientcmd/validation.go#L308-L335
		if len(provider.ExecConfig.Command) == 0 {
			allErrs = append(allErrs, field.Required(execConfigPath.Child("command"), "must be specified"))
		}
		if len(provider.ExecConfig.APIVersion) == 0 {
			allErrs = append(allErrs, field.Required(execConfigPath.Child("apiVersion"), "must be specified"))
		}
		for _, v := range provider.ExecConfig.Env {
			if len(v.Name) == 0 {
				allErrs = append(allErrs, field.Required(execConfigPath.Child("env").Child("name"), "must be specified"))
			}
		}
		switch provider.ExecConfig.InteractiveMode {
		case "":
			allErrs = append(allErrs, field.Required(execConfigPath.Child("interactiveMode"), "must be specified"))
		case clientcmdapi.NeverExecInteractiveMode, clientcmdapi.IfAvailableExecInteractiveMode, clientcmdapi.AlwaysExecInteractiveMode:
			// These are valid
		default:
			allErrs = append(allErrs, field.NotSupported(
				execConfigPath.Child("interactiveMode"),
				provider.ExecConfig.InteractiveMode,
				[]clientcmdapi.ExecInteractiveMode{clientcmdapi.NeverExecInteractiveMode, clientcmdapi.IfAvailableExecInteractiveMode, clientcmdapi.AlwaysExecInteractiveMode},
			))
		}
	}
	return allErrs
}

func validateWaitForPodsReady(c *configapi.Configuration) field.ErrorList {
	var allErrs field.ErrorList
	if !waitforpodsready.Enabled(c.WaitForPodsReady) {
		return allErrs
	}
	if c.WaitForPodsReady.Timeout.Duration == 0 {
		allErrs = append(allErrs, field.Required(waitForPodsReadyPath.Child("timeout"), "must be specified"))
	}
	if c.WaitForPodsReady.Timeout.Duration < 0 {
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

	if c.ManagedJobsNamespaceSelector != nil {
		allErrs = validateNamespaceSelectorForPodIntegration(c, c.ManagedJobsNamespaceSelector, managedJobsNamespaceSelectorPath, allErrs)
	} else {
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
	if len(fs.PreemptionStrategies) == 0 {
		allErrs = append(allErrs, field.Required(fsPreemptionStrategiesPath, "must be specified"))
	} else {
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

func validateDeviceClassMappings(c *configapi.Configuration) field.ErrorList {
	if c.Resources == nil || len(c.Resources.DeviceClassMappings) == 0 {
		return nil
	}

	mappings := c.Resources.DeviceClassMappings
	var allErrs field.ErrorList

	if len(mappings) > 16 {
		allErrs = append(allErrs, field.TooMany(dynamicResourceAllocationPath, len(mappings), 16))
	}

	seenResourceNames := make(sets.Set[corev1.ResourceName])
	deviceClassToResource := make(map[corev1.ResourceName]corev1.ResourceName)
	deviceClassCounterNames := make(map[corev1.ResourceName]sets.Set[string])

	for idx, mapping := range mappings {
		mappingPath := dynamicResourceAllocationPath.Index(idx)

		if errs := content.IsLabelKey(string(mapping.Name)); len(errs) > 0 {
			allErrs = append(allErrs, field.Invalid(mappingPath.Child("name"), mapping.Name, strings.Join(errs, "; ")))
		}

		if len(string(mapping.Name)) > 253 {
			allErrs = append(allErrs, field.Invalid(mappingPath.Child("name"), mapping.Name, "must not exceed 253 characters"))
		}

		if seenResourceNames.Has(mapping.Name) {
			allErrs = append(allErrs, field.Duplicate(mappingPath.Child("name"), mapping.Name))
		} else {
			seenResourceNames.Insert(mapping.Name)
		}

		if len(mapping.DeviceClassNames) == 0 {
			allErrs = append(allErrs, field.Required(mappingPath.Child("deviceClassNames"),
				"at least one device class name is required"))
		}

		if len(mapping.DeviceClassNames) > 8 {
			allErrs = append(allErrs, field.TooMany(mappingPath.Child("deviceClassNames"), len(mapping.DeviceClassNames), 8))
		}

		seenDeviceClassNames := make(sets.Set[corev1.ResourceName])

		for dcIdx, deviceClass := range mapping.DeviceClassNames {
			dcPath := mappingPath.Child("deviceClassNames").Index(dcIdx)

			if errs := content.IsLabelKey(string(deviceClass)); len(errs) > 0 {
				allErrs = append(allErrs, field.Invalid(dcPath, deviceClass, strings.Join(errs, "; ")))
			}

			if len(string(deviceClass)) > 253 {
				allErrs = append(allErrs, field.Invalid(dcPath, deviceClass, "must not exceed 253 characters"))
			}

			if seenDeviceClassNames.Has(deviceClass) {
				allErrs = append(allErrs, field.Duplicate(dcPath, deviceClass))
			} else {
				seenDeviceClassNames.Insert(deviceClass)
			}

			counterName := counterNameForMapping(mapping)
			if existingResource, exists := deviceClassToResource[deviceClass]; exists {
				if existingResource != mapping.Name {
					// Allow same DeviceClass in multiple mappings only when both have
					// counter sources with different counter names — each mapping tracks
					// a different counter dimension (e.g., gpu.memory and gpu.compute).
					existingCounters := deviceClassCounterNames[deviceClass]
					switch {
					case counterName == "" || existingCounters.Len() == 0:
						allErrs = append(allErrs, field.Invalid(dcPath, deviceClass,
							fmt.Sprintf("device class already mapped to resource %s", existingResource)))
					case existingCounters.Has(counterName):
						allErrs = append(allErrs, field.Invalid(dcPath, deviceClass,
							fmt.Sprintf("device class already has a counter source for counter %q", counterName)))
					default:
						deviceClassCounterNames[deviceClass].Insert(counterName)
					}
				}
			} else {
				deviceClassToResource[deviceClass] = mapping.Name
				deviceClassCounterNames[deviceClass] = sets.New[string]()
				if counterName != "" {
					deviceClassCounterNames[deviceClass].Insert(counterName)
				}
			}
		}

		if len(mapping.Sources) > 0 {
			sourcesPath := mappingPath.Child("sources")
			celCache := dracel.NewCache(len(mapping.Sources), dracel.Features{})
			counterCount := 0
			hasCapacity := false
			for sIdx, source := range mapping.Sources {
				sourcePath := sourcesPath.Index(sIdx)
				if source.Counter == nil && source.Capacity == nil {
					allErrs = append(allErrs, field.Required(sourcePath, "exactly one source type must be set per entry"))
					continue
				}
				if source.Counter != nil && source.Capacity != nil {
					allErrs = append(allErrs, field.Invalid(sourcePath, "counter+capacity", "exactly one source type must be set per entry"))
					continue
				}
				if source.Counter != nil {
					counterCount++
					if !features.Enabled(features.KueueDRAIntegrationPartitionableDevices) {
						allErrs = append(allErrs, field.Invalid(sourcePath.Child("counter"), true,
							"counter sources require KueueDRAIntegrationPartitionableDevices to be enabled"))
						continue
					}
					counterPath := sourcePath.Child("counter")
					if source.Counter.Name == "" {
						allErrs = append(allErrs, field.Required(counterPath.Child("name"), ""))
					} else if errs := apimachineryutilvalidation.IsDNS1123Label(source.Counter.Name); len(errs) != 0 {
						allErrs = append(allErrs, field.Invalid(counterPath.Child("name"), source.Counter.Name, strings.Join(errs, ",")))
					}
					allErrs = append(allErrs, validateDeviceClassSource(source.Counter.Driver, &source.Counter.DeviceSelector, counterPath, celCache)...)
				}
				if source.Capacity != nil {
					hasCapacity = true
					if !features.Enabled(features.KueueDRAIntegrationConsumableCapacity) {
						allErrs = append(allErrs, field.Invalid(sourcePath.Child("capacity"), true,
							"capacity sources require KueueDRAIntegrationConsumableCapacity to be enabled"))
						continue
					}
					capacityPath := sourcePath.Child("capacity")
					allErrs = append(allErrs, validateQualifiedName(source.Capacity.Name, capacityPath.Child("name"))...)
					allErrs = append(allErrs, validateDeviceClassSource(source.Capacity.Driver, &source.Capacity.DeviceSelector, capacityPath, celCache)...)
				}
			}
			if counterCount > 0 && hasCapacity {
				allErrs = append(allErrs, field.Invalid(sourcesPath, len(mapping.Sources),
					"cannot mix counter and capacity sources in the same mapping"))
			}
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

func LoadAndValidateFeatureGates(featureGateCLI string, featureGateMap map[string]bool) field.ErrorList {
	if featureGateCLI != "" {
		if err := utilfeature.DefaultMutableFeatureGate.Set(featureGateCLI); err != nil {
			return field.ErrorList{field.Invalid(featureGatesPath, featureGateCLI, err.Error())}
		}
	} else {
		if err := utilfeature.DefaultMutableFeatureGate.SetFromMap(featureGateMap); err != nil {
			return field.ErrorList{field.Invalid(featureGatesPath, featureGateMap, err.Error())}
		}
	}
	var allErrs field.ErrorList
	if featureGateCLI != "" && featureGateMap != nil {
		allErrs = append(allErrs, field.Invalid(featureGatesPath, featureGateMap, "feature gates for CLI and configuration cannot both specified"))
	}
	TASProfilesEnabled := []bool{features.Enabled(features.TASProfileMixed)}
	enabledProfilesCount := 0
	for _, enabled := range TASProfilesEnabled {
		if enabled {
			enabledProfilesCount++
		}
	}
	// Currently dead code but leaving in if more TASProfiles are added
	if enabledProfilesCount > 1 {
		allErrs = append(allErrs, field.Invalid(featureGatesPath, TASProfilesEnabled, "cannot use more than one TAS profiles"))
	}
	if !features.Enabled(features.TopologyAwareScheduling) && enabledProfilesCount > 0 {
		allErrs = append(allErrs, field.Invalid(featureGatesPath, enabledProfilesCount, "cannot use a TAS profile with TAS disabled"))
	}

	// TAS sub-features have no effect unless their dependencies are also enabled. All of them
	// require TopologyAwareScheduling; TASFailedNodeReplacementFailFast and
	// TASReplaceNodeOnPodTermination additionally require TASFailedNodeReplacement.
	allErrs = append(allErrs, validateFeatureGateDependency(features.TASHandleOverlappingFlavors, features.TopologyAwareScheduling)...)
	allErrs = append(allErrs, validateFeatureGateDependency(features.TASFailedNodeReplacement, features.TopologyAwareScheduling)...)
	allErrs = append(allErrs, validateFeatureGateDependency(features.TASFailedNodeReplacementFailFast, features.TopologyAwareScheduling, features.TASFailedNodeReplacement)...)
	allErrs = append(allErrs, validateFeatureGateDependency(features.TASReplaceNodeOnPodTermination, features.TopologyAwareScheduling, features.TASFailedNodeReplacement)...)
	allErrs = append(allErrs, validateFeatureGateDependency(features.TASReplaceNodeDueToNotReadyOverFixedTime, features.TopologyAwareScheduling, features.TASFailedNodeReplacement)...)
	allErrs = append(allErrs, validateFeatureGateDependency(features.TASBalancedPlacement, features.TopologyAwareScheduling)...)
	allErrs = append(allErrs, validateFeatureGateDependency(features.TASReplaceNodeOnNodeTaints, features.TopologyAwareScheduling)...)
	allErrs = append(allErrs, validateFeatureGateDependency(features.TASMultiLayerTopology, features.TopologyAwareScheduling)...)
	allErrs = append(allErrs, validateFeatureGateDependency(features.TASRespectNodeAffinityPreferred, features.TopologyAwareScheduling)...)
	allErrs = append(allErrs, validateFeatureGateDependency(features.UnadmittedWorkloadsExplicitStatus, features.UnadmittedWorkloadsObservability)...)

	allErrs = append(allErrs, validateFeatureGateDependency(features.ElasticJobsViaWorkloadSlicesWithTAS, features.ElasticJobsViaWorkloadSlices, features.TopologyAwareScheduling)...)

	allErrs = append(allErrs, validateDRAFeatureGateDependencies()...)

	return allErrs
}

func validateDRAFeatureGateDependencies() field.ErrorList {
	var allErrs field.ErrorList
	allErrs = append(allErrs, validateFeatureGateDependency(features.KueueDRAIntegrationExtendedResource, features.KueueDRAIntegration)...)
	allErrs = append(allErrs, validateFeatureGateDependency(features.KueueDRAIntegrationPartitionableDevices, features.KueueDRAIntegration)...)
	allErrs = append(allErrs, validateFeatureGateDependency(features.KueueDRAIntegrationConsumableCapacity, features.KueueDRAIntegration)...)
	return allErrs
}

// validateFeatureGateDependency returns an error for each dependency feature gate that is
// disabled while gate is enabled. A gate has no effect unless all its dependencies are enabled.
func validateFeatureGateDependency(gate featuregate.Feature, dependencies ...featuregate.Feature) field.ErrorList {
	if !features.Enabled(gate) {
		return nil
	}
	var allErrs field.ErrorList
	for _, dep := range dependencies {
		if !features.Enabled(dep) {
			allErrs = append(allErrs, field.Invalid(featureGatesPath, gate,
				fmt.Sprintf("%s requires %s to be enabled", gate, dep)))
		}
	}
	return allErrs
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

func validateTLS(c *configapi.Configuration) field.ErrorList {
	var allErrs field.ErrorList
	if c.TLS == nil {
		return allErrs
	}

	// Validate unparsed values first, then parse.
	// This provides clearer error messages for invalid input.
	_, err := tlsconfig.ParseTLSOptions(c.TLS)
	if err != nil {
		allErrs = append(allErrs, field.Invalid(tlsPath.Root(), c.TLS, err.Error()))
		return allErrs
	}

	// TLS 1.3 cipher suites are not configurable in Go's crypto/tls package.
	// When TLS 1.3 is set as the minimum version, cipher suites must not be specified.
	if c.TLS.MinVersion == "VersionTLS13" && len(c.TLS.CipherSuites) > 0 {
		allErrs = append(allErrs, field.Invalid(tlsPath.Child("cipherSuites"),
			c.TLS.CipherSuites, "may not be specified when `minVersion` is 'VersionTLS13'"))
	}
	return allErrs
}

func validateVisibilityServer(c *configapi.Configuration) field.ErrorList {
	var allErrs field.ErrorList
	if c.VisibilityServer == nil {
		return allErrs
	}
	if c.VisibilityServer.BindAddress != nil {
		if net.ParseIP(*c.VisibilityServer.BindAddress) == nil {
			allErrs = append(allErrs, field.Invalid(visibilityServerBindAddressPath, *c.VisibilityServer.BindAddress, "must be a valid IP address"))
		}
	}
	if c.VisibilityServer.BindPort != nil {
		port := *c.VisibilityServer.BindPort
		if port < 1 || port > 65535 {
			allErrs = append(allErrs, field.Invalid(visibilityServerBindPortPath, port, "must be a valid port number (1-65535)"))
		}
	}
	return allErrs
}

var customLabelNameRegexp = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)

func validateCustomLabels(c *configapi.Configuration) field.ErrorList {
	if len(c.Metrics.CustomLabels) == 0 {
		return nil
	}
	var allErrs field.ErrorList
	seenNames := sets.New[string]()
	countPerSourceKind := make(map[configapi.SourceKind]int)
	if len(c.Metrics.CustomLabels) > maxCustomLabels {
		allErrs = append(allErrs, field.TooMany(customLabelsPath, len(c.Metrics.CustomLabels), maxCustomLabels))
	}
	for i, entry := range c.Metrics.CustomLabels {
		fldPath := customLabelsPath.Index(i)

		if !customLabelNameRegexp.MatchString(entry.Name) {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("name"), entry.Name,
				"must match ^[a-zA-Z][a-zA-Z0-9_]*$"))
		}
		if seenNames.Has(entry.Name) {
			allErrs = append(allErrs, field.Duplicate(fldPath.Child("name"), entry.Name))
		} else {
			seenNames.Insert(entry.Name)
		}
		if entry.SourceLabelKey != "" && entry.SourceAnnotationKey != "" {
			allErrs = append(allErrs, field.Invalid(fldPath, entry,
				"sourceLabelKey and sourceAnnotationKey are mutually exclusive"))
		}
		switch {
		case entry.SourceLabelKey != "":
			allErrs = append(allErrs, validateLabelKey(fldPath.Child("sourceLabelKey"), entry.SourceLabelKey)...)
		case entry.SourceAnnotationKey != "":
			allErrs = append(allErrs, validateLabelKey(fldPath.Child("sourceAnnotationKey"), entry.SourceAnnotationKey)...)
		default:
			allErrs = append(allErrs, validateLabelKey(fldPath.Child("name"), entry.Name)...)
		}

		sourceKind := ptr.Deref(entry.SourceKind, configapi.DefaultCustomMetricLabelSourceKind)
		countPerSourceKind[sourceKind]++
		if sourceKind == configapi.SourceKindWorkload {
			if len(entry.TrackedValues) == 0 {
				allErrs = append(allErrs, field.Invalid(fldPath.Child("trackedValues"), entry.TrackedValues,
					"must not be empty when sourceKind is 'Workload'"))
			}
			if len(entry.TrackedValues) > maxTrackedWlCustomLabelValues {
				allErrs = append(allErrs, field.Invalid(fldPath.Child("trackedValues"), entry.TrackedValues,
					fmt.Sprintf("must not be greater than %d when sourceKind is 'Workload'", maxTrackedWlCustomLabelValues)))
			}
		} else if len(entry.TrackedValues) > maxTrackedCustomLabelValues {
			allErrs = append(allErrs, field.TooMany(fldPath.Child("trackedValues"), len(entry.TrackedValues), maxTrackedCustomLabelValues))
		}
		if sets.New(entry.TrackedValues...).Len() < len(entry.TrackedValues) {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("trackedValues"), entry.TrackedValues, "must not contain duplicates"))
		}
	}

	for _, kind := range slices.Sorted(maps.Keys(countPerSourceKind)) {
		labelLimit, kindSupported := maxCustomLabelsPerSourceKind[kind]
		if !kindSupported {
			allErrs = append(allErrs, field.Invalid(
				customLabelsPath,
				c.Metrics.CustomLabels,
				fmt.Sprintf("unknown source kind: %s", kind),
			))
		} else if count := countPerSourceKind[kind]; count > labelLimit {
			allErrs = append(allErrs, field.Invalid(
				customLabelsPath,
				c.Metrics.CustomLabels,
				fmt.Sprintf("too many custom labels for source kind %s: found %d, expected <= %d", kind, count, labelLimit),
			))
		}
	}

	return allErrs
}

func validateLabelKey(fldPath *field.Path, value string) field.ErrorList {
	if errs := content.IsLabelKey(value); len(errs) > 0 {
		return field.ErrorList{field.Invalid(fldPath, value, strings.Join(errs, "; "))}
	}
	return nil
}

func counterNameForMapping(mapping configapi.DeviceClassMapping) string {
	if len(mapping.Sources) > 0 && mapping.Sources[0].Counter != nil {
		return mapping.Sources[0].Counter.Name
	}
	return ""
}

func validateDeviceClassSource(driver string, selector *resourcev1.DeviceSelector, path *field.Path, celCache *dracel.Cache) field.ErrorList {
	var allErrs field.ErrorList
	if driver == "" {
		allErrs = append(allErrs, field.Required(path.Child("driver"), ""))
	} else if len(driver) > resourcev1.DriverNameMaxLength {
		allErrs = append(allErrs, field.Invalid(path.Child("driver"), driver, fmt.Sprintf("must not exceed %d characters", resourcev1.DriverNameMaxLength)))
	} else if errs := apimachineryutilvalidation.IsDNS1123Subdomain(driver); len(errs) != 0 {
		allErrs = append(allErrs, field.Invalid(path.Child("driver"), driver, strings.Join(errs, ",")))
	}
	selectorPath := path.Child("deviceSelector", "cel", "expression")
	if selector.CEL == nil || selector.CEL.Expression == "" {
		allErrs = append(allErrs, field.Required(selectorPath, ""))
	} else {
		result := celCache.GetOrCompile(selector.CEL.Expression)
		if result.Error != nil {
			allErrs = append(allErrs, field.Invalid(selectorPath, selector.CEL.Expression,
				fmt.Sprintf("CEL compilation failed: %v", result.Error)))
		}
	}
	return allErrs
}

// validateQualifiedName is copied from:
// https://github.com/kubernetes/kubernetes/blob/f5ab85e7c9d716e8bc5cf467ba931d8e8123764f/pkg/apis/resource/validation/validation.go#L1342-L1373
func validateQualifiedName(name resourcev1.QualifiedName, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	parts := strings.Split(string(name), "/")
	switch len(parts) {
	case 1:
		allErrs = append(allErrs, validateCIdentifier(parts[0], fldPath)...)
	case 2:
		if len(parts[0]) == 0 {
			allErrs = append(allErrs, field.Invalid(fldPath, "", "the domain must not be empty"))
		} else {
			allErrs = append(allErrs, validateDriverName(parts[0], fldPath)...)
		}
		if len(parts[1]) == 0 {
			allErrs = append(allErrs, field.Invalid(fldPath, "", "the name must not be empty"))
		} else {
			allErrs = append(allErrs, validateCIdentifier(parts[1], fldPath)...)
		}
		// TODO: This validation is incomplete. It should reject qualified names
		// that contain more than one slash. Currently, names like "a/b/c" are not
		// handled and are implicitly accepted.
		//
		// This needs to be fixed in two places:
		// 1. Here in this function.
		// 2. In the corresponding declarative validation utility `resourcesQualifiedName`
		//    in `staging/src/k8s.io/apimachinery/pkg/api/validate/strfmt.go`.
		//
		// The fix should be introduced carefully, possibly using ratcheting to avoid
		// breaking existing, non-compliant objects.
	}

	return allErrs
}

func validateDriverName(name string, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if len(name) > resourcev1.DeviceMaxDomainLength {
		allErrs = append(allErrs, field.TooLong(fldPath, "" /*unused*/, resourcev1.DeviceMaxDomainLength))
	}
	for _, msg := range apimachineryutilvalidation.IsDNS1123Subdomain(strings.ToLower(name)) {
		allErrs = append(allErrs, field.Invalid(fldPath, name, msg))
	}
	return allErrs
}

// validateCIdentifier is copied from
// https://github.com/kubernetes/kubernetes/blob/f5ab85e7c9d716e8bc5cf467ba931d8e8123764f/pkg/apis/resource/validation/validation.go#L1386-L1395
func validateCIdentifier(id string, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if len(id) > resourcev1.DeviceMaxIDLength {
		allErrs = append(allErrs, field.TooLong(fldPath, "" /*unused*/, resourcev1.DeviceMaxIDLength))
	}
	for _, msg := range content.IsCIdentifier(id) {
		allErrs = append(allErrs, field.Invalid(fldPath, id, msg))
	}
	return allErrs
}
