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
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	jobsetapi "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

func TestPodSetTopologyRequestBuilder(t *testing.T) {
	testCases := map[string]struct {
		meta               *metav1.ObjectMeta
		podIndexLabel      *string
		subGroupIndexLabel *string
		subGroupCount      *int32
		wantReq            *kueue.PodSetTopologyRequest
		wantErr            error
	}{
		"required annotation": {
			meta: &metav1.ObjectMeta{
				Annotations: map[string]string{
					kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
				},
			},
			wantReq: &kueue.PodSetTopologyRequest{
				Required: ptr.To("cloud.com/block"),
			},
		},
		"required annotation with pod index label": {
			meta: &metav1.ObjectMeta{
				Annotations: map[string]string{
					kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
				},
			},
			podIndexLabel: ptr.To(batchv1.JobCompletionIndexAnnotation),
			wantReq: &kueue.PodSetTopologyRequest{
				Required:      ptr.To("cloud.com/block"),
				PodIndexLabel: ptr.To(batchv1.JobCompletionIndexAnnotation),
			},
		},
		"pod index label only": {
			meta: &metav1.ObjectMeta{
				Annotations: map[string]string{},
			},
			podIndexLabel: ptr.To(batchv1.JobCompletionIndexAnnotation),
			wantReq: &kueue.PodSetTopologyRequest{
				PodIndexLabel: ptr.To(batchv1.JobCompletionIndexAnnotation),
			},
		},
		"required annotation with sub group": {
			meta: &metav1.ObjectMeta{
				Annotations: map[string]string{
					kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
					kueue.PodSetGroupName:                  "block",
				},
			},
			subGroupIndexLabel: ptr.To(jobsetapi.JobIndexKey),
			subGroupCount:      ptr.To[int32](1),
			wantReq: &kueue.PodSetTopologyRequest{
				Required:           ptr.To("cloud.com/block"),
				SubGroupIndexLabel: ptr.To(jobsetapi.JobIndexKey),
				SubGroupCount:      ptr.To[int32](1),
				PodSetGroupName:    ptr.To("block"),
			},
		},
		"required annotation with sub group and pod set group name annotation": {
			meta: &metav1.ObjectMeta{
				Annotations: map[string]string{
					kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
					kueue.PodSetGroupName:                  "block",
				},
			},
			subGroupIndexLabel: ptr.To(jobsetapi.JobIndexKey),
			subGroupCount:      ptr.To[int32](1),
			wantReq: &kueue.PodSetTopologyRequest{
				Required:           ptr.To("cloud.com/block"),
				SubGroupIndexLabel: ptr.To(jobsetapi.JobIndexKey),
				SubGroupCount:      ptr.To[int32](1),
				PodSetGroupName:    ptr.To("block"),
			},
		},
		"preferred annotation": {
			meta: &metav1.ObjectMeta{
				Annotations: map[string]string{
					kueue.PodSetPreferredTopologyAnnotation: "cloud.com/block",
				},
			},
			wantReq: &kueue.PodSetTopologyRequest{
				Preferred: ptr.To("cloud.com/block"),
			},
		},
		"unconstrained annotation (true)": {
			meta: &metav1.ObjectMeta{
				Annotations: map[string]string{
					kueue.PodSetUnconstrainedTopologyAnnotation: "true",
				},
			},
			wantReq: &kueue.PodSetTopologyRequest{
				Unconstrained: ptr.To(true),
			},
		},
		"unconstrained annotation (false)": {
			meta: &metav1.ObjectMeta{
				Annotations: map[string]string{
					kueue.PodSetUnconstrainedTopologyAnnotation: "false",
				},
			},
			wantReq: &kueue.PodSetTopologyRequest{
				Unconstrained: ptr.To(false),
			},
		},
		"slice-only topology": {
			meta: &metav1.ObjectMeta{
				Annotations: map[string]string{
					kueue.PodSetSliceRequiredTopologyAnnotation: "cloud.com/block",
					kueue.PodSetSliceSizeAnnotation:             "1",
				},
			},
			wantReq: &kueue.PodSetTopologyRequest{
				PodSetSliceRequiredTopology: ptr.To("cloud.com/block"),
				PodSetSliceSize:             ptr.To[int32](1),
			},
		},
		"slice-only topology – only slice required annotation": {
			meta: &metav1.ObjectMeta{
				Annotations: map[string]string{
					kueue.PodSetSliceRequiredTopologyAnnotation: "cloud.com/block",
				},
			},
		},
		"slice-only topology – only slice size annotation": {
			meta: &metav1.ObjectMeta{
				Annotations: map[string]string{
					kueue.PodSetSliceSizeAnnotation: "1",
				},
			},
		},
		"slice-only topology with sub group and pod set group name annotation": {
			meta: &metav1.ObjectMeta{
				Annotations: map[string]string{
					kueue.PodSetSliceRequiredTopologyAnnotation: "cloud.com/block",
					kueue.PodSetSliceSizeAnnotation:             "1",
					kueue.PodSetGroupName:                       "block",
				},
			},
			subGroupIndexLabel: ptr.To(jobsetapi.JobIndexKey),
			subGroupCount:      ptr.To[int32](1),
			wantReq: &kueue.PodSetTopologyRequest{
				PodSetSliceRequiredTopology: ptr.To("cloud.com/block"),
				PodSetSliceSize:             ptr.To[int32](1),
				SubGroupIndexLabel:          ptr.To(jobsetapi.JobIndexKey),
				SubGroupCount:               ptr.To[int32](1),
			},
		},
		"invalid unconstrained topology annotation value": {
			meta: &metav1.ObjectMeta{
				Annotations: map[string]string{
					kueue.PodSetUnconstrainedTopologyAnnotation: "invalid",
				},
			},
			wantErr: strconv.ErrSyntax,
		},
		"invalid podset slice size annotation value": {
			meta: &metav1.ObjectMeta{
				Annotations: map[string]string{
					kueue.PodSetSliceRequiredTopologyAnnotation: "cloud.com/block",
					kueue.PodSetSliceSizeAnnotation:             "invalid",
				},
			},
			wantErr: strconv.ErrSyntax,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			b := NewPodSetTopologyRequest(tc.meta)
			b.PodIndexLabel(tc.podIndexLabel)
			b.SubGroup(tc.subGroupIndexLabel, tc.subGroupCount)

			gotReq, gotErr := b.Build()

			if diff := cmp.Diff(tc.wantReq, gotReq); len(diff) != 0 {
				t.Errorf("Unexpected request (-want,+got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); len(diff) != 0 {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
		})
	}
}
