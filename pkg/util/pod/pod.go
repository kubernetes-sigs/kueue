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

var (
	ErrLabelNotFound = errors.New("label not found")
	ErrInvalidUInt   = errors.New("invalid unsigned integer")
	ErrValidation    = errors.New("validation error")
)

func IgnoreLabelNotFoundError(err error) error {
	if errors.Is(err, ErrLabelNotFound) {
		return nil
	}
	return err
}

func ReadUIntFromLabel(obj client.Object, labelKey string) (*int, error) {
	return ReadUIntFromLabelBelowBound(obj, labelKey, math.MaxInt)
}

func ReadUIntFromLabelBelowBound(obj client.Object, labelKey string, bound int) (*int, error) {
	value, found := obj.GetLabels()[labelKey]
	kind := obj.GetObjectKind().GroupVersionKind().Kind
	if !found {
		return nil, fmt.Errorf("%w: no label %q for %s %q", ErrLabelNotFound, labelKey, kind, klog.KObj(obj))
	}
	intValue, err := readUIntFromStringBelowBound(value, bound)
	if err != nil {
		return nil, fmt.Errorf("incorrect label value %q for %s %q: %w", value, kind, klog.KObj(obj), err)
	}
	return intValue, nil
}

func readUIntFromStringBelowBound(value string, bound int) (*int, error) {
	uintValue, err := strconv.ParseUint(value, 10, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidUInt, err.Error())
	}
	if uintValue >= uint64(bound) {
		return nil, fmt.Errorf("%w: value should be less than %d", ErrValidation, bound)
	}
	return ptr.To(int(uintValue)), nil
}
