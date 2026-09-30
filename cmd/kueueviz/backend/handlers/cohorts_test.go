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

package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

func makeCohort(name, parent string) *kueueapi.Cohort {
	return &kueueapi.Cohort{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: kueueapi.CohortSpec{
			ParentName: kueueapi.CohortReference(parent),
		},
	}
}

func TestFetchCohorts(t *testing.T) {
	objs := []ctrlclient.Object{
		makeCohort("root", ""),
		makeCohort("team", "root"),
		makeClusterQueue("cq-a", "team"),
		makeClusterQueue("cq-b", "team"),
		makeClusterQueue("cq-no-cohort", ""),
	}

	tests := map[string]struct {
		objs  []ctrlclient.Object
		funcs *interceptor.Funcs
		want  []map[string]any
		// wantErr is set when listing cohorts fails.
		wantErr bool
	}{
		"no cohorts returns nil": {},
		"cluster queues are grouped under their cohort": {
			objs: objs,
			want: []map[string]any{
				{
					"name":          "root",
					"clusterQueues": []map[string]any{},
				},
				{
					"name":          "team",
					"parentName":    "root",
					"clusterQueues": []map[string]any{{"name": "cq-a"}, {"name": "cq-b"}},
				},
			},
		},
		"cluster queue list error is returned": {
			objs: objs,
			funcs: &interceptor.Funcs{
				List: func(ctx context.Context, c ctrlclient.WithWatch, list ctrlclient.ObjectList, opts ...ctrlclient.ListOption) error {
					if _, ok := list.(*kueueapi.ClusterQueueList); ok {
						return errors.New("boom")
					}
					return c.List(ctx, list, opts...)
				},
			},
			wantErr: true,
		},
		"cohort list error is returned": {
			objs:    objs,
			funcs:   listErrorFuncs(errors.New("boom")),
			wantErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h := New(newFakeClient(t, tc.funcs, tc.objs...), nil)

			got, err := h.fetchCohorts(t.Context())
			if (err != nil) != tc.wantErr {
				t.Fatalf("fetchCohorts() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if diff := cmp.Diff(tc.want, got.([]map[string]any)); diff != "" {
				t.Errorf("unexpected cohorts (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestFetchCohortDetails(t *testing.T) {
	objs := []ctrlclient.Object{
		makeCohort("team", ""),
		makeClusterQueue("cq-a", "team"),
		makeClusterQueue("cq-b", "other"),
	}

	tests := map[string]struct {
		objs       []ctrlclient.Object
		funcs      *interceptor.Funcs
		cohortName string
		wantQueues []string
		wantErr    bool
	}{
		"lists cluster queues of the cohort": {
			objs:       objs,
			cohortName: "team",
			wantQueues: []string{"cq-a"},
		},
		"missing cohort returns error": {
			cohortName: "missing",
			wantErr:    true,
		},
		"cluster queue list error is returned": {
			objs:       objs,
			funcs:      listErrorFuncs(errors.New("boom")),
			cohortName: "team",
			wantErr:    true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h := New(newFakeClient(t, tc.funcs, tc.objs...), nil)

			got, err := h.fetchCohortDetails(t.Context(), tc.cohortName)
			if (err != nil) != tc.wantErr {
				t.Fatalf("fetchCohortDetails() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}

			if got["cohort"] != tc.cohortName {
				t.Errorf("cohort = %v, want %q", got["cohort"], tc.cohortName)
			}
			gotQueues := []string{}
			for _, cq := range got["clusterQueues"].([]map[string]any) {
				gotQueues = append(gotQueues, cq["name"].(string))
			}
			if diff := cmp.Diff(tc.wantQueues, gotQueues); diff != "" {
				t.Errorf("unexpected cluster queues (-want,+got):\n%s", diff)
			}
		})
	}
}
