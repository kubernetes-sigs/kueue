/*
Copyright 2022 The Kubernetes Authors.

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

package priority

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	schedulingv1 "k8s.io/api/scheduling/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/util/pointer"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestPriority(t *testing.T) {
	tests := map[string]struct {
		workload *kueue.Workload
		want     int32
	}{
		"priority is specified": {
			workload: utiltesting.MakeWorkload("name", "ns").Priority(pointer.Int32(100)).Obj(),
			want:     100,
		},
		"priority is empty": {
			workload: &kueue.Workload{
				Spec: kueue.WorkloadSpec{},
			},
			want: constants.DefaultPriority,
		},
	}

	for desc, tt := range tests {
		t.Run(desc, func(t *testing.T) {
			got := Priority(tt.workload)
			if got != tt.want {
				t.Errorf("Priority does not match: got: %d, expected: %d", got, tt.want)
			}
		})
	}
}

func TestGetPriorityFromPriorityClass(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := schedulingv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed adding scheduling scheme: %v", err)
	}

	tests := map[string]struct {
		priorityClassList      *schedulingv1.PriorityClassList
		priorityClassName      string
		wantPriorityClassName  string
		wantPriorityClassValue int32
		wantErr                string
	}{
		"priorityClass is specified and it exists": {
			priorityClassList: &schedulingv1.PriorityClassList{
				Items: []schedulingv1.PriorityClass{
					{
						ObjectMeta: v1.ObjectMeta{Name: "test"},
						Value:      50,
					},
				},
			},
			priorityClassName:      "test",
			wantPriorityClassName:  "test",
			wantPriorityClassValue: 50,
		},
		"priorityClass is specified and it does not exist": {
			priorityClassList: &schedulingv1.PriorityClassList{
				Items: []schedulingv1.PriorityClass{},
			},
			priorityClassName: "test",
			wantErr:           `priorityclasses.scheduling.k8s.io "test" not found`,
		},
		"priorityClass is unspecified and one global default exists": {
			priorityClassList: &schedulingv1.PriorityClassList{
				Items: []schedulingv1.PriorityClass{
					{
						ObjectMeta:    v1.ObjectMeta{Name: "globalDefault"},
						GlobalDefault: true,
						Value:         40,
					},
				},
			},
			wantPriorityClassName:  "globalDefault",
			wantPriorityClassValue: 40,
		},
		"priorityClass is unspecified and multiple global defaults exist": {
			priorityClassList: &schedulingv1.PriorityClassList{
				Items: []schedulingv1.PriorityClass{
					{
						ObjectMeta:    v1.ObjectMeta{Name: "globalDefault1"},
						GlobalDefault: true,
						Value:         90,
					},
					{
						ObjectMeta:    v1.ObjectMeta{Name: "globalDefault2"},
						GlobalDefault: true,
						Value:         20,
					},
					{
						ObjectMeta:    v1.ObjectMeta{Name: "globalDefault3"},
						GlobalDefault: true,
						Value:         50,
					},
				},
			},
			wantPriorityClassName:  "globalDefault2",
			wantPriorityClassValue: 20,
		},
	}

	for desc, tt := range tests {
		tt := tt
		t.Run(desc, func(t *testing.T) {
			t.Parallel()

			builder := fake.NewClientBuilder().WithScheme(scheme).WithLists(tt.priorityClassList)
			client := builder.Build()

			name, value, err := GetPriorityFromPriorityClass(context.Background(), client, tt.priorityClassName)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error")
				}

				if diff := cmp.Diff(tt.wantErr, err.Error()); diff != "" {
					t.Errorf("unexpected error (-want,+got):\n%s", diff)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if name != tt.wantPriorityClassName {
				t.Errorf("unexpected name: got: %s, expected: %s", name, tt.wantPriorityClassName)
			}

			if value != tt.wantPriorityClassValue {
				t.Errorf("unexpected value: got: %d, expected: %d", value, tt.wantPriorityClassValue)
			}
		})
	}
}
