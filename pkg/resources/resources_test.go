// /*
// Copyright 2025 The Kubernetes Authors.

// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at

// 	http://www.apache.org/licenses/LICENSE-2.0

// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// */

package resources

// import (
// 	"testing"

// 	"github.com/google/go-cmp/cmp"
// 	corev1 "k8s.io/api/core/v1"
// )

// func TestAddResources(t *testing.T) {
// 	cases := map[string]struct {
// 		a    map[corev1.ResourceName]int64
// 		b    map[corev1.ResourceName]int64
// 		want map[corev1.ResourceName]int64
// 	}{
// 		"add two resource lists": {
// 			a: map[corev1.ResourceName]int64{
// 				corev1.ResourceCPU:    1,
// 				corev1.ResourceMemory: 2,
// 			},
// 			b: map[corev1.ResourceName]int64{
// 				corev1.ResourceCPU:     3,
// 				corev1.ResourceMemory:  4,
// 				corev1.ResourceStorage: 5,
// 			},
// 			want: map[corev1.ResourceName]int64{
// 				corev1.ResourceCPU:     4,
// 				corev1.ResourceMemory:  6,
// 				corev1.ResourceStorage: 5,
// 			},
// 		},
// 		"add with empty map b": {
// 			a: map[corev1.ResourceName]int64{
// 				corev1.ResourceCPU:    1,
// 				corev1.ResourceMemory: 2,
// 			},
// 			b: map[corev1.ResourceName]int64{},
// 			want: map[corev1.ResourceName]int64{
// 				corev1.ResourceCPU:    1,
// 				corev1.ResourceMemory: 2,
// 			},
// 		},
// 		"add with empty map a": {
// 			a: map[corev1.ResourceName]int64{},
// 			b: map[corev1.ResourceName]int64{
// 				corev1.ResourceCPU:    1,
// 				corev1.ResourceMemory: 2,
// 			},
// 			want: map[corev1.ResourceName]int64{
// 				corev1.ResourceCPU:    1,
// 				corev1.ResourceMemory: 2,
// 			},
// 		},
// 		"add two empty maps": {
// 			a:    map[corev1.ResourceName]int64{},
// 			b:    map[corev1.ResourceName]int64{},
// 			want: map[corev1.ResourceName]int64{},
// 		},
// 	}

// 	for name, tc := range cases {
// 		t.Run(name, func(t *testing.T) {
// 			got := AddResources(tc.a, tc.b)
// 			if diff := cmp.Diff(tc.want, got); diff != "" {
// 				t.Errorf("Unexpected result (-want, +got)\n%s", diff)
// 			}
// 		})
// 	}
// }
