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

	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

func TestFetchLocalQueues(t *testing.T) {
	tests := map[string]struct {
		objs    []ctrlclient.Object
		funcs   *interceptor.Funcs
		want    []string
		wantErr bool
	}{
		"no local queues returns empty list": {
			want: []string{},
		},
		"lists all local queues": {
			objs: []ctrlclient.Object{
				makeLocalQueue("ns-1", "lq-1", "cq-a"),
				makeLocalQueue("ns-1", "lq-2", "cq-b"),
				makeLocalQueue("ns-2", "lq-3", "cq-a"),
			},
			want: []string{"ns-1/lq-1/cq-a", "ns-1/lq-2/cq-b", "ns-2/lq-3/cq-a"},
		},
		"list error is returned": {
			funcs:   listErrorFuncs(errors.New("boom")),
			wantErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h := New(newFakeClient(t, tc.funcs, tc.objs...), nil)

			got, err := h.fetchLocalQueues(t.Context())
			if (err != nil) != tc.wantErr {
				t.Fatalf("fetchLocalQueues() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}

			gotQueues := []string{}
			for _, q := range got.([]map[string]any) {
				spec := q["spec"].(map[string]any)
				gotQueues = append(gotQueues, q["namespace"].(string)+"/"+q["name"].(string)+"/"+spec["clusterQueue"].(string))
			}
			if diff := cmp.Diff(tc.want, gotQueues); diff != "" {
				t.Errorf("unexpected local queues (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestFetchLocalQueueDetails(t *testing.T) {
	tests := map[string]struct {
		objs      []ctrlclient.Object
		namespace string
		name      string
		wantErr   bool
	}{
		"returns the local queue": {
			objs:      []ctrlclient.Object{makeLocalQueue("ns-1", "lq-1", "cq-a")},
			namespace: "ns-1",
			name:      "lq-1",
		},
		"same name in another namespace is not found": {
			objs:      []ctrlclient.Object{makeLocalQueue("ns-1", "lq-1", "cq-a")},
			namespace: "ns-2",
			name:      "lq-1",
			wantErr:   true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h := New(newFakeClient(t, nil, tc.objs...), nil)

			got, err := h.fetchLocalQueueDetails(t.Context(), tc.namespace, tc.name)
			if (err != nil) != tc.wantErr {
				t.Fatalf("fetchLocalQueueDetails() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}

			lq := got.(*kueueapi.LocalQueue)
			if lq.Namespace != tc.namespace || lq.Name != tc.name {
				t.Errorf("got %s/%s, want %s/%s", lq.Namespace, lq.Name, tc.namespace, tc.name)
			}
			if lq.Spec.ClusterQueue != "cq-a" {
				t.Errorf("clusterQueue = %q, want %q", lq.Spec.ClusterQueue, "cq-a")
			}
		})
	}
}
