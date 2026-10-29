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
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	authorizationv1 "k8s.io/api/authorization/v1"
	"kueueviz/middleware"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestFetchNamespaces(t *testing.T) {
	objs := []ctrlclient.Object{
		makeLocalQueue("ns-b", "lq-1", "cq-a"),
		makeLocalQueue("ns-a", "lq-2", "cq-a"),
		makeLocalQueue("ns-a", "lq-3", "cq-b"),
		makeLocalQueue("ns-c", "lq-4", "cq-a"),
	}

	tests := map[string]struct {
		objs                 []ctrlclient.Object
		funcs                *interceptor.Funcs
		authorizer           middleware.Authorizer
		wantNamespaces       []string
		wantHasClusterAccess bool
		wantErr              bool
	}{
		"no local queues returns empty list": {
			wantNamespaces:       []string{},
			wantHasClusterAccess: true,
		},
		"without authorizer returns sorted unique namespaces": {
			objs:                 objs,
			wantNamespaces:       []string{"ns-a", "ns-b", "ns-c"},
			wantHasClusterAccess: true,
		},
		"denied namespaces are filtered out": {
			objs: objs,
			authorizer: authorizerFunc(func(attrs authorizationv1.ResourceAttributes) (bool, error) {
				return attrs.Namespace == "ns-a" || attrs.Namespace == "ns-c", nil
			}),
			wantNamespaces:       []string{"ns-a", "ns-c"},
			wantHasClusterAccess: false,
		},
		"cluster wide access is reported": {
			objs: objs,
			authorizer: authorizerFunc(func(authorizationv1.ResourceAttributes) (bool, error) {
				return true, nil
			}),
			wantNamespaces:       []string{"ns-a", "ns-b", "ns-c"},
			wantHasClusterAccess: true,
		},
		"authorizer error is treated as denied": {
			objs: objs,
			authorizer: authorizerFunc(func(authorizationv1.ResourceAttributes) (bool, error) {
				return true, errors.New("boom")
			}),
			wantNamespaces:       []string{},
			wantHasClusterAccess: false,
		},
		"list error is returned": {
			funcs:   listErrorFuncs(errors.New("boom")),
			wantErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h := New(newFakeClient(t, tc.funcs, tc.objs...), nil, tc.authorizer)

			got, err := h.fetchNamespaces(t.Context(), middleware.Identity{Username: "alice"})
			if (err != nil) != tc.wantErr {
				t.Fatalf("fetchNamespaces() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}

			result := got.(map[string]any)
			if diff := cmp.Diff(tc.wantNamespaces, result["namespaces"]); diff != "" {
				t.Errorf("unexpected namespaces (-want,+got):\n%s", diff)
			}
			if result["hasClusterAccess"] != tc.wantHasClusterAccess {
				t.Errorf("hasClusterAccess = %v, want %v", result["hasClusterAccess"], tc.wantHasClusterAccess)
			}
		})
	}
}
