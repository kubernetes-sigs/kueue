/*
Copyright 2024 IBM Corporation.

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

package utils

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	dockerref "github.com/distribution/reference"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	jobsetapi "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"

	//  Import the crypto sha256 algorithm for the docker image parser to work
	_ "crypto/sha256"
	//  Import the crypto/sha512 algorithm for the docker image parser to work with 384 and 512 sha hashes
	_ "crypto/sha512"
)

const templateString = "template"

const (
	PodSetAnnotationTASPodIndexLabel      = "workload.codeflare.dev.appwrapper/tas-pod-index-label"
	PodSetAnnotationTASSubGroupIndexLabel = "workload.codeflare.dev.appwrapper/tas-sub-group-index-label"
	PodSetAnnotationTASSubGroupCount      = "workload.codeflare.dev.appwrapper/tas-sub-group-count"
)

// GetPodTemplateSpec extracts a Kueue-compatible PodTemplateSpec at the given path within obj
func GetPodTemplateSpec(obj *unstructured.Unstructured, path string) (*v1.PodTemplateSpec, error) {
	candidatePTS, err := GetRawTemplate(obj.UnstructuredContent(), path)
	if err != nil {
		return nil, err
	}

	// Convert candidatePTS.spec to a natively-typed PodSpec
	// NOTE: candidatePTS _may_ be a Pod, not a PodSpecTemplate so only parse the Spec.
	src := &v1.PodSpec{}
	spec, ok := candidatePTS["spec"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("content at %v does not contain a spec", path)
	}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructuredWithValidation(spec, src, true); err != nil {
		return nil, fmt.Errorf("content at %v.spec not parseable as a v1.PodSpec: %w", path, err)
	}

	// Now, copy just the subset of src that is relevant to Kueue.
	// We must deeply ensure that any fields with non-zero default values
	// are copied as-if src had been processed by the APIServer.
	// This ensures proper operation of Kueue's ComparePodSetSlices

	// Metadata
	dst := &v1.PodTemplateSpec{}
	if metadata, ok := candidatePTS["metadata"].(map[string]interface{}); ok {
		if labels, ok := metadata["labels"].(map[string]interface{}); ok {
			dst.Labels = make(map[string]string)
			for k, v := range labels {
				if str, ok := v.(string); ok {
					dst.Labels[k] = str
				}
			}
		}
		if annotations, ok := metadata["annotations"].(map[string]interface{}); ok {
			dst.Annotations = make(map[string]string)
			for k, v := range annotations {
				if str, ok := v.(string); ok {
					dst.Annotations[k] = str
				}
			}
		}
	}

	// Spec
	if len(src.InitContainers) > 0 {
		dst.Spec.InitContainers = copyContainers(src.InitContainers)
	}
	dst.Spec.Containers = copyContainers(src.Containers)
	if src.RestartPolicy == "" {
		dst.Spec.RestartPolicy = v1.RestartPolicyAlways
	} else {
		dst.Spec.RestartPolicy = src.RestartPolicy
	}
	if src.TerminationGracePeriodSeconds == nil {
		tmp := int64(v1.DefaultTerminationGracePeriodSeconds)
		dst.Spec.TerminationGracePeriodSeconds = &tmp
	}
	if src.DNSPolicy == "" {
		dst.Spec.DNSPolicy = v1.DNSClusterFirst
	} else {
		dst.Spec.DNSPolicy = src.DNSPolicy
	}
	dst.Spec.NodeSelector = src.NodeSelector
	dst.Spec.NodeName = src.NodeName
	if src.SecurityContext == nil {
		dst.Spec.SecurityContext = &v1.PodSecurityContext{}
	} else {
		dst.Spec.SecurityContext = src.SecurityContext
	}
	dst.Spec.Affinity = src.Affinity
	if src.SchedulerName == "" {
		dst.Spec.SchedulerName = v1.DefaultSchedulerName
	} else {
		dst.Spec.SchedulerName = src.SchedulerName
	}
	dst.Spec.Tolerations = src.Tolerations
	dst.Spec.PriorityClassName = src.PriorityClassName
	// Intentionally not copying/defaulting Priority; Kueue computes Workload.Priority from PriorityClassName and ignores Priority
	if src.PreemptionPolicy == nil {
		tmp := v1.PreemptLowerPriority
		dst.Spec.PreemptionPolicy = &tmp
	} else {
		dst.Spec.PreemptionPolicy = src.PreemptionPolicy
	}
	dst.Spec.Overhead = defaultResourceList(src.Overhead)
	dst.Spec.TopologySpreadConstraints = src.TopologySpreadConstraints
	return dst, nil
}

func copyContainers(src []v1.Container) []v1.Container {
	dst := make([]v1.Container, len(src))
	for i := range src {
		dst[i].Name = src[i].Name
		dst[i].Image = src[i].Image
		dst[i].Command = src[i].Command
		dst[i].Args = src[i].Args
		dst[i].Resources.Requests = defaultResourceList(src[i].Resources.Requests)
		dst[i].Resources.Limits = defaultResourceList(src[i].Resources.Limits)
		if src[i].TerminationMessagePath == "" {
			dst[i].TerminationMessagePath = v1.TerminationMessagePathDefault
		} else {
			dst[i].TerminationMessagePath = src[i].TerminationMessagePath
		}
		if src[i].TerminationMessagePolicy == "" {
			dst[i].TerminationMessagePolicy = v1.TerminationMessageReadFile
		} else {
			dst[i].TerminationMessagePolicy = src[i].TerminationMessagePolicy
		}
		if src[i].ImagePullPolicy == "" {
			if getImageTag(src[i].Image) == "latest" {
				dst[i].ImagePullPolicy = v1.PullAlways
			} else {
				dst[i].ImagePullPolicy = v1.PullIfNotPresent
			}
		} else {
			dst[i].ImagePullPolicy = src[i].ImagePullPolicy
		}
		dst[i].SecurityContext = src[i].SecurityContext
	}

	return dst
}

func defaultResourceList(rl v1.ResourceList) v1.ResourceList {
	for key, val := range rl {
		const milliScale = -3
		val.RoundUp(milliScale)
		rl[key] = val
	}
	return rl
}

// getImageTag parses a docker image string and returns the tag.
// If both tag and digest are empty,"latest" will be returned.
func getImageTag(image string) string {
	named, err := dockerref.ParseNormalizedNamed(image)
	if err != nil {
		return ""
	}
	var tag, digest string
	tagged, ok := named.(dockerref.Tagged)
	if ok {
		tag = tagged.Tag()
	}
	digested, ok := named.(dockerref.Digested)
	if ok {
		digest = digested.Digest().String()
	}
	// If no tag was specified, use the default "latest".
	if len(tag) == 0 && len(digest) == 0 {
		tag = "latest"
	}
	return tag
}

// GetReplicas parses the value at the given path within obj as an int
func GetReplicas(obj *unstructured.Unstructured, path string) (int32, error) {
	value, err := getValueAtPath(obj.UnstructuredContent(), path)
	if err != nil {
		return 0, err
	}
	switch v := value.(type) {
	case int:
		return int32(v), nil
	case int32:
		return v, nil
	case int64:
		return int32(v), nil
	default:
		return 0, fmt.Errorf("at path position '%v' non-int value %v", path, value)
	}
}

// return the subobject found at the given path, or nil if the path is invalid
func GetRawTemplate(obj map[string]interface{}, path string) (map[string]interface{}, error) {
	value, err := getValueAtPath(obj, path)
	if err != nil {
		return nil, err
	}
	if asMap, ok := value.(map[string]interface{}); ok {
		return asMap, nil
	} else {
		return nil, fmt.Errorf("at path position '%v' non-map value", path)
	}
}

// get the value found at the given path or an error if the path is invalid
func getValueAtPath(obj map[string]interface{}, path string) (interface{}, error) {
	processed := templateString
	if !strings.HasPrefix(path, processed) {
		return nil, fmt.Errorf("first element of the path must be 'template'")
	}
	remaining := strings.TrimPrefix(path, processed)
	var cursor interface{} = obj

	for remaining != "" {
		if strings.HasPrefix(remaining, "[") {
			// Array index expression
			end := strings.Index(remaining, "]")
			if end < 0 {
				return nil, fmt.Errorf("at path position '%v' invalid array index '%v'", processed, remaining)
			}
			index, err := strconv.Atoi(remaining[1:end])
			if err != nil {
				return nil, fmt.Errorf("at path position '%v' invalid index expression '%v'", processed, remaining[1:end])
			}
			asArray, ok := cursor.([]interface{})
			if !ok {
				return nil, fmt.Errorf("at path position '%v' found non-array value", processed)
			}
			if index < 0 || index >= len(asArray) {
				return nil, fmt.Errorf("at path position '%v' out of bounds index '%v'", processed, index)
			}
			cursor = asArray[index]
			processed += remaining[0:end]
			remaining = remaining[end+1:]
		} else if strings.HasPrefix(remaining, ".") {
			// Field reference expression
			remaining = remaining[1:]
			processed += "."
			end := len(remaining)
			if dotIdx := strings.Index(remaining, "."); dotIdx > 0 {
				end = dotIdx
			}
			if bracketIdx := strings.Index(remaining, "["); bracketIdx > 0 && bracketIdx < end {
				end = bracketIdx
			}
			key := remaining[:end]
			asMap, ok := cursor.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("at path position '%v' non-map value", processed)
			}
			cursor, ok = asMap[key]
			if !ok {
				return nil, fmt.Errorf("at path position '%v' missing field '%v'", processed, key)
			}
			remaining = strings.TrimPrefix(remaining, key)
			processed += key
		} else {
			return nil, fmt.Errorf("at path position '%v' invalid path element '%v'", processed, remaining)
		}
	}

	return cursor, nil
}

func Replicas(ps awv1beta2.AppWrapperPodSet) int32 {
	if ps.Replicas == nil {
		return 1
	} else {
		return *ps.Replicas
	}
}

func ExpectedPodCount(aw *awv1beta2.AppWrapper) (int32, error) {
	if err := EnsureComponentStatusInitialized(aw); err != nil {
		return 0, err
	}
	var expected int32
	for _, c := range aw.Status.ComponentStatus {
		for _, s := range c.PodSets {
			expected += Replicas(s)
		}
	}
	return expected, nil
}

// EnsureComponentStatusInitialized initializes aw.Status.ComponenetStatus, including performing PodSet inference for known GVKs
func EnsureComponentStatusInitialized(aw *awv1beta2.AppWrapper) error {
	if len(aw.Status.ComponentStatus) == len(aw.Spec.Components) {
		return nil
	}

	// Construct definitive PodSets from the Spec + InferPodSets and cache in the Status (to avoid clashing with user updates to the Spec via apply)
	compStatus := make([]awv1beta2.AppWrapperComponentStatus, len(aw.Spec.Components))
	for idx := range aw.Spec.Components {
		if len(aw.Spec.Components[idx].DeclaredPodSets) > 0 {
			compStatus[idx].PodSets = aw.Spec.Components[idx].DeclaredPodSets
		} else {
			obj := &unstructured.Unstructured{}
			if _, _, err := unstructured.UnstructuredJSONScheme.Decode(aw.Spec.Components[idx].Template.Raw, nil, obj); err != nil {
				// Transient error; Template.Raw was validated by our AdmissionController
				return err
			}
			podSets, err := InferPodSets(obj)
			if err != nil {
				// Transient error; InferPodSets was validated by our AdmissionController
				return err
			}
			compStatus[idx].PodSets = podSets
		}
	}
	aw.Status.ComponentStatus = compStatus
	return nil
}

func GetComponentPodSpecs(aw *awv1beta2.AppWrapper) ([]*v1.PodTemplateSpec, []awv1beta2.AppWrapperPodSet, error) {
	templates := []*v1.PodTemplateSpec{}
	podSets := []awv1beta2.AppWrapperPodSet{}
	if err := EnsureComponentStatusInitialized(aw); err != nil {
		return nil, nil, err
	}
	for idx := range aw.Status.ComponentStatus {
		if len(aw.Status.ComponentStatus[idx].PodSets) > 0 {
			obj := &unstructured.Unstructured{}
			if _, _, err := unstructured.UnstructuredJSONScheme.Decode(aw.Spec.Components[idx].Template.Raw, nil, obj); err != nil {
				// Should be unreachable; Template.Raw validated by AppWrapper AdmissionController
				return nil, nil, err
			}
			for _, podSet := range aw.Status.ComponentStatus[idx].PodSets {
				if template, err := GetPodTemplateSpec(obj, podSet.Path); err == nil {
					templates = append(templates, template)
					podSets = append(podSets, podSet)
				}
			}
		}
	}
	return templates, podSets, nil
}

// SetPodSetInfos propagates podSetsInfo into the PodSetInfos of aw.Spec.Components
func SetPodSetInfos(aw *awv1beta2.AppWrapper, podSetsInfo []awv1beta2.AppWrapperPodSetInfo) error {
	if err := EnsureComponentStatusInitialized(aw); err != nil {
		return err
	}
	podSetsInfoIndex := 0
	for idx := range aw.Spec.Components {
		if len(aw.Spec.Components[idx].PodSetInfos) != len(aw.Status.ComponentStatus[idx].PodSets) {
			aw.Spec.Components[idx].PodSetInfos = make([]awv1beta2.AppWrapperPodSetInfo, len(aw.Status.ComponentStatus[idx].PodSets))
		}
		for podSetIdx := range aw.Status.ComponentStatus[idx].PodSets {
			podSetsInfoIndex += 1
			if podSetsInfoIndex > len(podSetsInfo) {
				continue // we will return an error below...continuing to get an accurate count for the error message
			}
			aw.Spec.Components[idx].PodSetInfos[podSetIdx] = podSetsInfo[podSetsInfoIndex-1]
		}
	}

	if podSetsInfoIndex != len(podSetsInfo) {
		return fmt.Errorf("expecting %d podsets, got %d", podSetsInfoIndex, len(podSetsInfo))
	}
	return nil
}

// ClearPodSetInfos clears the PodSetInfos saved by SetPodSetInfos
func ClearPodSetInfos(aw *awv1beta2.AppWrapper) bool {
	for idx := range aw.Spec.Components {
		aw.Spec.Components[idx].PodSetInfos = nil
	}
	return true
}

// inferReplicas parses the value at the given path within obj as an int or return 1 or error
func inferReplicas(obj map[string]interface{}, path string) (int32, error) {
	if path == "" {
		// no path specified, default to one replica
		return 1, nil
	}

	// check obj is well formed
	index := strings.LastIndex(path, ".")
	if index >= 0 {
		var err error
		obj, err = GetRawTemplate(obj, path[:index])
		if err != nil {
			return 0, err
		}
	}

	// check type and value
	switch v := obj[path[index+1:]].(type) {
	case nil:
		return 1, nil // default to 1
	case int:
		return int32(v), nil
	case int32:
		return v, nil
	case int64:
		return int32(v), nil
	default:
		return 0, fmt.Errorf("at path position '%v' non-int value %v", path, v)
	}
}

// where to find a replica count and a PodTemplateSpec in a resource
type resourceTemplate struct {
	path     string // path to pod template spec
	replicas string // path to replica count
}

// map from known GVKs to resource templates
var templatesForGVK = map[schema.GroupVersionKind][]resourceTemplate{
	{Group: "", Version: "v1", Kind: "Pod"}:             {{path: "template"}},
	{Group: "apps", Version: "v1", Kind: "Deployment"}:  {{path: "template.spec.template", replicas: "template.spec.replicas"}},
	{Group: "apps", Version: "v1", Kind: "StatefulSet"}: {{path: "template.spec.template", replicas: "template.spec.replicas"}},
}

// inferPodSets infers PodSets for RayJobs and RayClusters
func inferRayPodSets(obj *unstructured.Unstructured, clusterSpecPrefix string) ([]awv1beta2.AppWrapperPodSet, error) {
	podSets := []awv1beta2.AppWrapperPodSet{}

	podSets = append(podSets, awv1beta2.AppWrapperPodSet{Replicas: ptr.To(int32(1)), Path: clusterSpecPrefix + "headGroupSpec.template"})
	if workers, err := getValueAtPath(obj.UnstructuredContent(), clusterSpecPrefix+"workerGroupSpecs"); err == nil {
		if workers, ok := workers.([]interface{}); ok {
			for i := range workers {
				workerGroupSpecPrefix := fmt.Sprintf(clusterSpecPrefix+"workerGroupSpecs[%v].", i)
				// validate path to replica template
				if _, err := getValueAtPath(obj.UnstructuredContent(), workerGroupSpecPrefix+templateString); err == nil {
					// infer replica count
					replicas, err := inferReplicas(obj.UnstructuredContent(), workerGroupSpecPrefix+"replicas")
					if err != nil {
						return nil, err
					}
					podSets = append(podSets, awv1beta2.AppWrapperPodSet{Replicas: ptr.To(replicas), Path: workerGroupSpecPrefix + templateString})
				}
			}
		}
	}
	return podSets, nil
}

// InferPodSets infers PodSets for known GVKs
func InferPodSets(obj *unstructured.Unstructured) ([]awv1beta2.AppWrapperPodSet, error) {
	gvk := obj.GroupVersionKind()
	podSets := []awv1beta2.AppWrapperPodSet{}

	switch gvk {
	case schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}:
		var replicas int32 = 1
		if parallelism, err := GetReplicas(obj, "template.spec.parallelism"); err == nil {
			replicas = parallelism
		}
		if completions, err := GetReplicas(obj, "template.spec.completions"); err == nil && completions < replicas {
			replicas = completions
		}
		podSets = append(podSets, awv1beta2.AppWrapperPodSet{
			Replicas: ptr.To(replicas),
			Path:     "template.spec.template",
			Annotations: map[string]string{
				PodSetAnnotationTASPodIndexLabel: batchv1.JobCompletionIndexAnnotation,
			},
		})

	case schema.GroupVersionKind{Group: "jobset.x-k8s.io", Version: "v1alpha2", Kind: "JobSet"}:
		if jobs, err := getValueAtPath(obj.UnstructuredContent(), "template.spec.replicatedJobs"); err == nil {
			if jobs, ok := jobs.([]interface{}); ok {
				for i := range jobs {
					jobSpecPrefix := fmt.Sprintf("template.spec.replicatedJobs[%v].", i)
					// validate path to replica template
					if _, err := getValueAtPath(obj.UnstructuredContent(), jobSpecPrefix+"template"); err == nil {
						var podCount int32 = 1
						if parallelism, err := GetReplicas(obj, jobSpecPrefix+"template.spec.parallelism"); err == nil {
							podCount = parallelism
						}
						if completions, err := GetReplicas(obj, jobSpecPrefix+"template.spec.completions"); err == nil && completions < podCount {
							podCount = completions
						}
						var replicas int32 = 1
						if r, err := GetReplicas(obj, jobSpecPrefix+"replicas"); err == nil {
							replicas = r
						}
						podSets = append(podSets, awv1beta2.AppWrapperPodSet{
							Replicas: ptr.To(replicas * podCount),
							Path:     jobSpecPrefix + "template.spec.template",
							Annotations: map[string]string{
								PodSetAnnotationTASPodIndexLabel:      batchv1.JobCompletionIndexAnnotation,
								PodSetAnnotationTASSubGroupIndexLabel: jobsetapi.JobIndexKey,
								PodSetAnnotationTASSubGroupCount:      strconv.Itoa(int(replicas)),
							},
						})
					}
				}
			}
		}

	case schema.GroupVersionKind{Group: "kubeflow.org", Version: "v1", Kind: "PyTorchJob"}:
		for _, replicaType := range []string{"Master", "Worker"} {
			prefix := "template.spec.pytorchReplicaSpecs." + replicaType + "."
			// validate path to replica template
			if _, err := getValueAtPath(obj.UnstructuredContent(), prefix+templateString); err == nil {
				// infer replica count
				replicas, err := inferReplicas(obj.UnstructuredContent(), prefix+"replicas")
				if err != nil {
					return nil, err
				}
				podSets = append(podSets, awv1beta2.AppWrapperPodSet{
					Replicas: ptr.To(replicas),
					Path:     prefix + templateString,
					Annotations: map[string]string{
						PodSetAnnotationTASPodIndexLabel: kftraining.ReplicaIndexLabel,
					},
				})
			}
		}

	case schema.GroupVersionKind{Group: "ray.io", Version: "v1", Kind: "RayCluster"}:
		rayPodSets, err := inferRayPodSets(obj, "template.spec.")
		if err != nil {
			return nil, err
		}
		podSets = append(podSets, rayPodSets...)

	case schema.GroupVersionKind{Group: "ray.io", Version: "v1", Kind: "RayJob"}:
		rayPodSets, err := inferRayPodSets(obj, "template.spec.rayClusterSpec.")
		if err != nil {
			return nil, err
		}
		podSets = append(podSets, rayPodSets...)

	default:
		for _, template := range templatesForGVK[gvk] {
			// validate path to template
			if _, err := getValueAtPath(obj.UnstructuredContent(), template.path); err == nil {
				replicas, err := inferReplicas(obj.UnstructuredContent(), template.replicas)
				// infer replica count
				if err != nil {
					return nil, err
				}
				podSets = append(podSets, awv1beta2.AppWrapperPodSet{Replicas: ptr.To(replicas), Path: template.path})
			}
		}
	}

	for _, ps := range podSets {
		if _, err := GetPodTemplateSpec(obj, ps.Path); err != nil {
			return nil, fmt.Errorf("%v does not refer to a v1.PodSpecTemplate: %v", ps.Path, err)
		}
	}

	return podSets, nil
}

// ValidatePodSets validates the declared and inferred PodSets
func ValidatePodSets(declared []awv1beta2.AppWrapperPodSet, inferred []awv1beta2.AppWrapperPodSet) error {
	if len(declared) == 0 {
		return nil
	}

	// Validate that there are no duplicate paths in declared
	declaredPaths := map[string]awv1beta2.AppWrapperPodSet{}
	for _, p := range declared {
		if _, ok := declaredPaths[p.Path]; ok {
			return fmt.Errorf("multiple DeclaredPodSets with path '%v'", p.Path)
		}
		declaredPaths[p.Path] = p
	}

	// Validate that the declared PodSets match what inference computed
	if len(inferred) > 0 {
		if len(inferred) != len(declared) {
			return fmt.Errorf("DeclaredPodSet count %v differs from inferred count %v", len(declared), len(inferred))
		}

		// match inferred PodSets to declared PodSets
		for _, ips := range inferred {
			dps, ok := declaredPaths[ips.Path]
			if !ok {
				return fmt.Errorf("PodSet with path '%v' is missing", ips.Path)
			}

			ipr := ptr.Deref(ips.Replicas, 1)
			dpr := ptr.Deref(dps.Replicas, 1)
			if ipr != dpr {
				return fmt.Errorf("replica count %v differs from inferred count %v for PodSet at path position '%v'", dpr, ipr, ips.Path)
			}
		}
	}

	return nil
}

var labelRegex = regexp.MustCompile(`[^-_.\w]`)

// SanitizeLabel sanitizes a string for use as a label
func SanitizeLabel(label string) string {
	// truncate to max length
	if len(label) > 63 {
		label = label[0:63]
	}
	// replace invalid characters with underscores
	label = labelRegex.ReplaceAllString(label, "_")
	// trim non-alphanumeric characters at both ends
	label = strings.Trim(label, "-_.")
	return label
}
