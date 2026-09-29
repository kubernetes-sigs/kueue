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

package webhooks

import (
	"fmt"

	"k8s.io/apimachinery/pkg/util/validation/field"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/orderedgroups"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

// On update, spreading checks run only for PodSets and named groups whose
// spreading inputs changed.
func validateTopologySpreading(obj, oldObj *kueue.Workload) field.ErrorList {
	if !features.Enabled(features.TASTopologySpreading) {
		return nil
	}
	var allErrs field.ErrorList
	podSetsPath := field.NewPath("spec", "podSets")
	oldByName := map[kueue.PodSetReference]*kueue.PodSet{}
	if oldObj != nil {
		for i := range oldObj.Spec.PodSets {
			ps := &oldObj.Spec.PodSets[i]
			oldByName[ps.Name] = ps
		}
	}
	for i := range obj.Spec.PodSets {
		ps := &obj.Spec.PodSets[i]
		if oldPS, found := oldByName[ps.Name]; found && podSetSpreadingInputsUnchanged(oldPS, ps) {
			continue
		}
		allErrs = append(allErrs, validatePodSetSpreading(ps, podSetsPath.Index(i))...)
	}
	allErrs = append(allErrs, validateSpreadingGroups(obj.Spec.PodSets, oldObj, podSetsPath)...)
	return allErrs
}

// Compare raw inputs so rewriting an invalid annotation triggers validation.
func podSetSpreadingInputsUnchanged(oldPS, newPS *kueue.PodSet) bool {
	oldValue, oldFound := spreadingAnnotation(oldPS)
	newValue, newFound := spreadingAnnotation(newPS)
	if oldFound != newFound || oldValue != newValue {
		return false
	}
	oldReq, oldReqFound := requiredTopology(oldPS)
	newReq, newReqFound := requiredTopology(newPS)
	return oldReqFound == newReqFound && oldReq == newReq
}

func spreadingAnnotation(ps *kueue.PodSet) (string, bool) {
	value, found := ps.Template.Annotations[kueue.PodSetTopologySpreadingAnnotation]
	return value, found
}

func requiredTopology(ps *kueue.PodSet) (string, bool) {
	if ps.TopologyRequest == nil || ps.TopologyRequest.Required == nil {
		return "", false
	}
	return *ps.TopologyRequest.Required, true
}

func validatePodSetSpreading(ps *kueue.PodSet, path *field.Path) field.ErrorList {
	value, found := spreadingAnnotation(ps)
	if !found {
		return nil
	}

	annPath := path.Child("template", "metadata", "annotations").Key(kueue.PodSetTopologySpreadingAnnotation)
	if _, requiredFound := requiredTopology(ps); !requiredFound {
		return field.ErrorList{field.Forbidden(annPath,
			fmt.Sprintf("may only be set together with %s", path.Child("topologyRequest", "required")))}
	}

	return utiltas.ValidateSpreadingAnnotation(annPath, value)
}

// validateSpreadingGroups requires members of a named group to agree on
// spreading annotations, or all to omit them.
func validateSpreadingGroups(podSets []kueue.PodSet, oldObj *kueue.Workload, podSetsPath *field.Path) field.ErrorList {
	podSetGroups := orderedgroups.NewOrderedGroups[string, int]()
	for i := range podSets {
		ps := &podSets[i]
		if ps.TopologyRequest == nil || ps.TopologyRequest.PodSetGroupName == nil {
			continue
		}
		podSetGroups.Insert(*ps.TopologyRequest.PodSetGroupName, i)
	}

	var oldGroups map[string]map[kueue.PodSetReference]spreadingAnnotationInput
	if oldObj != nil {
		oldGroups = spreadingGroupsByName(oldObj.Spec.PodSets)
	}

	var allErrs field.ErrorList
	for groupName, idxs := range podSetGroups.InOrder {
		if oldMembers, found := oldGroups[groupName]; found && spreadingGroupInputsUnchanged(oldMembers, podSets, idxs) {
			continue
		}
		refIdx, refValue, ok := firstSpreadingAnnotation(podSets, idxs)
		if !ok {
			continue
		}
		refPath := podSetSpreadingAnnotationPath(podSetsPath, refIdx)
		for _, i := range idxs {
			value, found := spreadingAnnotation(&podSets[i])
			if found && utiltas.SpreadingAnnotationsAgree(value, refValue) {
				continue
			}
			allErrs = append(allErrs, field.Invalid(
				podSetSpreadingAnnotationPath(podSetsPath, i),
				field.OmitValueType{},
				fmt.Sprintf(
					"must specify the same '%s' annotation as '%s' in group '%s', or omit the annotation from all members of the group",
					kueue.PodSetTopologySpreadingAnnotation,
					refPath,
					groupName,
				),
			))
		}
	}
	return allErrs
}

type spreadingAnnotationInput struct {
	present bool
	value   string
}

func spreadingGroupsByName(podSets []kueue.PodSet) map[string]map[kueue.PodSetReference]spreadingAnnotationInput {
	out := make(map[string]map[kueue.PodSetReference]spreadingAnnotationInput)
	for i := range podSets {
		ps := &podSets[i]
		if ps.TopologyRequest == nil || ps.TopologyRequest.PodSetGroupName == nil {
			continue
		}
		groupName := *ps.TopologyRequest.PodSetGroupName
		members, ok := out[groupName]
		if !ok {
			members = make(map[kueue.PodSetReference]spreadingAnnotationInput)
			out[groupName] = members
		}
		value, found := spreadingAnnotation(ps)
		members[ps.Name] = spreadingAnnotationInput{present: found, value: value}
	}
	return out
}

func spreadingGroupInputsUnchanged(oldMembers map[kueue.PodSetReference]spreadingAnnotationInput, podSets []kueue.PodSet, idxs []int) bool {
	if len(oldMembers) != len(idxs) {
		return false
	}
	for _, i := range idxs {
		old, found := oldMembers[podSets[i].Name]
		if !found {
			return false
		}
		value, present := spreadingAnnotation(&podSets[i])
		if old.present != present || old.value != value {
			return false
		}
	}
	return true
}

// firstSpreadingAnnotation returns the first group member that carries the
// spreading annotation, including an empty value.
func firstSpreadingAnnotation(podSets []kueue.PodSet, idxs []int) (int, string, bool) {
	for _, i := range idxs {
		value, found := spreadingAnnotation(&podSets[i])
		if found {
			return i, value, true
		}
	}
	return 0, "", false
}

func podSetSpreadingAnnotationPath(podSetsPath *field.Path, i int) *field.Path {
	return podSetsPath.Index(i).Child("template", "metadata", "annotations").Key(kueue.PodSetTopologySpreadingAnnotation)
}
