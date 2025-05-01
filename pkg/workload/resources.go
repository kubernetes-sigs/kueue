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

package workload

import (
	"context"
	"fmt"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	dra "k8s.io/api/resource/v1beta1"
	k8sresource "k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/limitrange"
	"sigs.k8s.io/kueue/pkg/util/resource"
)

var (
	PodSetsPath = field.NewPath("spec").Child("podSets")
)

const (
	RequestsMustNotExceedLimitMessage = "requests must not exceed its limits"
)

// We do not verify Pod's RuntimeClass legality here as this will be performed in admission controller.
// As a result, the pod's Overhead is not always correct. E.g. if we set a non-existent runtime class name to
// `pod.Spec.RuntimeClassName` and we also set the `pod.Spec.Overhead`, in real world, the pod creation will be
// rejected due to the mismatch with RuntimeClass. However, in the future we assume that they are correct.
func handlePodOverhead(ctx context.Context, cl client.Client, wl *kueue.Workload) []error {
	var errs []error
	for i := range wl.Spec.PodSets {
		podSpec := &wl.Spec.PodSets[i].Template.Spec
		if podSpec.RuntimeClassName != nil && len(podSpec.Overhead) == 0 {
			var runtimeClass nodev1.RuntimeClass
			if err := cl.Get(ctx, types.NamespacedName{Name: *podSpec.RuntimeClassName}, &runtimeClass); err != nil {
				errs = append(errs, fmt.Errorf("in podSet %s: %w", wl.Spec.PodSets[i].Name, err))
				continue
			}
			if runtimeClass.Overhead != nil {
				podSpec.Overhead = runtimeClass.Overhead.PodFixed
			}
		}
	}
	return errs
}

func handlePodLimitRange(ctx context.Context, cl client.Client, wl *kueue.Workload) error {
	// get the list of limit ranges
	var limitRanges corev1.LimitRangeList
	if err := cl.List(ctx, &limitRanges, &client.ListOptions{Namespace: wl.Namespace}, client.MatchingFields{indexer.LimitRangeHasContainerType: "true"}); err != nil {
		return err
	}

	if len(limitRanges.Items) == 0 {
		return nil
	}
	summary := limitrange.Summarize(limitRanges.Items...)
	containerLimits, found := summary[corev1.LimitTypeContainer]
	if !found {
		return nil
	}

	for pi := range wl.Spec.PodSets {
		pod := &wl.Spec.PodSets[pi].Template.Spec
		for ci := range pod.InitContainers {
			res := &pod.InitContainers[ci].Resources
			res.Limits = resource.MergeResourceListKeepFirst(res.Limits, containerLimits.Default)
			res.Requests = resource.MergeResourceListKeepFirst(res.Requests, containerLimits.DefaultRequest)
		}
		for ci := range pod.Containers {
			res := &pod.Containers[ci].Resources
			res.Limits = resource.MergeResourceListKeepFirst(res.Limits, containerLimits.Default)
			res.Requests = resource.MergeResourceListKeepFirst(res.Requests, containerLimits.DefaultRequest)
		}
	}
	return nil
}

func handleLimitsToRequests(wl *kueue.Workload) {
	for pi := range wl.Spec.PodSets {
		UseLimitsAsMissingRequestsInPod(&wl.Spec.PodSets[pi].Template.Spec)
	}
}

// UseLimitsAsMissingRequestsInPod adjust the resource requests to the limits value
// for resources that only set limits.
func UseLimitsAsMissingRequestsInPod(pod *corev1.PodSpec) {
	for ci := range pod.InitContainers {
		res := &pod.InitContainers[ci].Resources
		res.Requests = resource.MergeResourceListKeepFirst(res.Requests, res.Limits)
	}
	for ci := range pod.Containers {
		res := &pod.Containers[ci].Resources
		res.Requests = resource.MergeResourceListKeepFirst(res.Requests, res.Limits)
	}
}

// AdjustResources adjusts the resource requests of a workload based on:
// - PodOverhead
// - LimitRanges
// - Limits
func AdjustResources(ctx context.Context, cl client.Client, wl *kueue.Workload) {
	log := ctrl.LoggerFrom(ctx)
	for _, err := range handlePodOverhead(ctx, cl, wl) {
		log.Error(err, "Failures adjusting requests for pod overhead")
	}
	if err := handlePodLimitRange(ctx, cl, wl); err != nil {
		log.Error(err, "Failed adjusting requests for LimitRanges")
	}
	handleLimitsToRequests(wl)
}

