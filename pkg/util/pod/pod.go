/*
CCopyright The Kubernetes Authors.

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

package pod

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// HasGate checks if the pod has a scheduling gate with a specified name.
func HasGate(pod *corev1.Pod, gateName string) bool {
	return gateIndex(pod, gateName) >= 0
}

// Ungate removes scheduling gate from the Pod if present.
// Returns true if the pod has been updated and false otherwise.
func Ungate(pod *corev1.Pod, gateName string) bool {
	if idx := gateIndex(pod, gateName); idx >= 0 {
		pod.Spec.SchedulingGates = slices.Delete(pod.Spec.SchedulingGates, idx, idx+1)
		return true
	}
	return false
}

// Gate adds scheduling gate from the Pod if present.
// Returns true if the pod has been updated and false otherwise.
func Gate(pod *corev1.Pod, gateName string) bool {
	if !HasGate(pod, gateName) {
		pod.Spec.SchedulingGates = append(pod.Spec.SchedulingGates, corev1.PodSchedulingGate{
			Name: gateName,
		})
		return true
	}
	return false
}

// gateIndex returns the index of the Kueue scheduling gate for corev1.Pod.
// If the scheduling gate is not found, returns -1.
func gateIndex(p *corev1.Pod, gateName string) int {
	return slices.IndexFunc(p.Spec.SchedulingGates, func(g corev1.PodSchedulingGate) bool {
		return g.Name == gateName
	})
}

func ReadUIntFromLabel(obj client.Object, labelKey string) (*int, error) {
	return ReadUIntFromLabelWithMax(obj, labelKey, math.MaxInt)
}

func ReadUIntFromLabelWithMax(obj client.Object, labelKey string, max int) (*int, error) {
	value, found := obj.GetLabels()[labelKey]
	kind := obj.GetObjectKind().GroupVersionKind().Kind
	if !found {
		return nil, fmt.Errorf("no label %q for %s %q", labelKey, kind, klog.KObj(obj))
	}
	intValue, err := readUIntFromStringWithMax(value, max)
	if err != nil {
		return nil, fmt.Errorf("incorrect label value %q for %s %q: %w", value, kind, klog.KObj(obj), err)
	}
	return intValue, nil
}

var (
	errInvalidUInt = errors.New("invalid unsigned integer")
)

func readUIntFromStringWithMax(value string, max int) (*int, error) {
	uintValue, err := strconv.ParseUint(value, 10, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", errInvalidUInt, err.Error())
	}
	intValue := int(uintValue)
	if intValue > max {
		return nil, fmt.Errorf("value should be less than or equal to %d", max)
	}
	return ptr.To(intValue), nil
}
