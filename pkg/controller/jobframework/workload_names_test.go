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
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/pkg/features"
)

func TestGenerateWorkloadName(t *testing.T) {
	gvk := batchv1.SchemeGroupVersion.WithKind("Job")

	testCases := map[string]struct {
		featureGates map[featuregate.Feature]bool
		ownerName    string
		ownerUID     types.UID
		ownerGVK     schema.GroupVersionKind
		generation   *int64
		want         string
	}{
		"should generate workload name (ShortWorkloadNames=false)": {
			featureGates: map[featuregate.Feature]bool{
				features.ShortWorkloadNames: false,
			},
			ownerName:  "name",
			ownerUID:   "uid",
			ownerGVK:   gvk,
			generation: nil,
			want:       "job-name-ef3a1",
		},
		"should generate workload name (ShortWorkloadNames=false, with generation)": {
			featureGates: map[featuregate.Feature]bool{
				features.ShortWorkloadNames: false,
			},
			ownerName:  "name",
			ownerUID:   "uid",
			ownerGVK:   gvk,
			generation: ptr.To[int64](1),
			want:       "job-name-2514c",
		},
		"should generate workload name (ShortWorkloadNames=false, 260 symbols owner name)": {
			featureGates: map[featuregate.Feature]bool{
				features.ShortWorkloadNames: false,
			},
			ownerName:  "marketing-campaign-autumn-2024-region-north-america-department-digital-strategy-sub-department-social-media-engagement-tracker-and-analytics-dashboard-v2-final-revision-by-engineering-team-lead-senior-architect-validation-checked-approved-by-legal-and-ops-dept",
			ownerUID:   "uid",
			ownerGVK:   gvk,
			generation: nil,
			want:       "job-marketing-campaign-autumn-2024-region-north-america-department-digital-strategy-sub-department-social-media-engagement-tracker-and-analytics-dashboard-v2-final-revision-by-engineering-team-lead-senior-architect-validation-checked-approved-by-l-218b8",
		},
		"should generate workload name (ShortWorkloadNames=true, 260 symbols owner name)": {
			featureGates: map[featuregate.Feature]bool{
				features.ShortWorkloadNames: true,
			},
			ownerName:  "marketing-campaign-autumn-2024-region-north-america-department-digital-strategy-sub-department-social-media-engagement-tracker-and-analytics-dashboard-v2-final-revision-by-engineering-team-lead-senior-architect-validation-checked-approved-by-legal-and-ops-dept",
			ownerUID:   "uid",
			ownerGVK:   gvk,
			generation: nil,
			want:       "job-marketing-campaign-autumn-2024-region-north-america-d-218b8",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			for fg, enabled := range tc.featureGates {
				features.SetFeatureGateDuringTest(t, fg, enabled)
			}
			got := generateWorkloadName(tc.ownerName, tc.ownerUID, tc.ownerGVK, tc.generation)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateErrors()); len(diff) != 0 {
				t.Fatalf("Unexpected workloadName (-want,+got):\n%s", diff)
			}
		})
	}
}