// ValidateResources validates that requested resources are less or equal
// to limits.
func ValidateResources(wi *Info) field.ErrorList {
	// requests should be less than limits.
	var allErrors field.ErrorList
	for i := range wi.Obj.Spec.PodSets {
		ps := &wi.Obj.Spec.PodSets[i]
		podSpecPath := PodSetsPath.Index(i).Child("template").Child("spec")
		for i := range ps.Template.Spec.InitContainers {
			c := ps.Template.Spec.InitContainers[i]
			if resNames := resource.GetGreaterKeys(c.Resources.Requests, c.Resources.Limits); len(resNames) > 0 {
				allErrors = append(
					allErrors,
					field.Invalid(podSpecPath.Child("initContainers").Index(i), resNames, RequestsMustNotExceedLimitMessage),
				)
			}
		}

		for i := range ps.Template.Spec.Containers {
			c := ps.Template.Spec.Containers[i]
			if resNames := resource.GetGreaterKeys(c.Resources.Requests, c.Resources.Limits); len(resNames) > 0 {
				allErrors = append(
					allErrors,
					field.Invalid(podSpecPath.Child("containers").Index(i), resNames, RequestsMustNotExceedLimitMessage),
				)
			}
		}
	}
	return allErrors
}

// ValidateLimitRange validates that the requested resources fit into the namespace defined
// limitRanges.
func ValidateLimitRange(ctx context.Context, c client.Client, wi *Info) field.ErrorList {
	var allErrs field.ErrorList
	limitRanges := corev1.LimitRangeList{}
	if err := c.List(ctx, &limitRanges, &client.ListOptions{Namespace: wi.Obj.Namespace}); err != nil {
		allErrs = append(allErrs, field.InternalError(field.NewPath(""), err))
		return allErrs
	}
	if len(limitRanges.Items) == 0 {
		return nil
	}
	summary := limitrange.Summarize(limitRanges.Items...)

	// verify
	for i := range wi.Obj.Spec.PodSets {
		ps := &wi.Obj.Spec.PodSets[i]
		allErrs = append(allErrs, summary.ValidatePodSpec(&ps.Template.Spec, PodSetsPath.Index(i).Child("template").Child("spec"))...)
	}
	return allErrs
}

// GetResourceClaimTemplates will retrieve the ResourceClaimTemplate from the api server.
func GetResourceClaimTemplates(ctx context.Context, c client.Client, name, namespace string) (dra.ResourceClaimTemplate, error) {
	resourceClaimTemplate := dra.ResourceClaimTemplate{}
	err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &resourceClaimTemplate, &client.GetOptions{})
	return resourceClaimTemplate, err
}

func AddDeviceClassesToContainerRequests(ctx context.Context, cl client.Client, wl *kueue.Workload) {
	// If DRA is not enabled then this becomes a no op and workloads won't be modified.
	// There is a potential issue
	// If Kueue has this feature enabled but the k8s cluster does not have this enabled,
	// then Kueue may not be able to actually find these resources
	if !features.Enabled(features.DynamicResourceStructuredParameters) {
		return
	}

	log := ctrl.LoggerFrom(ctx)

	deviceClassMap, err := getDeviceClassToNameMap(ctx, cl, wl)
	if err != nil {
		log.Error(err, "error getting device class to resource name map")
		return
	}

	resourceList, errors := handleResourceClaimTemplate(ctx, cl, AddResourceClaimsToResourceList(wl), wl.Namespace, deviceClassMap)
	for key, val := range resourceList {
		log.Info("ResourceList", "key", key, "val", val)
	}
	for _, err := range errors {
		log.Error(err, "Failures adjusting requests for dynamic resources")
	}
	for pi := range wl.Spec.PodSets {
		resourceClaimsToContainerRequests(&wl.Spec.PodSets[pi].Template.Spec, resourceList)
	}
}

