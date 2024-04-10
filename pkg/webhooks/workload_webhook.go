/*
Copyright 2022 The Kubernetes Authors.

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

package webhooks

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1validation "k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/slices"
	"sigs.k8s.io/kueue/pkg/workload"
)

type WorkloadWebhook struct{}

func setupWebhookForWorkload(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&kueue.Workload{}).
		WithDefaulter(&WorkloadWebhook{}).
		WithValidator(&WorkloadWebhook{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-kueue-x-k8s-io-v1beta1-workload,mutating=true,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=workloads,verbs=create,versions=v1beta1,name=mworkload.kb.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &WorkloadWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *WorkloadWebhook) Default(ctx context.Context, obj runtime.Object) error {
	wl := obj.(*kueue.Workload)
	log := ctrl.LoggerFrom(ctx).WithName("workload-webhook")
	log.V(5).Info("Applying defaults", "workload", klog.KObj(wl))

	// Only when we have one podSet and its name is empty,
	// we'll set it to the default name `main`.
	if len(wl.Spec.PodSets) == 1 {
		podSet := &wl.Spec.PodSets[0]
		if len(podSet.Name) == 0 {
			podSet.Name = kueue.DefaultPodSetName
		}
	}

	// drop minCounts if PartialAdmission is not enabled
	if !features.Enabled(features.PartialAdmission) {
		for i := range wl.Spec.PodSets {
			wl.Spec.PodSets[i].MinCount = nil
		}
	}

	return nil
}

// +kubebuilder:webhook:path=/validate-kueue-x-k8s-io-v1beta1-workload,mutating=false,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=workloads;workloads/status,verbs=create;update,versions=v1beta1,name=vworkload.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &WorkloadWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *WorkloadWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	wl := obj.(*kueue.Workload)
	log := ctrl.LoggerFrom(ctx).WithName("workload-webhook")
	log.V(5).Info("Validating create", "workload", klog.KObj(wl))
	return nil, ValidateWorkload(wl).ToAggregate()
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *WorkloadWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	newWL := newObj.(*kueue.Workload)
	oldWL := oldObj.(*kueue.Workload)
	log := ctrl.LoggerFrom(ctx).WithName("workload-webhook")
	log.V(5).Info("Validating update", "workload", klog.KObj(newWL))
	return nil, ValidateWorkloadUpdate(newWL, oldWL).ToAggregate()
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *WorkloadWebhook) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func ValidateWorkload(obj *kueue.Workload) field.ErrorList {
	var allErrs field.ErrorList
	specPath := field.NewPath("spec")

	variableCountPosets := 0
	for i := range obj.Spec.PodSets {
		ps := &obj.Spec.PodSets[i]
		allErrs = append(allErrs, validatePodSet(ps, specPath.Child("podSets").Index(i))...)
		if ps.MinCount != nil {
			variableCountPosets++
		}
	}

	if variableCountPosets > 1 {
		allErrs = append(allErrs, field.Invalid(specPath.Child("podSets"), variableCountPosets, "at most one podSet can use minCount"))
	}

	if len(obj.Spec.PriorityClassName) > 0 {
		msgs := validation.IsDNS1123Subdomain(obj.Spec.PriorityClassName)
		if len(msgs) > 0 {
			for _, msg := range msgs {
				allErrs = append(allErrs, field.Invalid(specPath.Child("priorityClassName"), obj.Spec.PriorityClassName, msg))
			}
		}
		if obj.Spec.Priority == nil {
			allErrs = append(allErrs, field.Invalid(specPath.Child("priority"), obj.Spec.Priority, "priority should not be nil when priorityClassName is set"))
		}
	}

	if len(obj.Spec.QueueName) > 0 {
		allErrs = append(allErrs, validateNameReference(obj.Spec.QueueName, specPath.Child("queueName"))...)
	}

	statusPath := field.NewPath("status")
	if workload.HasQuotaReservation(obj) {
		allErrs = append(allErrs, validateAdmission(obj, statusPath.Child("admission"))...)
	}

	allErrs = append(allErrs, metav1validation.ValidateConditions(obj.Status.Conditions, statusPath.Child("conditions"))...)
	allErrs = append(allErrs, validateReclaimablePods(obj, statusPath.Child("reclaimablePods"))...)
	allErrs = append(allErrs, validateAdmissionChecks(obj, statusPath.Child("admissionChecks"))...)

	return allErrs
}

func validatePodSet(ps *kueue.PodSet, path *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	// Apply the same validation as container names.
	for _, msg := range validation.IsDNS1123Label(ps.Name) {
		allErrs = append(allErrs, field.Invalid(path.Child("name"), ps.Name, msg))
	}

	// validate initContainers
	icPath := path.Child("template", "spec", "initContainers")
	for ci := range ps.Template.Spec.InitContainers {
		allErrs = append(allErrs, validateContainer(&ps.Template.Spec.InitContainers[ci], icPath.Index(ci))...)
	}
	// validate containers
	cPath := path.Child("template", "spec", "containers")
	for ci := range ps.Template.Spec.Containers {
		allErrs = append(allErrs, validateContainer(&ps.Template.Spec.Containers[ci], cPath.Index(ci))...)
	}

	if min := ptr.Deref(ps.MinCount, ps.Count); min > ps.Count || min < 0 {
		allErrs = append(allErrs, field.Forbidden(path.Child("minCount"), fmt.Sprintf("%d should be positive and less or equal to %d", min, ps.Count)))
	}

	return allErrs
}

func validateContainer(c *corev1.Container, path *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	rPath := path.Child("resources", "requests")
	for name := range c.Resources.Requests {
		if name == corev1.ResourcePods {
			allErrs = append(allErrs, field.Invalid(rPath.Key(string(name)), corev1.ResourcePods, "the key is reserved for internal kueue use"))
		}
	}
	return allErrs
}

func validateAdmissionChecks(obj *kueue.Workload, basePath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	for i := range obj.Status.AdmissionChecks {
		admissionChecksPath := basePath.Index(i)
		ac := &obj.Status.AdmissionChecks[i]
		if len(ac.PodSetUpdates) > 0 && len(ac.PodSetUpdates) != len(obj.Spec.PodSets) {
			allErrs = append(allErrs, field.Invalid(admissionChecksPath.Child("podSetUpdates"), field.OmitValueType{}, "must have the same number of podSetUpdates as the podSets"))
		}
		allErrs = append(allErrs, validatePodSetUpdates(ac, obj, admissionChecksPath.Child("podSetUpdates"))...)
	}
	return allErrs
}

func validatePodSetUpdates(acs *kueue.AdmissionCheckState, obj *kueue.Workload, basePath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	knowPodSets := sets.New(slices.Map(obj.Spec.PodSets, func(ps *kueue.PodSet) string {
		return ps.Name
	})...)

	for i := range acs.PodSetUpdates {
		psu := &acs.PodSetUpdates[i]
		psuPath := basePath.Index(i)
		if !knowPodSets.Has(psu.Name) {
			allErrs = append(allErrs, field.NotSupported(psuPath.Child("name"), psu.Name, sets.List(knowPodSets)))
		}
		allErrs = append(allErrs, validateTolerations(psu.Tolerations, psuPath.Child("tolerations"))...)
		allErrs = append(allErrs, apivalidation.ValidateAnnotations(psu.Annotations, psuPath.Child("annotations"))...)
		allErrs = append(allErrs, metav1validation.ValidateLabels(psu.NodeSelector, psuPath.Child("nodeSelector"))...)
		allErrs = append(allErrs, metav1validation.ValidateLabels(psu.Labels, psuPath.Child("labels"))...)
	}
	return allErrs
}

func validateImmutablePodSetUpdates(newObj, oldObj *kueue.Workload, basePath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	newAcs := slices.ToRefMap(newObj.Status.AdmissionChecks, func(f *kueue.AdmissionCheckState) string { return f.Name })
	for i := range oldObj.Status.AdmissionChecks {
		oldAc := &oldObj.Status.AdmissionChecks[i]
		newAc, found := newAcs[oldAc.Name]
		if !found {
			continue
		}
		if oldAc.State == kueue.CheckStateReady && newAc.State == kueue.CheckStateReady {
			allErrs = append(allErrs, apivalidation.ValidateImmutableField(newAc.PodSetUpdates, oldAc.PodSetUpdates, basePath.Index(i).Child("podSetUpdates"))...)
		}
	}
	return allErrs
}

// validateTolerations is extracted from git.k8s.io/kubernetes/pkg/apis/core/validation/validation.go
// we do not import it as dependency, see the comment:
// https://github.com/kubernetes/kubernetes/issues/79384#issuecomment-505627280
func validateTolerations(tolerations []corev1.Toleration, fldPath *field.Path) field.ErrorList {
	allErrors := field.ErrorList{}
	for i, toleration := range tolerations {
		idxPath := fldPath.Index(i)
		// validate the toleration key
		if len(toleration.Key) > 0 {
			allErrors = append(allErrors, metav1validation.ValidateLabelName(toleration.Key, idxPath.Child("key"))...)
		}

		// empty toleration key with Exists operator and empty value means match all taints
		if len(toleration.Key) == 0 && toleration.Operator != corev1.TolerationOpExists {
			allErrors = append(allErrors, field.Invalid(idxPath.Child("operator"), toleration.Operator,
				"operator must be Exists when `key` is empty, which means \"match all values and all keys\""))
		}

		if toleration.TolerationSeconds != nil && toleration.Effect != corev1.TaintEffectNoExecute {
			allErrors = append(allErrors, field.Invalid(idxPath.Child("effect"), toleration.Effect,
				"effect must be 'NoExecute' when `tolerationSeconds` is set"))
		}

		// validate toleration operator and value
		switch toleration.Operator {
		// empty operator means Equal
		case corev1.TolerationOpEqual, "":
			if errs := validation.IsValidLabelValue(toleration.Value); len(errs) != 0 {
				allErrors = append(allErrors, field.Invalid(idxPath.Child("operator"), toleration.Value, strings.Join(errs, ";")))
			}
		case corev1.TolerationOpExists:
			if len(toleration.Value) > 0 {
				allErrors = append(allErrors, field.Invalid(idxPath.Child("operator"), toleration, "value must be empty when `operator` is 'Exists'"))
			}
		default:
			validValues := []string{string(corev1.TolerationOpEqual), string(corev1.TolerationOpExists)}
			allErrors = append(allErrors, field.NotSupported(idxPath.Child("operator"), toleration.Operator, validValues))
		}

		// validate toleration effect, empty toleration effect means match all taint effects
		if len(toleration.Effect) > 0 {
			allErrors = append(allErrors, validateTaintEffect(&toleration.Effect, true, idxPath.Child("effect"))...)
		}
	}
	return allErrors
}

func validateAdmission(obj *kueue.Workload, path *field.Path) field.ErrorList {
	admission := obj.Status.Admission
	var allErrs field.ErrorList
	allErrs = append(allErrs, validateNameReference(string(admission.ClusterQueue), path.Child("clusterQueue"))...)

	names := sets.New[string]()
	for _, ps := range obj.Spec.PodSets {
		names.Insert(ps.Name)
	}
	assignmentsPath := path.Child("podSetAssignments")
	if names.Len() != len(admission.PodSetAssignments) {
		allErrs = append(allErrs, field.Invalid(assignmentsPath, field.OmitValueType{}, "must have the same number of podSets as the spec"))
	}

	for i, ps := range admission.PodSetAssignments {
		psaPath := assignmentsPath.Index(i)
		if !names.Has(ps.Name) {
			allErrs = append(allErrs, field.NotFound(psaPath.Child("name"), ps.Name))
		}
		if count := ptr.Deref(ps.Count, 0); count > 0 {
			for k, v := range ps.ResourceUsage {
				if (workload.ResourceValue(k, v) % int64(count)) != 0 {
					allErrs = append(allErrs, field.Invalid(psaPath.Child("resourceUsage").Key(string(k)), v, fmt.Sprintf("is not a multiple of %d", ps.Count)))
				}
			}
		}
	}

	return allErrs
}

func validateReclaimablePods(obj *kueue.Workload, basePath *field.Path) field.ErrorList {
	if len(obj.Status.ReclaimablePods) == 0 {
		return nil
	}
	knowPodSets := make(map[string]*kueue.PodSet, len(obj.Spec.PodSets))
	knowPodSetNames := make([]string, len(obj.Spec.PodSets))
	for i := range obj.Spec.PodSets {
		name := obj.Spec.PodSets[i].Name
		knowPodSets[name] = &obj.Spec.PodSets[i]
		knowPodSetNames = append(knowPodSetNames, name)
	}

	var ret field.ErrorList
	for i := range obj.Status.ReclaimablePods {
		rps := &obj.Status.ReclaimablePods[i]
		ps, found := knowPodSets[rps.Name]
		rpsPath := basePath.Key(rps.Name)
		if !found {
			ret = append(ret, field.NotSupported(rpsPath.Child("name"), rps.Name, knowPodSetNames))
		} else if rps.Count > ps.Count {
			ret = append(ret, field.Invalid(rpsPath.Child("count"), rps.Count, fmt.Sprintf("should be less or equal to %d", ps.Count)))
		}
	}
	return ret
}

func ValidateWorkloadUpdate(newObj, oldObj *kueue.Workload) field.ErrorList {
	var allErrs field.ErrorList
	specPath := field.NewPath("spec")
	statusPath := field.NewPath("status")
	allErrs = append(allErrs, ValidateWorkload(newObj)...)

	if workload.HasQuotaReservation(oldObj) {
		allErrs = append(allErrs, apivalidation.ValidateImmutableField(newObj.Spec.PodSets, oldObj.Spec.PodSets, specPath.Child("podSets"))...)
		allErrs = append(allErrs, apivalidation.ValidateImmutableField(newObj.Spec.PriorityClassSource, oldObj.Spec.PriorityClassSource, specPath.Child("priorityClassSource"))...)
		allErrs = append(allErrs, apivalidation.ValidateImmutableField(newObj.Spec.PriorityClassName, oldObj.Spec.PriorityClassName, specPath.Child("priorityClassName"))...)
	}
	if workload.HasQuotaReservation(newObj) && workload.HasQuotaReservation(oldObj) {
		allErrs = append(allErrs, apivalidation.ValidateImmutableField(newObj.Spec.QueueName, oldObj.Spec.QueueName, specPath.Child("queueName"))...)
		allErrs = append(allErrs, validateReclaimablePodsUpdate(newObj, oldObj, field.NewPath("status", "reclaimablePods"))...)
	}
	allErrs = append(allErrs, validateAdmissionUpdate(newObj.Status.Admission, oldObj.Status.Admission, field.NewPath("status", "admission"))...)
	allErrs = append(allErrs, validateImmutablePodSetUpdates(newObj, oldObj, statusPath.Child("admissionChecks"))...)

	return allErrs
}

// validateAdmissionUpdate validates that admission can be set or unset, but the
// fields within can't change.
func validateAdmissionUpdate(new, old *kueue.Admission, path *field.Path) field.ErrorList {
	if old == nil || new == nil {
		return nil
	}
	return apivalidation.ValidateImmutableField(new, old, path)
}

// validateReclaimablePodsUpdate validates that the reclaimable counts do not decrease, this should be checked
// while the workload is admitted.
func validateReclaimablePodsUpdate(newObj, oldObj *kueue.Workload, basePath *field.Path) field.ErrorList {
	if workload.ReclaimablePodsAreEqual(newObj.Status.ReclaimablePods, oldObj.Status.ReclaimablePods) {
		return nil
	}

	if len(oldObj.Status.ReclaimablePods) == 0 {
		return nil
	}

	knowPodSets := make(map[string]*kueue.ReclaimablePod, len(oldObj.Status.ReclaimablePods))
	for i := range oldObj.Status.ReclaimablePods {
		name := oldObj.Status.ReclaimablePods[i].Name
		knowPodSets[name] = &oldObj.Status.ReclaimablePods[i]
	}

	var ret field.ErrorList
	newNames := sets.New[string]()
	for i := range newObj.Status.ReclaimablePods {
		newCount := &newObj.Status.ReclaimablePods[i]
		newNames.Insert(newCount.Name)
		if !workload.HasQuotaReservation(newObj) && newCount.Count == 0 {
			continue
		}
		oldCount, found := knowPodSets[newCount.Name]
		if found && newCount.Count < oldCount.Count {
			ret = append(ret, field.Invalid(basePath.Key(newCount.Name).Child("count"), newCount.Count, fmt.Sprintf("cannot be less then %d", oldCount.Count)))
		}
	}

	for name := range knowPodSets {
		if workload.HasQuotaReservation(newObj) && !newNames.Has(name) {
			ret = append(ret, field.Required(basePath.Key(name), "cannot be removed"))
		}
	}
	return ret
}
