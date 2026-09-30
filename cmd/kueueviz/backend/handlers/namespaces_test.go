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
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestFetchNamespaces(t *testing.T) {
	tests := map[string]struct {
		objs           []ctrlclient.Object
		funcs          *interceptor.Funcs
		wantNamespaces []string
		wantErr        bool
	}{
		"no local queues returns empty list": {
			wantNamespaces: []string{},
		},
		"returns sorted unique namespaces": {
			objs: []ctrlclient.Object{
				makeLocalQueue("ns-b", "lq-1", "cq-a"),
				makeLocalQueue("ns-a", "lq-2", "cq-a"),
				makeLocalQueue("ns-a", "lq-3", "cq-b"),
				makeLocalQueue("ns-c", "lq-4", "cq-a"),
			},
			wantNamespaces: []string{"ns-a", "ns-b", "ns-c"},
		},
		"list error is returned": {
			funcs:   listErrorFuncs(errors.New("boom")),
			wantErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h := New(newFakeClient(t, tc.funcs, tc.objs...), nil)

			got, err := h.fetchNamespaces(t.Context())
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
		})
	}
}