func getDeviceClassToNameMap(ctx context.Context, cl client.Client, wl *kueue.Workload) (map[corev1.ResourceName]corev1.ResourceName, error) {
	log := ctrl.LoggerFrom(ctx)
	log.WithValues("workloadName", wl.Name, "workloadNamespace", wl.Namespace)
	queueName := wl.Spec.QueueName
	if queueName == "" {
		log.Error(fmt.Errorf("queue name is empty"), "empty queue name")
		return nil, fmt.Errorf("queue name is empty")
	}

	lq := &kueue.LocalQueue{}
	err := cl.Get(ctx, client.ObjectKey{Namespace: wl.Namespace, Name: string(queueName)}, lq)
	if err != nil {
		log.Error(err, "error getting local queue")
		return nil, err
	}

	cq := &kueue.ClusterQueue{}
	err = cl.Get(ctx, client.ObjectKey{Name: string(lq.Spec.ClusterQueue)}, cq)
	if err != nil {
		log.Error(err, "error getting cluster queue")
		return nil, err
	}

	return deviceClassMapFromCQ(log, cq), nil
}

func deviceClassMapFromCQ(log logr.Logger, cq *kueue.ClusterQueue) map[corev1.ResourceName]corev1.ResourceName {
	deviceClassMap := make(map[corev1.ResourceName]corev1.ResourceName)
	for _, resourceGroup := range cq.Spec.ResourceGroups {
		for _, flavor := range resourceGroup.Flavors {
			for _, res := range flavor.Resources {
				if res.Kind != nil && *res.Kind == "DeviceClass" {
					for _, dc := range res.DeviceClassNames {
						deviceClassMap[dc] = res.Name
					}
				}
			}
		}
	}
	// TODO: change the logging verbosity below
	log.V(2).Info("deviceClassMapFromCQ", "cq", cq.Name, "deviceClassMap", deviceClassMap)
	return deviceClassMap
}

func resourceClaimsToContainerRequests(podSpec *corev1.PodSpec, resourceList corev1.ResourceList) {
	for i := range podSpec.Containers {
		res := &podSpec.Containers[i].Resources
		res.Requests = resource.MergeResourceListKeepFirst(res.Requests, resourceList)
		// just inject the dra resources to 1 container to make sure they are not being double counted.
		break
	}
}

func handleResourceClaimTemplate(ctx context.Context, cl client.Client, psr []PodSetResources, namespace string, deviceClassMap map[corev1.ResourceName]corev1.ResourceName) (corev1.ResourceList, []error) {
	var errors []error
	updateResourceList := corev1.ResourceList{}
	for _, singlePsr := range psr {
		for key, request := range singlePsr.Requests {
			// TODO: This needs to handle ResourceClaim objects as well
			draDeviceClass, err := GetResourceClaimTemplates(ctx, cl, key.String(), namespace)
			if err != nil {
				errors = append(errors, fmt.Errorf("unable to get %s/%s resource claim %v", namespace, key, err))
				continue
			}
			// TODO: 1. this needs to change with prioritized lists
			// TODO: 2. implement allocation mode of all
			// TODO: 3. need to change with partitionable devices
			for _, val := range draDeviceClass.Spec.Spec.Devices.Requests {
				if val.AllocationMode == dra.DeviceAllocationModeExactCount {
					resName, ok := deviceClassMap[corev1.ResourceName(val.DeviceClassName)]
					if !ok {
						errors = append(errors, fmt.Errorf("resource name for device class %s is empty in deviceClassMap", val.DeviceClassName))
						continue
					}
					updateResourceList[resName] = *k8sresource.NewQuantity(request*val.Count, k8sresource.DecimalSI)
				} else if val.AllocationMode == dra.DeviceAllocationModeAll {
					// TODO: implement me
				}
			}
		}
	}
	return updateResourceList, errors
}

func AddResourceClaimsToResourceList(wl *kueue.Workload) []PodSetResources {
	if len(wl.Spec.PodSets) == 0 {
		return nil
	}
	res := make([]PodSetResources, 0, len(wl.Spec.PodSets))
	podSets := &wl.Spec.PodSets
	currentCounts := podSetsCountsAfterReclaim(wl)
	for _, ps := range *podSets {
		count := currentCounts[ps.Name]
		setRes := PodSetResources{
			Name:  ps.Name,
			Count: count,
		}
		setRes.Requests = resources.NewRequests(limitrange.TotalResourceClaimsFromPodSpec(&ps.Template.Spec))
		res = append(res, setRes)
	}
	return res
}
