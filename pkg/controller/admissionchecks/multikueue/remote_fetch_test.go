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
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestGetOrList(t *testing.T) {
	cq1 := utiltestingapi.MakeClusterQueue("cq1").Obj()
	cq2 := utiltestingapi.MakeClusterQueue("cq2").Obj()

	cases := map[string]struct {
		keys     sets.Set[types.NamespacedName]
		want     []kueue.ClusterQueue
		wantGet  bool
		wantList bool
	}{
		"empty keys": {
			keys:     sets.New[types.NamespacedName](),
			want:     nil,
			wantGet:  false,
			wantList: false,
		},
		"single key, found": {
			keys:     sets.New(types.NamespacedName{Name: "cq1"}),
			want:     []kueue.ClusterQueue{*cq1},
			wantGet:  true,
			wantList: false,
		},
		"single key, not found": {
			keys:     sets.New(types.NamespacedName{Name: "cq3"}),
			want:     nil,
			wantGet:  true,
			wantList: false,
		},
		"multiple keys, some found": {
			keys:     sets.New(types.NamespacedName{Name: "cq1"}, types.NamespacedName{Name: "cq3"}),
			want:     []kueue.ClusterQueue{*cq1},
			wantGet:  false,
			wantList: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			var getCalled, listCalled bool
			client := getClientBuilder(ctx).
				WithObjects(cq1, cq2).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						getCalled = true
						return c.Get(ctx, key, obj, opts...)
					},
					List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						listCalled = true
						return c.List(ctx, list, opts...)
					},
				}).
				Build()

			got, err := getOrList(ctx, client, tc.keys, cqItemsGetter{})

			if err != nil {
				t.Fatalf("unexpected getOrList() error: %v", err)
			}
			if getCalled != tc.wantGet {
				t.Errorf("Get() called = %v, want %v", getCalled, tc.wantGet)
			}
			if listCalled != tc.wantList {
				t.Errorf("List() called = %v, want %v", listCalled, tc.wantList)
			}

			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty(), cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"), cmpopts.SortSlices(func(a, b kueue.ClusterQueue) bool {
				return a.Name < b.Name
			})); diff != "" {
				t.Errorf("getOrList() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
