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
	podIndexLabel      *string
	subGroupIndexLabel *string
	subGroupCount      *int32
	meta               *metav1.ObjectMeta
}

func (p *podSetTopologyRequestBuilder) PodIndexLabel(podIndexLabel *string) *podSetTopologyRequestBuilder {
	p.podIndexLabel = podIndexLabel
	return p
}

func (p *podSetTopologyRequestBuilder) SubGroup(subGroupIndexLabel *string, subGroupCount *int32) *podSetTopologyRequestBuilder {
	p.subGroupIndexLabel = subGroupIndexLabel
	p.subGroupCount = subGroupCount
	return p
}

func NewPodSetTopologyRequest(meta *metav1.ObjectMeta) *podSetTopologyRequestBuilder {
	return &podSetTopologyRequestBuilder{
		meta: meta,
	}
}

func (p *podSetTopologyRequestBuilder) Build() (*kueue.PodSetTopologyRequest, error) {
	psTopologyReq := kueue.PodSetTopologyRequest{}
	requiredValue, requiredFound := p.meta.Annotations[kueuealpha.PodSetRequiredTopologyAnnotation]
	preferredValue, preferredFound := p.meta.Annotations[kueuealpha.PodSetPreferredTopologyAnnotation]
	unconstrained, unconstrainedFound := p.meta.Annotations[kueuealpha.PodSetUnconstrainedTopologyAnnotation]

	sliceRequiredTopologyValue, sliceRequiredTopologyFound := p.meta.Annotations[kueuealpha.PodSetSliceRequiredTopologyAnnotation]
	sliceSizeValue, sliceSizeFound := p.meta.Annotations[kueuealpha.PodSetSliceSizeAnnotation]

	podSetGroupName, podSetGroupNameFound := p.meta.Annotations[kueuealpha.PodSetGroupName]

	switch {
	case requiredFound:
		psTopologyReq.Required = &requiredValue
	case preferredFound:
		psTopologyReq.Preferred = &preferredValue
	case unconstrainedFound:
		unconstrained, err := strconv.ParseBool(unconstrained)
		if err != nil {
			return nil, err
		}
		psTopologyReq.Unconstrained = &unconstrained
	default:
		if !sliceRequiredTopologyFound || !sliceSizeFound {
			return nil, nil
		}
	}

	if sliceRequiredTopologyFound && sliceSizeFound {
		sliceSizeIntValue, err := strconv.ParseInt(sliceSizeValue, 10, 32)
		if err != nil {
			return nil, err
		} else {
			psTopologyReq.PodSetSliceRequiredTopology = &sliceRequiredTopologyValue
			psTopologyReq.PodSetSliceSize = ptr.To(int32(sliceSizeIntValue))
		}
	}

	psTopologyReq.PodIndexLabel = p.podIndexLabel
	psTopologyReq.SubGroupCount = p.subGroupCount
	psTopologyReq.SubGroupIndexLabel = p.subGroupIndexLabel
	if podSetGroupNameFound && (requiredFound || preferredFound) {
		psTopologyReq.PodSetGroupName = &podSetGroupName
	}

	return &psTopologyReq, nil
}
