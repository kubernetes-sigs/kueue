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

package nodeavoidance

import (
	"testing"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestGetNodeAvoidancePolicy(t *testing.T) {
	testCases := []struct {
		name string
		wl   *kueue.Workload
		want string
	}{
		{
			name: "nil workload",
			wl:   nil,
			want: "",
		},
		{
			name: "workload without annotations",
			wl:   utiltesting.MakeWorkload("wl", "ns").Obj(),
			want: "",
		},
		{
			name: "workload with NoSchedule",
			wl: utiltesting.MakeWorkload("wl", "ns").
				Annotations(map[string]string{
					constants.NodeAvoidancePolicyAnnotation: constants.NodeAvoidancePolicyNoSchedule,
				}).Obj(),
			want: constants.NodeAvoidancePolicyNoSchedule,
		},
		{
			name: "workload with PreferNoSchedule",
			wl: utiltesting.MakeWorkload("wl", "ns").
				Annotations(map[string]string{
					constants.NodeAvoidancePolicyAnnotation: constants.NodeAvoidancePolicyPreferNoSchedule,
				}).Obj(),
			want: constants.NodeAvoidancePolicyPreferNoSchedule,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := GetNodeAvoidancePolicy(tc.wl)
			if got != tc.want {
				t.Errorf("GetNodeAvoidancePolicy() = %v, want %v", got, tc.want)
			}
		})
	}
}
