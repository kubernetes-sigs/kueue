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
	"encoding/json"
	"testing"

	"kueueviz/middleware"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func TestFetchEmptyResourceLists(t *testing.T) {
	tests := map[string]struct {
		objs  []ctrlclient.Object
		fetch func(*Handlers) (any, error)
	}{
		"local queues": {
			fetch: func(h *Handlers) (any, error) {
				return h.fetchLocalQueues(t.Context(), "", middleware.Identity{})
			},
		},
		"local queues in an empty namespace": {
			fetch: func(h *Handlers) (any, error) {
				return h.fetchLocalQueues(t.Context(), "team-b", middleware.Identity{})
			},
		},
		"local queues exist only in another namespace": {
			objs: []ctrlclient.Object{makeLocalQueue("team-a", "lq-a", "cq-main")},
			fetch: func(h *Handlers) (any, error) {
				return h.fetchLocalQueues(t.Context(), "team-b", middleware.Identity{})
			},
		},
		"cluster queues": {
			fetch: func(h *Handlers) (any, error) {
				return h.fetchClusterQueues(t.Context())
			},
		},
		"cohorts": {
			fetch: func(h *Handlers) (any, error) {
				return h.fetchCohorts(t.Context())
			},
		},
		"resource flavors": {
			fetch: func(h *Handlers) (any, error) {
				return h.fetchResourceFlavors(t.Context())
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h := New(newFakeClient(t, nil, tc.objs...), nil, nil)
			got, err := tc.fetch(h)
			if err != nil {
				t.Fatalf("fetch() error = %v", err)
			}
			data, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("marshaling resource list: %v", err)
			}
			if string(data) != "[]" {
				t.Errorf("resource list JSON = %s, want []", data)
			}
		})
	}
}
