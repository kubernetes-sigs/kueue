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

package jobframework

import (
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

type podSetTopologyRequestBuilder struct {
	request *kueue.PodSetTopologyRequest
}

func (p *podSetTopologyRequestBuilder) Build() *kueue.PodSetTopologyRequest {
	return p.request
}

func (p *podSetTopologyRequestBuilder) PodIndexLabel(podIndexLabel *string) *podSetTopologyRequestBuilder {
	if p.request != nil {
		p.request.PodIndexLabel = podIndexLabel
	}
	return p
}

func (p *podSetTopologyRequestBuilder) SubGroup(subGroupIndexLabel *string, subGroupCount *int32) *podSetTopologyRequestBuilder {
	if p.request != nil {
		p.request.SubGroupIndexLabel = subGroupIndexLabel
		p.request.SubGroupCount = subGroupCount
	}
	return p
}

func NewPodSetTopologyRequest(meta *metav1.ObjectMeta) *podSetTopologyRequestBuilder {
	psTopologyReq := &kueue.PodSetTopologyRequest{}
	requiredValue, requiredFound := meta.Annotations[kueuealpha.PodSetRequiredTopologyAnnotation]
	preferredValue, preferredFound := meta.Annotations[kueuealpha.PodSetPreferredTopologyAnnotation]
	unconstrained, unconstrainedFound := meta.Annotations[kueuealpha.PodSetUnconstrainedTopologyAnnotation]

	sliceRequiredTopologyValue, sliceRequiredTopologyFound := meta.Annotations[kueuealpha.PodSetSliceRequiredTopologyAnnotation]
	sliceSizeValue, sliceSizeFound := meta.Annotations[kueuealpha.PodSetSliceSizeAnnotation]

	switch {
	case requiredFound:
		psTopologyReq.Required = &requiredValue
	case preferredFound:
		psTopologyReq.Preferred = &preferredValue
	case unconstrainedFound:
		unconstrained, _ := strconv.ParseBool(unconstrained)
		psTopologyReq.Unconstrained = &unconstrained
	default:
		psTopologyReq = nil
	}

	if sliceRequiredTopologyFound && sliceSizeFound {
		psTopologyReq.PodSetSliceRequiredTopology = &sliceRequiredTopologyValue
		sliceSizeIntValue, _ := strconv.ParseInt(sliceSizeValue, 10, 32)
		// TODO Error handling. For simplicity of reviewing a PR, it will be implemented in a follow-up
		psTopologyReq.PodSetSliceSize = ptr.To(int32(sliceSizeIntValue))
	}

	builder := &podSetTopologyRequestBuilder{request: psTopologyReq}
	return builder
}
