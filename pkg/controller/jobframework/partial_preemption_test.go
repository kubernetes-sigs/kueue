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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/kueue/pkg/constants"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestPropagatePartialPreemptionAnnotation(t *testing.T) {
	const (
		optIn        = constants.PartialPreemptionAnnotation
		maxReclaimed = constants.PartialPreemptionMaxReclaimedCountAnnotation
	)
	cases := map[string]struct {
		job         map[string]string
		workload    map[string]string
		want        map[string]string
		wantChanged bool
	}{
		"nothing to copy": {},
		"opt-in is copied": {
			job:         map[string]string{optIn: "true"},
			want:        map[string]string{optIn: "true"},
			wantChanged: true,
		},
		"opt-in that is not true is not copied": {
			job: map[string]string{optIn: "false"},
		},
		"max reclaimed count is copied": {
			job:         map[string]string{optIn: "true", maxReclaimed: "2"},
			want:        map[string]string{optIn: "true", maxReclaimed: "2"},
			wantChanged: true,
		},
		"max reclaimed count that changed is copied": {
			job:         map[string]string{optIn: "true", maxReclaimed: "5"},
			workload:    map[string]string{optIn: "true", maxReclaimed: "2"},
			want:        map[string]string{optIn: "true", maxReclaimed: "5"},
			wantChanged: true,
		},
		"max reclaimed count that the job dropped is removed": {
			job:         map[string]string{optIn: "true"},
			workload:    map[string]string{optIn: "true", maxReclaimed: "2"},
			want:        map[string]string{optIn: "true"},
			wantChanged: true,
		},
		"max reclaimed count is copied when only it changed": {
			job:         map[string]string{optIn: "true", maxReclaimed: "2"},
			workload:    map[string]string{optIn: "true"},
			want:        map[string]string{optIn: "true", maxReclaimed: "2"},
			wantChanged: true,
		},
		"both annotations change in one call": {
			job:         map[string]string{maxReclaimed: "2"},
			workload:    map[string]string{optIn: "true"},
			want:        map[string]string{maxReclaimed: "2"},
			wantChanged: true,
		},
		"up to date": {
			job:      map[string]string{optIn: "true", maxReclaimed: "2"},
			workload: map[string]string{optIn: "true", maxReclaimed: "2"},
			want:     map[string]string{optIn: "true", maxReclaimed: "2"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Annotations: tc.job}}
			wl := utiltestingapi.MakeWorkload("wl", "ns").Obj()
			wl.Annotations = tc.workload

			changed := PropagatePartialPreemptionAnnotation(job, wl)

			if changed != tc.wantChanged {
				t.Errorf("PropagatePartialPreemptionAnnotation() = %v, want %v", changed, tc.wantChanged)
			}
			if diff := cmp.Diff(tc.want, wl.Annotations, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected annotations (-want,+got):\n%s", diff)
			}
		})
	}
}
