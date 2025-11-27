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

package priority

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	schedulingv1 "k8s.io/api/scheduling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestPriority(t *testing.T) {
	tests := map[string]struct {
		workload *kueue.Workload
		want     int32
	}{
		"priority is specified": {
			workload: utiltestingapi.MakeWorkload("name", "ns").Priority(100).Obj(),
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
		wantPriorityClassRef   *kueue.PriorityClassRef
		wantPriorityClassValue int32
		wantErr                error
	}{
		"priorityClass is specified and it exists": {
			priorityClassList: &schedulingv1.PriorityClassList{
				Items: []schedulingv1.PriorityClass{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "test"},
						Value:      50,
					},
				},
			},

			priorityClassName:      "test",
			wantPriorityClassRef:   kueue.NewPodPriorityClassRef("test"),
			wantPriorityClassValue: 50,
		},
		"priorityClass is specified and it does not exist": {
			priorityClassList: &schedulingv1.PriorityClassList{
				Items: []schedulingv1.PriorityClass{},
			},
			priorityClassName: "test",
			wantErr:           apierrors.NewNotFound(schedulingv1.Resource("priorityclasses"), "test"),
		},
		"priorityClass is unspecified and one global default exists": {
			priorityClassList: &schedulingv1.PriorityClassList{
				Items: []schedulingv1.PriorityClass{
					{
						ObjectMeta:    metav1.ObjectMeta{Name: "globalDefault"},
						GlobalDefault: true,
						Value:         40,
					},
				},
			},
			wantPriorityClassRef:   kueue.NewPodPriorityClassRef("globalDefault"),
			wantPriorityClassValue: 40,
		},
		"priorityClass is unspecified and multiple global defaults exist": {
			priorityClassList: &schedulingv1.PriorityClassList{
				Items: []schedulingv1.PriorityClass{
					{
						ObjectMeta:    metav1.ObjectMeta{Name: "globalDefault1"},
						GlobalDefault: true,
						Value:         90,
					},
					{
						ObjectMeta:    metav1.ObjectMeta{Name: "globalDefault2"},
						GlobalDefault: true,
						Value:         20,
					},
					{
						ObjectMeta:    metav1.ObjectMeta{Name: "globalDefault3"},
						GlobalDefault: true,
						Value:         50,
					},
				},
			},
			wantPriorityClassRef:   kueue.NewPodPriorityClassRef("globalDefault2"),
			wantPriorityClassValue: 20,
		},
	}

	for desc, tt := range tests {
		t.Run(desc, func(t *testing.T) {
			t.Parallel()

			builder := fake.NewClientBuilder().WithScheme(scheme).WithLists(tt.priorityClassList)
			client := builder.Build()

			ctx, _ := utiltesting.ContextWithLog(t)
			priorityClassRef, value, err := GetPriorityFromPriorityClass(ctx, client, tt.priorityClassName)
			if diff := cmp.Diff(tt.wantErr, err); diff != "" {
				t.Errorf("unexpected error (-want,+got):\n%s", diff)
			}

			if diff := cmp.Diff(tt.wantPriorityClassRef, priorityClassRef); diff != "" {
				t.Errorf("unexpected priortyClassRef (-want,+got):\n%s", diff)
			}

			if value != tt.wantPriorityClassValue {
				t.Errorf("unexpected value: got: %d, expected: %d", value, tt.wantPriorityClassValue)
			}
		})
	}
}

func TestGetPriorityFromWorkloadPriorityClass(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kueue.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed adding kueue scheme: %v", err)
	}

	tests := map[string]struct {
		workloadPriorityClassList *kueue.WorkloadPriorityClassList
		workloadPriorityClassName string
		wantPriorityClassRef      *kueue.PriorityClassRef
		wantPriorityClassValue    int32
		wantErr                   error
	}{
		"workloadPriorityClass is specified and it exists": {
			workloadPriorityClassList: &kueue.WorkloadPriorityClassList{
				Items: []kueue.WorkloadPriorityClass{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "test"},
						Value:      50,
					},
				},
			},
			workloadPriorityClassName: "test",
			wantPriorityClassRef:      kueue.NewWorkloadPriorityClassRef("test"),
			wantPriorityClassValue:    50,
		},
		"workloadPriorityClass is specified and it does not exist": {
			workloadPriorityClassList: &kueue.WorkloadPriorityClassList{
				Items: []kueue.WorkloadPriorityClass{},
			},
			workloadPriorityClassName: "test",
			wantErr:                   apierrors.NewNotFound(kueue.Resource("workloadpriorityclasses"), "test"),
		},
	}

	for desc, tt := range tests {
		t.Run(desc, func(t *testing.T) {
			t.Parallel()

			builder := fake.NewClientBuilder().WithScheme(scheme).WithLists(tt.workloadPriorityClassList)
			client := builder.Build()
			ctx, _ := utiltesting.ContextWithLog(t)
			priorityClassRef, value, err := GetPriorityFromWorkloadPriorityClass(ctx, client, tt.workloadPriorityClassName)
			if diff := cmp.Diff(tt.wantErr, err); diff != "" {
				t.Errorf("unexpected error (-want,+got):\n%s", diff)
			}

			if diff := cmp.Diff(tt.wantPriorityClassRef, priorityClassRef); diff != "" {
				t.Errorf("unexpected priortyClassRef (-want,+got):\n%s", diff)
			}

			if value != tt.wantPriorityClassValue {
				t.Errorf("unexpected value: got: %d, expected: %d", value, tt.wantPriorityClassValue)
			}
		})
	}
}
