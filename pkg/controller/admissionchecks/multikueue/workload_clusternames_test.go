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

package multikueue

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
)

func TestIntersectAuthorizedClusters(t *testing.T) {
	authorized := map[string]*kueue.Workload{"worker1": nil, "worker2": nil}
	cases := map[string]struct {
		requested []string
		want      []string
	}{
		"narrow to one authorized cluster": {
			requested: []string{"worker1"},
			want:      []string{"worker1"},
		},
		"drop unauthorized clusters (narrow only, never widen)": {
			requested: []string{"worker1", "worker3"},
			want:      []string{"worker1"},
		},
		"all requested unauthorized -> empty": {
			requested: []string{"worker3", "worker4"},
			want:      []string{},
		},
		"result is sorted and deduplicated": {
			requested: []string{"worker2", "worker1", "worker1"},
			want:      []string{"worker1", "worker2"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := intersectAuthorizedClusters(tc.requested, authorized)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("unexpected result (-want +got):\n%s", diff)
			}
		})
	}
}

func TestUserNominatedClusters(t *testing.T) {
	wlWith := func(ann map[string]string) *kueue.Workload {
		return &kueue.Workload{ObjectMeta: metav1.ObjectMeta{Annotations: ann}}
	}
	cases := map[string]struct {
		featureEnabled bool
		wl             *kueue.Workload
		want           []string
		wantOK         bool
	}{
		"feature disabled -> not requested": {
			featureEnabled: false,
			wl:             wlWith(map[string]string{kueue.MultiKueueClusterNamesAnnotation: "worker1"}),
			wantOK:         false,
		},
		"no annotation -> not requested": {
			featureEnabled: true,
			wl:             wlWith(nil),
			wantOK:         false,
		},
		"empty annotation -> not requested": {
			featureEnabled: true,
			wl:             wlWith(map[string]string{kueue.MultiKueueClusterNamesAnnotation: ""}),
			wantOK:         false,
		},
		"single cluster": {
			featureEnabled: true,
			wl:             wlWith(map[string]string{kueue.MultiKueueClusterNamesAnnotation: "worker1"}),
			want:           []string{"worker1"},
			wantOK:         true,
		},
		"multiple clusters": {
			featureEnabled: true,
			wl:             wlWith(map[string]string{kueue.MultiKueueClusterNamesAnnotation: "worker1,worker2,worker3"}),
			want:           []string{"worker1", "worker2", "worker3"},
			wantOK:         true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.MultiKueueClusterNames, tc.featureEnabled)
			got, ok := userNominatedClusters(tc.wl)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("unexpected clusters (-want +got):\n%s", diff)
			}
		})
	}
}
