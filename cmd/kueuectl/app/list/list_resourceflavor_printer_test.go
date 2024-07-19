/*
Copyright 2024 The Kubernetes Authors.

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

package list

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	testingclock "k8s.io/utils/clock/testing"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestResourceFlavorPrint(t *testing.T) {
	testStartTime := time.Now()

	testCases := map[string]struct {
		options *ResourceFlavorOptions
		in      *v1beta1.ResourceFlavorList
		out     []metav1.TableRow
	}{
		"should print resource flavor list": {
			options: &ResourceFlavorOptions{},
			in: &v1beta1.ResourceFlavorList{
				Items: []v1beta1.ResourceFlavor{
					*utiltesting.MakeResourceFlavor("rf").
						Creation(testStartTime.Add(-time.Hour).Truncate(time.Second)).
						NodeLabel("key1", "value").
						NodeLabel("key2", "value").
						Obj(),
				},
			},
			out: []metav1.TableRow{
				{
					Cells: []any{"rf", "key1=value, key2=value", "60m"},
					Object: runtime.RawExtension{
						Object: utiltesting.MakeResourceFlavor("rf").
							Creation(testStartTime.Add(-time.Hour).Truncate(time.Second)).
							NodeLabel("key1", "value").
							NodeLabel("key2", "value").
							Obj(),
					},
				},
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			p := newResourceFlavorTablePrinter().WithClock(testingclock.NewFakeClock(testStartTime))
			out := p.printResourceFlavorList(tc.in)
			if diff := cmp.Diff(tc.out, out); diff != "" {
				t.Errorf("Unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}
