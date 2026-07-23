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

package equality

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

type ComparePodSetsOptions struct {
	ignoreTolerations     bool
	ignoreTopologyRequest bool
}

type ComparePodSetsOption func(*ComparePodSetsOptions)

func WithIgnoreTolerations() ComparePodSetsOption {
	return func(options *ComparePodSetsOptions) {
		options.ignoreTolerations = true
	}
}

func WithIgnoreTopologyRequest() ComparePodSetsOption {
	return func(options *ComparePodSetsOptions) {
		options.ignoreTopologyRequest = true
	}
}

// TODO: Revisit this, maybe we should extend the check to everything that could potentially impact
// the workload scheduling (priority, nodeSelectors(when suspended), tolerations and maybe more)
func ComparePodTemplate(a, b *corev1.PodSpec, options ...ComparePodSetsOption) bool {
	opts := &ComparePodSetsOptions{}
	for _, opt := range options {
		opt(opts)
	}
	if !opts.ignoreTolerations && !equality.Semantic.DeepEqual(a.Tolerations, b.Tolerations) {
		return false
	}
	if !equality.Semantic.DeepEqual(a.InitContainers, b.InitContainers) {
		return false
	}
	if !equality.Semantic.DeepEqual(a.Containers, b.Containers) {
		return false
	}
	if !equality.Semantic.DeepEqual(a.Resources, b.Resources) {
		return false
	}
	return compareResourceClaims(a.ResourceClaims, b.ResourceClaims)
}

// compareResourceClaims compares PodSpec.ResourceClaims order-independently, keyed
// by Name, since the field is a +listType=map (+listMapKey=name) and its order is
// not semantically significant.
func compareResourceClaims(a, b []corev1.PodResourceClaim) bool {
	if len(a) != len(b) {
		return false
	}
	bByName := utilslices.ToMap(b, func(i int) (string, corev1.PodResourceClaim) {
		return b[i].Name, b[i]
	})
	for i := range a {
		bClaim, found := bByName[a[i].Name]
		if !found || !equality.Semantic.DeepEqual(a[i], bClaim) {
			return false
		}
	}
	return true
}

func normalizedTopologyRequest(r *kueue.PodSetTopologyRequest) *kueue.PodSetTopologyRequest {
	if r == nil {
		return nil
	}
	result := r.DeepCopy()
	if constraints := utiltas.PodSetSliceRequiredTopologyConstraints(result); len(constraints) > 0 {
		result.PodsetSliceRequiredTopologyConstraints = constraints
		result.PodSetSliceRequiredTopology = nil
		result.PodSetSliceSize = nil
	}
	return result
}

func ComparePodSets(a, b *kueue.PodSet, options ...ComparePodSetsOption) bool {
	opts := &ComparePodSetsOptions{}
	for _, opt := range options {
		opt(opts)
	}
	if a.Count != b.Count {
		return false
	}
	if ptr.Deref(a.MinCount, -1) != ptr.Deref(b.MinCount, -1) {
		return false
	}
	if !opts.ignoreTopologyRequest &&
		(utiltas.HasTopologyConstraint(a.TopologyRequest) || utiltas.HasTopologyConstraint(b.TopologyRequest)) &&
		!equality.Semantic.DeepEqual(normalizedTopologyRequest(a.TopologyRequest), normalizedTopologyRequest(b.TopologyRequest)) {
		return false
	}

	return ComparePodTemplate(&a.Template.Spec, &b.Template.Spec, options...)
}

func ComparePodSetSlices(a, b []kueue.PodSet, options ...ComparePodSetsOption) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !ComparePodSets(&a[i], &b[i], options...) {
			return false
		}
	}
	return true
}
